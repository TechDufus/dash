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
	"unicode/utf8"

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
			got, err := BuildQuery(tc.q, DefaultLogFields(), tc.tail)
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
				if _, err := BuildQuery(tc.q, DefaultLogFields(), tail); !errors.Is(err, ErrInvalid) {
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

	lines, err := NewVLogs(srv.URL, nil, 500).Query(context.Background(), LogQuery{Namespace: "n", Pod: "p1"})
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
	v := NewVLogs(srv.URL, nil, 0)

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
	if _, err := NewVLogs("", nil, 0).Query(context.Background(), LogQuery{Namespace: "n"}); !errors.Is(err, ErrDisabled) {
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
	err := NewVLogs(srv.URL, nil, 0).Tail(ctx, LogQuery{Namespace: "n", Pod: "p1", Text: "x"}, func(l model.LogLine) error {
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

func TestVLogsTailStartOffset(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { raw = r.URL.RawQuery }))
	defer srv.Close()
	for _, tc := range []struct {
		off  time.Duration
		want string // start_offset value, empty = parameter absent
	}{
		{0, ""},
		{-time.Second, ""},
		{35 * time.Second, "35s"},
		{1500 * time.Millisecond, "2s"},
		{time.Minute, "60s"},
	} {
		raw = ""
		q := LogQuery{Namespace: "n", StartOffset: tc.off}
		if err := NewVLogs(srv.URL, nil, 0).Tail(context.Background(), q, func(model.LogLine) error { return nil }); err != nil {
			t.Fatal(err)
		}
		got, perr := url.ParseQuery(raw)
		if perr != nil {
			t.Fatal(perr)
		}
		if v := got.Get("start_offset"); v != tc.want || got.Has("start_offset") != (tc.want != "") {
			t.Errorf("offset %v: start_offset = %q (raw %q), want %q", tc.off, v, raw, tc.want)
		}
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
		l, ok := recordLine(rec, DefaultLogFields())
		if !ok || l.Msg != tc.want || l.Pod != "dash-1" || l.Container != "dash" {
			t.Errorf("%s: got %+v ok=%v, want msg %q", tc.name, l, ok, tc.want)
		}
	}
	if _, ok := recordLine(map[string]string{"_msg": "x", "_time": "nope"}, DefaultLogFields()); ok {
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
	err := NewVLogs(srv.URL, nil, 0).Tail(context.Background(), LogQuery{Namespace: "n"}, func(model.LogLine) error {
		n++
		return boom
	})
	if !errors.Is(err, boom) || n != 1 {
		t.Fatalf("err = %v after %d emits", err, n)
	}

	// Stream ending on its own is a nil return.
	if err := NewVLogs(srv.URL, nil, 0).Tail(context.Background(), LogQuery{Namespace: "n"}, func(model.LogLine) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestReadLogLinesSkipsOversizedRecord(t *testing.T) {
	huge := `{"_msg":"` + strings.Repeat("x", 300<<10) + `","_time":"2026-10-30T21:39:17.5Z"}`
	var got []model.LogLine
	err := readLogLines(strings.NewReader(vlOldest+"\n"+huge+"\n"+vlNewest+"\n"), DefaultLogFields(), func(l model.LogLine) error {
		got = append(got, l)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Msg != "first" || got[1].Msg != "second" {
		t.Fatalf("got %d lines %+v, want first and second", len(got), got)
	}
}

func TestRecordLineCapsMessage(t *testing.T) {
	l, ok := recordLine(map[string]string{"_time": "2026-10-30T21:39:17.5Z", "_msg": strings.Repeat("é", 10000)}, DefaultLogFields())
	if !ok {
		t.Fatal("record skipped")
	}
	if n := utf8.RuneCountInString(l.Msg); n != maxLogMsg+1 || !strings.HasSuffix(l.Msg, "…") {
		t.Errorf("message has %d runes, suffix %q; want %d ending in …", n, l.Msg[len(l.Msg)-3:], maxLogMsg+1)
	}
}

var customFields = LogFields{Namespace: "k8s.namespace", Pod: "k8s.pod", Container: "ctr"}

func TestBuildQueryUsesTheConfiguredFields(t *testing.T) {
	q := LogQuery{Namespace: "n", Pod: "p", Container: "c", Text: "boom"}
	got, err := BuildQuery(q, customFields, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `_time:15m k8s.namespace:="n" k8s.pod:="p" ctr:="c" "boom" | sort by (_time desc)`
	if got != want {
		t.Errorf("query = %s\nwant    %s", got, want)
	}
	tail, err := BuildQuery(q, customFields, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := `k8s.namespace:="n" k8s.pod:="p" ctr:="c" "boom"`; tail != want {
		t.Errorf("tail query = %s\nwant         %s", tail, want)
	}
}

func TestVLogsReadsPodAndContainerThroughTheMapping(t *testing.T) {
	var gotQuery, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotLimit = r.URL.Query().Get("query"), r.URL.Query().Get("limit")
		fmt.Fprint(w, `{"_msg":"hello","_time":"2026-10-30T21:39:17.5Z","k8s.namespace":"n","k8s.pod":"p1","ctr":"c1","k8s.node":"node-a","level":"info"}`+"\n")
	}))
	defer srv.Close()

	v := NewVLogs(srv.URL, nil, 50).WithFields(customFields)
	lines, err := v.Query(context.Background(), LogQuery{Namespace: "n", Pod: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if gotLimit != "50" || gotQuery != `_time:15m k8s.namespace:="n" k8s.pod:="p1" | sort by (_time desc)` {
		t.Errorf("request: limit=%q query=%q", gotLimit, gotQuery)
	}
	// k8s.* is the family of the mapped fields, ctr is hidden by name; level stays in the message.
	if len(lines) != 1 || lines[0].Pod != "p1" || lines[0].Container != "c1" || lines[0].Msg != "hello level=info" {
		t.Errorf("lines = %+v", lines)
	}

	// WithFields copies: the client it was called on keeps the default mapping.
	if q, _ := BuildQuery(LogQuery{Namespace: "n"}, v.fields, true); !strings.HasPrefix(q, "k8s.namespace:=") {
		t.Errorf("the derived client lost its fields: %s", q)
	}
	base := NewVLogs(srv.URL, nil, 0)
	_ = base.WithFields(customFields)
	if base.fields != DefaultLogFields() {
		t.Errorf("WithFields changed the receiver: %+v", base.fields)
	}
}

func TestVLogsLimitDefaults(t *testing.T) {
	var gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotLimit = r.URL.Query().Get("limit") }))
	defer srv.Close()
	for _, limit := range []int{0, -3} {
		gotLimit = ""
		if _, err := NewVLogs(srv.URL, nil, limit).Query(context.Background(), LogQuery{Namespace: "n"}); err != nil {
			t.Fatal(err)
		}
		if gotLimit != "500" {
			t.Errorf("limit %d sent %q, want 500", limit, gotLimit)
		}
	}
}

// The family of hidden fields is derived from the configured names: up to and including the
// first dot, so the default mapping hides kubernetes.* (the pod IP, the labels), and a mapping
// into another prefix hides that one instead.
func TestLogFieldsHidden(t *testing.T) {
	tests := []struct {
		name   string
		fields LogFields
		key    string
		hidden bool
	}{
		{"default hides the pod ip", DefaultLogFields(), "kubernetes.pod_ip", true},
		{"default hides a label", DefaultLogFields(), "kubernetes.pod_labels.app", true},
		{"default shows a plain field", DefaultLogFields(), "level", false},
		{"internal fields", DefaultLogFields(), "_stream_id", true},
		{"stream", DefaultLogFields(), "stream", true},
		{"another prefix is shown by the default mapping", DefaultLogFields(), "k8s.node", false},
		{"a mapped prefix hides its family", customFields, "k8s.node", true},
		{"the default family is shown when unmapped", customFields, "kubernetes.pod_ip", false},
		{"an undotted field is hidden by name", customFields, "ctr", true},
		{"and only by name", customFields, "ctr2", false},
		{"an undotted mapping hides no family", LogFields{Namespace: "ns", Pod: "pod", Container: "container"}, "pod_ip", false},
		{"a prefix needs its dot", LogFields{Namespace: "a.ns", Pod: "a.pod", Container: "a.ctr"}, "ab", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fields.hidden(tc.key); got != tc.hidden {
				t.Errorf("hidden(%q) = %v, want %v", tc.key, got, tc.hidden)
			}
		})
	}
}
