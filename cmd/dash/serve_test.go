package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
)

// anyPort lets the kernel choose a free loopback port; serve reports the one it bound.
const anyPort = "127.0.0.1:0"

func servers(t *testing.T, apiAddr, healthAddr string, h http.Handler) (*http.Server, *http.Server) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Listen, cfg.HealthListen = apiAddr, healthAddr
	return newServers(cfg, http.NotFoundHandler(), h, context.Background(), func() {})
}

func TestServeMarksServingOnlyOnceBothListenersAreBound(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	for _, tc := range []struct {
		name        string
		api, health string
		opened      int // listeners serve binds before the failing one
	}{
		{"api address in use", taken.Addr().String(), anyPort, 0},
		{"health address in use", anyPort, taken.Addr().String(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep every listener serve opens: one that bound must be released when the other
			// fails.
			var opened []net.Listener
			defer func(orig func(string, string) (net.Listener, error)) { listen = orig }(listen)
			listen = func(network, addr string) (net.Listener, error) {
				ln, err := net.Listen(network, addr)
				if err == nil {
					opened = append(opened, ln)
				}
				return ln, err
			}
			apiSrv, healthSrv := servers(t, tc.api, tc.health, http.NotFoundHandler())
			var marked, cancelled, bound atomic.Bool
			err := serve(context.Background(), apiSrv, healthSrv, func() { marked.Store(true) }, func() { cancelled.Store(true) },
				func(net.Addr, net.Addr) { bound.Store(true) })
			if err == nil {
				t.Fatal("serve returned nil with an address in use")
			}
			if marked.Load() || bound.Load() {
				t.Errorf("marked serving %v, reported bound %v, although a listener failed to bind", marked.Load(), bound.Load())
			}
			if !cancelled.Load() {
				t.Error("open streams were not cancelled")
			}
			if len(opened) != tc.opened {
				t.Fatalf("serve opened %d listeners, want %d", len(opened), tc.opened)
			}
			for _, ln := range opened {
				if err := ln.Close(); !errors.Is(err, net.ErrClosed) {
					t.Errorf("the listener on %s was left open (Close = %v)", ln.Addr(), err)
				}
			}
		})
	}
}

func TestServeServesAfterMarkingAndStopsWithTheContext(t *testing.T) {
	apiAddr, healthAddr := anyPort, anyPort
	var serving atomic.Bool
	health := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if serving.Load() {
			_, _ = io.WriteString(w, "ok")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	apiSrv, healthSrv := servers(t, apiAddr, healthAddr, health)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, apiSrv, healthSrv, func() { serving.Store(true); close(marked) }, func() {},
			func(api, health net.Addr) { apiAddr, healthAddr = api.String(), health.String() })
	}()

	select {
	case <-marked:
	case err := <-done:
		t.Fatalf("serve returned %v before marking", err)
	case <-time.After(5 * time.Second):
		t.Fatal("never marked serving")
	}
	// Bound before marked: a connection succeeds at once, and the handler answers.
	resp, err := http.Get("http://" + healthAddr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Errorf("health listener: %d %q", resp.StatusCode, body)
	}
	resp, err = http.Get("http://" + apiAddr + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("api listener: %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve after a clean shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the context ended")
	}
}

func TestWarnSettledRequirements(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	var tree config.Tree
	tree.Plugins.Kubernetes.Enabled = new(false)
	tree.Plugins.Flux.Enabled = new(true)
	warnSettledRequirements(tree, []string{"kubernetes", "flux", "gatus"})
	out := buf.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "plugin=kubernetes") {
		t.Errorf("want one WARN naming only the disabled kubernetes plugin:\n%s", out)
	}
}
