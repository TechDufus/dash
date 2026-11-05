package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

// cutSecret is a registered secret the cut tests place across a cut.
const cutSecret = "client-secret-canary-55aa-0123456789"

func cutRegistry(secrets ...string) *secret.Registry {
	reg := secret.NewRegistry()
	for _, s := range append(secrets, cutSecret) {
		reg.Add(secret.New(s, ""))
	}
	return reg
}

// statusError is the StatusError of a GET against an upstream that answers 500 with body.
func statusError(t *testing.T, body string, opts ...Option) *StatusError {
	t.Helper()
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, body)
	})
	_, err := newClient(s.URL, nil, 5*time.Second, opts...).open(ctx, http.MethodGet, "/", nil, nil, nil)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a StatusError", err)
	}
	return se
}

// An error body is cut to maxErrorText runes by the client's cut: a registered secret or a token
// that straddles the cut is redacted whole, even one of 4 KiB (the body read reaches past it),
// and plain text is cut exactly as before.
func TestStatusErrorBodyKeepsNoPartOfASecret(t *testing.T) {
	big := "B" + strings.Repeat("0123456789abcdef", 256) // 4097 bytes, starting before rune 200
	reg := cutRegistry(big)
	for _, tc := range []struct {
		name, body, want string
		opts             []Option
	}{
		{"registered secret after two-byte runes", strings.Repeat("é", 192) + cutSecret + " tail",
			strings.Repeat("é", 192) + "[redacted]", []Option{Cut(reg.CutRunes)}},
		{"4 KiB registered secret", strings.Repeat("x", 190) + big + " tail",
			strings.Repeat("x", 190) + "[redacted]", []Option{Cut(reg.CutRunes)}},
		{"token without the option", strings.Repeat("x", 195) + "dash_pat_abcdefghij0123 tail",
			strings.Repeat("x", 195) + "[redacted]", nil},
		{"plain runes", "  " + strings.Repeat("é", 300) + "\n", strings.Repeat("é", 200), []Option{Cut(reg.CutRunes)}},
		{"plain body past the old read limit", strings.Repeat("y", 10000), strings.Repeat("y", 200), []Option{Cut(reg.CutRunes)}},
		{"short body", "  not found\n", "not found", []Option{Cut(reg.CutRunes)}},
	} {
		se := statusError(t, tc.body, tc.opts...)
		if se.Body != tc.want {
			t.Errorf("%s: Body = %.60q…, want %.60q…", tc.name, se.Body, tc.want)
		}
		for _, sec := range []string{cutSecret, big} {
			if strings.Contains(se.Body, sec[:8]) {
				t.Errorf("%s: Body keeps the start of a secret: %q", tc.name, se.Body)
			}
		}
	}
}

// countingBody is an endless error body that counts the bytes read from it.
type countingBody struct {
	mu sync.Mutex
	n  int
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range p {
		p[i] = "dash_pat_a "[(b.n+i)%11]
	}
	b.n += len(p)
	return len(p), nil
}

func (b *countingBody) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// However long the error body, the client reads at most maxErrorBody of it, its cut sees no more
// than that, and the StatusError keeps maxErrorText runes and a marker.
func TestStatusErrorReadIsBounded(t *testing.T) {
	body := &countingBody{}
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: body, Header: http.Header{}, Request: r}, nil
	})
	seen := 0
	cut := func(s string, n int) (string, bool) { seen = len(s); return cutRegistry().CutRunes(s, n) }
	_, err := newClient("http://upstream.example", rt, 5*time.Second, Cut(cut)).open(ctx, http.MethodGet, "/", nil, nil, nil)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v", err)
	}
	body.mu.Lock()
	read := body.n
	body.mu.Unlock()
	if read > maxErrorBody+512 || seen > maxErrorBody {
		t.Errorf("read %d bytes and cut %d, want at most %d", read, seen, maxErrorBody)
	}
	if len(se.Body) > maxErrorText+len("[redacted]") {
		t.Errorf("Body is %d bytes", len(se.Body))
	}
}

// A query's error text is cut the same way.
func TestPromQueryErrorKeepsNoPartOfASecret(t *testing.T) {
	reg := cutRegistry()
	for _, tc := range []struct{ msg, want string }{
		{strings.Repeat("x", maxErrorText-8) + cutSecret + " tail", strings.Repeat("x", maxErrorText-8) + "[redacted]"},
		{strings.Repeat("é", 300), strings.Repeat("é", maxErrorText)},
	} {
		s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			b, _ := json.Marshal(map[string]string{"status": "error", "errorType": "bad_data", "error": tc.msg})
			_, _ = w.Write(b)
		})
		_, err := NewProm(s.URL, nil, Cut(reg.CutRunes)).Instant(ctx, "up")
		if err == nil || !strings.HasSuffix(err.Error(), "query failed: bad_data "+tc.want) || strings.Contains(err.Error(), cutSecret[:8]) {
			t.Errorf("err = %v, want it to end in %.40q…", err, tc.want)
		}
	}
}

// A log message over maxLogMsg runes is cut by the client's cut, on a query and on a tail: a
// secret across the cut is redacted whole, before the "…".
func TestLogMessageKeepsNoPartOfASecret(t *testing.T) {
	reg := cutRegistry()
	record := func(msg string) string {
		b, _ := json.Marshal(map[string]string{"_msg": msg, "_time": "2026-10-30T21:39:17.5Z"})
		return string(b)
	}
	pad := strings.Repeat("é", maxLogMsg-8)
	for _, tc := range []struct{ msg, want string }{
		{pad + cutSecret + " tail", pad + "[redacted]…"},
		{strings.Repeat("é", 10000), strings.Repeat("é", maxLogMsg) + "…"},
		{"short line", "short line"},
	} {
		s := serve(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, record(tc.msg)) })
		v := NewVLogs(s.URL, nil, 0, Cut(reg.CutRunes))
		lines, err := v.Query(ctx, LogQuery{Namespace: "n"})
		if err != nil || len(lines) != 1 || lines[0].Msg != tc.want {
			t.Errorf("query: %v %d lines, want %.40q…", err, len(lines), tc.want)
		}
		var tailed []model.LogLine
		if err := v.Tail(ctx, LogQuery{Namespace: "n"}, func(l model.LogLine) error { tailed = append(tailed, l); return nil }); err != nil || len(tailed) != 1 || tailed[0].Msg != tc.want {
			t.Errorf("tail: %v %d lines, want %.40q…", err, len(tailed), tc.want)
		}
		for _, l := range append(lines, tailed...) {
			if strings.Contains(l.Msg, cutSecret[:8]) {
				t.Errorf("a line keeps the start of the secret: …%q", l.Msg[max(0, len(l.Msg)-40):])
			}
		}
	}
}
