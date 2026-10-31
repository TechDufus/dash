package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

func TestBuildQuery(t *testing.T) {
	long := strings.Repeat("a", 253)
	tests := []struct {
		name string
		q    LogQuery
		tail bool
		want string
	}{
		{"ns only", LogQuery{Namespace: "gatus"},
			false, `_time:15m kubernetes.pod_namespace:="gatus" | sort by (_time desc)`},
		{"ns and pod", LogQuery{Namespace: "gatus", Pod: "gatus-abc-1"},
			false, `_time:15m kubernetes.pod_namespace:="gatus" kubernetes.pod_name:="gatus-abc-1" | sort by (_time desc)`},
		{"all 5m", LogQuery{Namespace: "n", Pod: "p", Container: "c", Text: "boom", Since: "5m"},
			false, `_time:5m kubernetes.pod_namespace:="n" kubernetes.pod_name:="p" kubernetes.container_name:="c" "boom" | sort by (_time desc)`},
		{"all 15m", LogQuery{Namespace: "n", Pod: "p", Container: "c", Text: "boom", Since: "15m"},
			false, `_time:15m kubernetes.pod_namespace:="n" kubernetes.pod_name:="p" kubernetes.container_name:="c" "boom" | sort by (_time desc)`},
		{"all 1h", LogQuery{Namespace: "n", Pod: "p", Container: "c", Text: "boom", Since: "1h"},
			false, `_time:1h kubernetes.pod_namespace:="n" kubernetes.pod_name:="p" kubernetes.container_name:="c" "boom" | sort by (_time desc)`},
		{"all 6h", LogQuery{Namespace: "n", Pod: "p", Container: "c", Text: "boom", Since: "6h"},
			false, `_time:6h kubernetes.pod_namespace:="n" kubernetes.pod_name:="p" kubernetes.container_name:="c" "boom" | sort by (_time desc)`},
		{"all 24h", LogQuery{Namespace: "n", Pod: "p", Container: "c", Text: "boom", Since: "24h"},
			false, `_time:24h kubernetes.pod_namespace:="n" kubernetes.pod_name:="p" kubernetes.container_name:="c" "boom" | sort by (_time desc)`},
		{"tail ignores since", LogQuery{Namespace: "n", Pod: "p", Text: "boom", Since: "6h"},
			true, `kubernetes.pod_namespace:="n" kubernetes.pod_name:="p" "boom"`},
		{"quote and backslash escaped", LogQuery{Namespace: "n", Text: `say "hi" \o/`},
			false, `_time:15m kubernetes.pod_namespace:="n" "say \"hi\" \\o/" | sort by (_time desc)`},
		{"newline NUL and CR dropped", LogQuery{Namespace: "n", Text: "a\nb\x00c\r"},
			false, `_time:15m kubernetes.pod_namespace:="n" "abc" | sort by (_time desc)`},
		{"text only breaks leaves no clause", LogQuery{Namespace: "n", Text: "\n\n"},
			false, `_time:15m kubernetes.pod_namespace:="n" | sort by (_time desc)`},
		{"text truncated before escaping", LogQuery{Namespace: "n", Text: strings.Repeat(`"`, 250)},
			false, `_time:15m kubernetes.pod_namespace:="n" "` + strings.Repeat(`\"`, 200) + `" | sort by (_time desc)`},
		{"truncation is rune safe", LogQuery{Namespace: "n", Text: strings.Repeat("é", 201)},
			false, `_time:15m kubernetes.pod_namespace:="n" "` + strings.Repeat("é", 200) + `" | sort by (_time desc)`},
		{"253 char namespace ok", LogQuery{Namespace: long},
			false, `_time:15m kubernetes.pod_namespace:="` + long + `" | sort by (_time desc)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildQuery(tc.q, tc.tail)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestBuildQueryInvalid(t *testing.T) {
	tests := []struct {
		name string
		q    LogQuery
	}{
		{"uppercase namespace", LogQuery{Namespace: "Foo"}},
		{"slash namespace", LogQuery{Namespace: "a/b"}},
		{"empty namespace", LogQuery{}},
		{"254 char namespace", LogQuery{Namespace: strings.Repeat("a", 254)}},
		{"namespace with quote", LogQuery{Namespace: `a"b`}},
		{"leading dash namespace", LogQuery{Namespace: "-a"}},
		{"bad pod", LogQuery{Namespace: "n", Pod: "Pod_1"}},
		{"pod with space", LogQuery{Namespace: "n", Pod: "a b"}},
		{"bad container", LogQuery{Namespace: "n", Container: "c/d"}},
		{"bad since", LogQuery{Namespace: "n", Since: "2d"}},
		{"since injection", LogQuery{Namespace: "n", Since: "5m | delete"}},
	}
	for _, tc := range tests {
		for _, tail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tail=%v", tc.name, tail), func(t *testing.T) {
				if _, err := BuildQuery(tc.q, tail); !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
			})
		}
	}
}

const vlNewest = `{"_msg":"second","_time":"2026-10-30T21:39:17.5Z","kubernetes.pod_name":"p1","kubernetes.container_name":"c1","kubernetes.pod_namespace":"n"}`
const vlOldest = `{"_msg":"first","_time":"2026-10-30T21:39:16.940789516Z","kubernetes.pod_name":"p1","kubernetes.container_name":"c1","kubernetes.pod_namespace":"n"}`

