package plugin_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// failingEnv cannot build a client: its CA file is unreadable.
type failingEnv struct{}

func (failingEnv) HTTPClient(plugin.HTTPConfig) (*http.Client, error) {
	return nil, errors.New("reading CA file: open /secret/ca.pem: no such file")
}

func (failingEnv) Now() time.Time { return time.Unix(0, 0) }

func TestCheckReadCategories(t *testing.T) {
	t.Run("client that cannot be built is tls", func(t *testing.T) {
		got := plugin.CheckRead(context.Background(), &failingEnv{}, "probe", plugin.HTTPConfig{}, "http://192.0.2.1/", nil)
		want := plugin.CheckResult{Name: "probe", Category: "tls", Message: "TLS or certificate error"}
		if got != want {
			t.Errorf("result = %+v, want %+v", got, want)
		}
	})

	t.Run("headers are sent and the latency is the clock's", func(t *testing.T) {
		var seen string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Get("X-Probe")
		}))
		defer srv.Close()
		readings := 0
		env := &plugintest.CheckEnv{Clock: func() time.Time {
			readings++
			return time.Unix(0, 0).Add(time.Duration(readings) * 40 * time.Millisecond)
		}}
		got := plugin.CheckRead(context.Background(), env, "probe", plugin.HTTPConfig{}, srv.URL, http.Header{"X-Probe": {"yes"}})
		want := plugin.CheckResult{Name: "probe", OK: true, Message: "connected", LatencyMs: 40}
		if got != want || seen != "yes" {
			t.Errorf("result = %+v (X-Probe %q), want %+v with X-Probe sent", got, seen, want)
		}
	})

	t.Run("blocked address wrapped by the client is unreachable", func(t *testing.T) {
		env := &plugintest.CheckEnv{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial tcp 127.0.0.1:80: refused: %w", plugin.ErrBlockedAddress)
		})}
		got := plugin.CheckRead(context.Background(), env, "probe", plugin.HTTPConfig{}, "http://127.0.0.1/", nil)
		if got.OK || got.Category != "unreachable" || got.Message != "blocked address" {
			t.Errorf("result = %+v, want unreachable with the message \"blocked address\"", got)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// countingTransport is http.DefaultTransport with a count of the response body bytes the
// client read.
type countingTransport struct{ read atomic.Int64 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = countingBody{ReadCloser: resp.Body, read: &c.read}
	return resp, nil
}

type countingBody struct {
	io.ReadCloser
	read *atomic.Int64
}

func (b countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read.Add(int64(n))
	return n, err
}

func TestCheckReadBody(t *testing.T) {
	t.Run("a 2xx body that breaks off is unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n" + strings.Repeat("x", 10))
			_ = buf.Flush()
			_ = conn.Close()
		}))
		defer srv.Close()
		got := plugin.CheckRead(context.Background(), &plugintest.CheckEnv{}, "probe", plugin.HTTPConfig{}, srv.URL, nil)
		if got.OK || got.Category != "unreachable" || got.Message != plugin.CheckMessage("unreachable") {
			t.Errorf("result = %+v, want unreachable", got)
		}
	})

	t.Run("a large 2xx body passes after reading only its start", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bw := bufio.NewWriterSize(w, 4096)
			chunk := []byte(strings.Repeat("y", 4096))
			for range 4096 { // 16 MiB
				if _, err := bw.Write(chunk); err != nil {
					return
				}
			}
			_ = bw.Flush()
		}))
		defer srv.Close()
		rt := &countingTransport{}
		got := plugin.CheckRead(context.Background(), &plugintest.CheckEnv{Transport: rt}, "probe", plugin.HTTPConfig{}, srv.URL, nil)
		if !got.OK {
			t.Errorf("result = %+v, want a passing check", got)
		}
		if n := rt.read.Load(); n == 0 || n > 64<<10 {
			t.Errorf("the check read %d bytes of a 16 MiB body, want some and at most 64 KiB", n)
		}
	})
}

func TestCheckReadLeavesNoConnectionOpen(t *testing.T) {
	var mu sync.Mutex
	open := map[net.Conn]http.ConnState{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		if s == http.StateClosed || s == http.StateHijacked {
			delete(open, c)
		} else {
			open[c] = s
		}
	}
	srv.Start()
	defer srv.Close()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()

	env := &plugintest.CheckEnv{Transport: tr}
	for range 3 {
		if got := plugin.CheckRead(context.Background(), env, "probe", plugin.HTTPConfig{}, srv.URL, nil); !got.OK {
			t.Fatalf("result = %+v, want a passing check", got)
		}
	}
	eventually(t, "the server to see every check's connection closed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(open) == 0
	})
}
