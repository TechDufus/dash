package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientOptions(t *testing.T) {
	t.Run("defaults stay", func(t *testing.T) {
		c := newClient("http://x.example/", nil, 5*time.Second)
		if c.hc.Timeout != 5*time.Second || c.hc.CheckRedirect != nil || c.base != "http://x.example" {
			t.Errorf("client = %+v", c)
		}
	})
	t.Run("Timeout replaces the default", func(t *testing.T) {
		c := newClient("http://x.example", nil, 5*time.Second, Timeout(30*time.Second))
		if c.hc.Timeout != 30*time.Second {
			t.Errorf("timeout = %v", c.hc.Timeout)
		}
	})
	t.Run("CheckRedirect is the client's policy", func(t *testing.T) {
		errStop := errors.New("stop")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}))
		defer srv.Close()
		c := newClient(srv.URL, nil, time.Second, CheckRedirect(func(*http.Request, []*http.Request) error { return errStop }))
		_, err := c.open(context.Background(), http.MethodGet, "/", nil, nil, nil)
		if !errors.Is(err, errStop) {
			t.Errorf("err = %v, want the policy's error", err)
		}
	})
	t.Run("a stream client ignores Timeout", func(t *testing.T) {
		c := newStreamClient("http://x.example", nil, Timeout(time.Second))
		if c.hc.Timeout != 0 {
			t.Errorf("stream timeout = %v, want none", c.hc.Timeout)
		}
	})
	t.Run("the timeout cuts a slow answer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}))
		defer srv.Close()
		c := newClient(srv.URL, nil, time.Minute, Timeout(50*time.Millisecond))
		if _, err := c.open(context.Background(), http.MethodGet, "/", nil, nil, nil); err == nil {
			t.Error("a request slower than the Timeout option succeeded")
		}
	})
}