func TestVLogsQuery(t *testing.T) {
	var gotQuery, gotLimit, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotLimit = r.URL.Path, r.URL.Query().Get("query"), r.URL.Query().Get("limit")
		// Newest first, as the query sorts; one junk line that must be skipped.
		fmt.Fprintf(w, "%s\nnot json\n%s\n", vlNewest, vlOldest)
	}))
	defer srv.Close()

	lines, err := NewVLogs(srv.URL, nil).Query(context.Background(), LogQuery{Namespace: "n", Pod: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/select/logsql/query" || gotLimit != "500" ||
		gotQuery != `_time:15m kubernetes.pod_namespace:="n" kubernetes.pod_name:="p1" | sort by (_time desc)` {
		t.Fatalf("request: path=%q limit=%q query=%q", gotPath, gotLimit, gotQuery)
	}
	if len(lines) != 2 || lines[0].Msg != "first" || lines[1].Msg != "second" {
		t.Fatalf("lines not chronological: %+v", lines)
	}
	if lines[0].Pod != "p1" || lines[0].Container != "c1" || !lines[0].Time.Equal(time.Date(2026, 10, 30, 21, 39, 16, 940789516, time.UTC)) {
		t.Fatalf("line fields: %+v", lines[0])
	}
}

func TestVLogsQueryEmptyAndErrors(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status != http.StatusOK {
			fmt.Fprint(w, strings.Repeat("x", 500))
		}
	}))
	defer srv.Close()
	v := NewVLogs(srv.URL, nil)

	lines, err := v.Query(context.Background(), LogQuery{Namespace: "n"})
	if err != nil || lines == nil || len(lines) != 0 {
		t.Fatalf("want empty non-nil, got %v %v", lines, err)
	}

	status = http.StatusBadGateway
	_, err = v.Query(context.Background(), LogQuery{Namespace: "n"})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 502 || len(se.Body) != 200 {
		t.Fatalf("err = %v", err)
	}

	if _, err := v.Query(context.Background(), LogQuery{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if _, err := NewVLogs("", nil).Query(context.Background(), LogQuery{Namespace: "n"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

func TestVLogsTail(t *testing.T) {
	var gotQuery, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotRawQuery = r.URL.Query().Get("query"), r.URL.RawQuery
		fmt.Fprintf(w, "%s\n%s\n", vlOldest, vlNewest)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // stay open like a real tail
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []string
	err := NewVLogs(srv.URL, nil).Tail(ctx, LogQuery{Namespace: "n", Pod: "p1", Text: "x"}, func(l model.LogLine) error {
		got = append(got, l.Msg)
		if len(got) == 2 {
			cancel() // client goes away
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tail after cancel = %v, want nil", err)
	}
	if strings.Join(got, ",") != "first,second" {
		t.Fatalf("got %v", got)
	}
	if gotQuery != `kubernetes.pod_namespace:="n" kubernetes.pod_name:="p1" "x"` {
		t.Fatalf("query = %q", gotQuery)
	}
	wantRaw := "offset=1s&query=" + url.QueryEscape(`kubernetes.pod_namespace:="n" kubernetes.pod_name:="p1" "x"`)
	if gotRawQuery != wantRaw {
		t.Fatalf("raw query = %q, want %q", gotRawQuery, wantRaw)
	}
}

func TestRecordLineAppendsFields(t *testing.T) {
	base := map[string]string{
		"_time": "2026-10-30T21:47:02.295551Z", "_msg": "audit", "_stream": "{x}", "_stream_id": "1", "stream": "stderr",
		"kubernetes.pod_name": "dash-1", "kubernetes.container_name": "dash", "kubernetes.pod_ip": "10.0.0.1",
	}
	tests := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"plain message", nil, "audit"},
		{"sorted fields", map[string]string{"user": "TechDufus", "action": "restart", "level": "INFO"}, "audit action=restart level=INFO user=TechDufus"},
		{"values with spaces are quoted", map[string]string{"error": "boom happened", "result": "error"}, `audit error="boom happened" result=error`},
	}
	for _, tc := range tests {
		rec := map[string]string{}
		for k, v := range base {
			rec[k] = v
		}
		for k, v := range tc.extra {
			rec[k] = v
		}
		l, ok := recordLine(rec)
		if !ok || l.Msg != tc.want || l.Pod != "dash-1" || l.Container != "dash" {
			t.Errorf("%s: got %+v ok=%v, want msg %q", tc.name, l, ok, tc.want)
		}
	}
	if _, ok := recordLine(map[string]string{"_msg": "x", "_time": "nope"}); ok {
		t.Error("a record with a bad _time must be skipped")
	}
}

func TestVLogsTailEmitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s\n%s\n", vlOldest, vlNewest)
	}))
	defer srv.Close()
	boom := errors.New("boom")
	n := 0
	err := NewVLogs(srv.URL, nil).Tail(context.Background(), LogQuery{Namespace: "n"}, func(model.LogLine) error {
		n++
		return boom
	})
	if !errors.Is(err, boom) || n != 1 {
		t.Fatalf("err = %v after %d emits", err, n)
	}

	// Stream ending on its own is a nil return.
	if err := NewVLogs(srv.URL, nil).Tail(context.Background(), LogQuery{Namespace: "n"}, func(model.LogLine) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
