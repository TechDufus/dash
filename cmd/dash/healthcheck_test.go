package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
)

// readyzStub is a health listener on 127.0.0.1:0 and the dial seam that sends every healthcheck
// connection to it, recording the address asked for and counting the bytes the client read. No
// test binds or dials the address the healthcheck computes.
type readyzStub struct {
	srv  *httptest.Server
	read atomic.Int64

	mu     sync.Mutex
	dialed []string
	paths  []string
}

// newStub serves h on every path, recording each request.
func newStub(t *testing.T, h http.HandlerFunc) *readyzStub {
	t.Helper()
	s := &readyzStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// newReadyzStub answers /readyz with status (a 3xx points elsewhere) and anything else with 404.
func newReadyzStub(t *testing.T, status int) *readyzStub {
	return newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		if status >= 300 && status < 400 {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(status)
	})
}

func (s *readyzStub) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	s.mu.Lock()
	s.dialed = append(s.dialed, addr)
	s.mu.Unlock()
	c, err := (&net.Dialer{}).DialContext(ctx, network, s.srv.Listener.Addr().String())
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: c, n: &s.read}, nil
}

func (s *readyzStub) only(t *testing.T) (dialed, path string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dialed) != 1 || len(s.paths) != 1 {
		t.Fatalf("dialed %v, requests %v; want one of each", s.dialed, s.paths)
	}
	return s.dialed[0], s.paths[0]
}

// countingConn counts the bytes read from the connection.
type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// failed checks err is a healthcheck failure: exit 1 and one line that says want.
func failed(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, errUnhealthy) || exitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d), want errUnhealthy and exit 1", err, exitCode(err))
	}
	if msg := err.Error(); strings.ContainsAny(msg, "\r\n") || !strings.Contains(msg, want) {
		t.Errorf("message %q: want one line containing %q", msg, want)
	}
}

func TestHealthcheckReadyIsExitZero(t *testing.T) {
	s := newReadyzStub(t, http.StatusOK)
	if err := healthcheck(config.Sources{}, s.dial, healthcheckTimeout); err != nil {
		t.Fatalf("healthcheck = %v, want nil (exit 0)", err)
	}
	dialed, path := s.only(t)
	if dialed != "127.0.0.1:8081" || path != "GET /readyz" {
		t.Errorf("dialed %s for %s, want 127.0.0.1:8081 for GET /readyz (the default 0.0.0.0:8081 on loopback)", dialed, path)
	}
}

func TestHealthcheckNotReadyIsExitOne(t *testing.T) {
	s := newReadyzStub(t, http.StatusServiceUnavailable)
	failed(t, healthcheck(config.Sources{}, s.dial, healthcheckTimeout), "GET http://127.0.0.1:8081/readyz: 503 Service Unavailable")
}

// Only 200 is healthy: another 2xx or a 3xx is not, and a redirect is not followed.
func TestHealthcheckAnyOtherStatusIsExitOne(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusFound, http.StatusNotModified} {
		s := newReadyzStub(t, status)
		failed(t, healthcheck(config.Sources{}, s.dial, healthcheckTimeout), "/readyz: "+strconv.Itoa(status)+" "+http.StatusText(status))
		if _, path := s.only(t); path != "GET /readyz" {
			t.Errorf("%d: requested %s, want GET /readyz only", status, path)
		}
	}
}

func TestHealthcheckNothingListeningIsExitOne(t *testing.T) {
	// A refused dial, simulated: a freed port can be taken by another package's listener while
	// tests run in parallel.
	refused := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}
	failed(t, healthcheck(config.Sources{}, refused, healthcheckTimeout), "connection refused")
}

// A /readyz that never answers fails at the timeout (2 s in the command, below the image's
// HEALTHCHECK --timeout; a short one here). The stub gives up after 5 s with a 200, so a
// client without a timeout reports healthy.
func TestHealthcheckHangingReadyzTimesOut(t *testing.T) {
	if healthcheckTimeout != 2*time.Second {
		t.Errorf("healthcheckTimeout = %s, want 2s", healthcheckTimeout)
	}
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	start := time.Now()
	err := healthcheck(config.Sources{}, s.dial, 100*time.Millisecond)
	failed(t, err, "Client.Timeout exceeded")
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %s, want about the 100ms timeout", took)
	}
}

// At most healthcheckBodyLimit bytes of the body are read: an endless /readyz cannot hold the
// probe until its timeout or fill memory.
func TestHealthcheckReadsABoundedBody(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		for range 128 { // 8 MiB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	if err := healthcheck(config.Sources{}, s.dial, healthcheckTimeout); err != nil {
		t.Fatal(err)
	}
	if n := s.read.Load(); n > 256<<10 {
		t.Errorf("read %d bytes of an 8 MiB body, want about %d", n, healthcheckBodyLimit)
	}
}

// HTTP_PROXY never routes the probe. Go reads the proxy variables once per process, so the
// check runs in a child process that starts with them set.
func TestHealthcheckIgnoresProxyVariables(t *testing.T) {
	if os.Getenv("DASH_TEST_HEALTHCHECK_PROXY_CHILD") == "1" {
		s := newReadyzStub(t, http.StatusOK)
		src := config.Sources{Environ: []string{"DASH_SERVER_HEALTH_LISTEN=192.0.2.7:9000"}}
		if err := healthcheck(src, s.dial, healthcheckTimeout); err != nil {
			t.Fatal(err)
		}
		if dialed, _ := s.only(t); dialed != "192.0.2.7:9000" {
			t.Fatalf("dialed %s, want 192.0.2.7:9000 (not the proxy)", dialed)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHealthcheckIgnoresProxyVariables$")
	cmd.Env = append(os.Environ(), "DASH_TEST_HEALTHCHECK_PROXY_CHILD=1",
		"HTTP_PROXY=http://192.0.2.99:3128", "http_proxy=http://192.0.2.99:3128", "NO_PROXY=", "no_proxy=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func TestHealthcheckMapsAnUnspecifiedHostToLoopback(t *testing.T) {
	for _, tc := range []struct {
		environ []string
		want    string
	}{
		{[]string{"DASH_SERVER_HEALTH_LISTEN=0.0.0.0:8081"}, "127.0.0.1:8081"},
		{[]string{"DASH_SERVER_HEALTH_LISTEN=:8081"}, "127.0.0.1:8081"},
		{[]string{"DASH_SERVER_HEALTH_LISTEN=[::]:8081"}, "[::1]:8081"},
		{[]string{"DASH_SERVER_HEALTH_LISTEN=192.0.2.7:9000"}, "192.0.2.7:9000"},
		{[]string{"DASH_SERVER_HEALTH_LISTEN=[2001:db8::7]:9000"}, "[2001:db8::7]:9000"},
		{[]string{"DASH_DEMO=true"}, "127.0.0.1:8081"},
	} {
		s := newReadyzStub(t, http.StatusOK)
		if err := healthcheck(config.Sources{Environ: tc.environ}, s.dial, healthcheckTimeout); err != nil {
			t.Errorf("%v: %v", tc.environ, err)
			continue
		}
		if dialed, _ := s.only(t); dialed != tc.want {
			t.Errorf("%v: dialed %s, want %s", tc.environ, dialed, tc.want)
		}
	}
}

func TestHealthcheckHonoursTheConfiguredHealthPort(t *testing.T) {
	file := &config.File{Path: "config.yaml", Data: []byte("server:\n  healthListen: 0.0.0.0:9191\n")}
	s := newReadyzStub(t, http.StatusOK)
	if err := healthcheck(config.Sources{File: file}, s.dial, healthcheckTimeout); err != nil {
		t.Fatal(err)
	}
	if dialed, _ := s.only(t); dialed != "127.0.0.1:9191" {
		t.Errorf("file: dialed %s, want 127.0.0.1:9191", dialed)
	}

	s = newReadyzStub(t, http.StatusOK)
	src := config.Sources{Environ: []string{"DASH_SERVER_HEALTH_LISTEN=127.0.0.1:9292"}, File: file}
	if err := healthcheck(src, s.dial, healthcheckTimeout); err != nil {
		t.Fatal(err)
	}
	if dialed, _ := s.only(t); dialed != "127.0.0.1:9292" {
		t.Errorf("env over file: dialed %s, want 127.0.0.1:9292", dialed)
	}
}

// The healthcheck reads no secret, key or data directory: references to files that do not exist
// are left alone, and a refused configuration fails in one line that quotes no value, even when
// the layering error has several.
func TestHealthcheckNeedsNoSecretsAndPrintsNone(t *testing.T) {
	missing := t.TempDir() + "/missing"
	s := newReadyzStub(t, http.StatusOK)
	src := config.Sources{Environ: []string{
		"DASH_DATA_DIR=" + missing,
		"DASH_SECRET_KEY_FILE=" + missing,
		"DASH_ADMIN_PASSWORD_HASH_FILE=" + missing,
		"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=file:" + missing,
		"DASH_PLUGINS_KUBERNETES_MODE=inCluster",
	}}
	if err := healthcheck(src, s.dial, healthcheckTimeout); err != nil {
		t.Fatalf("healthcheck = %v, want nil", err)
	}

	src = config.Sources{Environ: []string{
		"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=hunter2-not-a-real-secret",
		"DASH_PLUGINS_PROXMOX_URLL=https://192.0.2.1:8006",
		"DASH_PLUGINS_GATUS_URLL=https://192.0.2.2:8080",
	}}
	if _, err := config.HealthListenOf(src); err == nil || !strings.Contains(err.Error(), "\n") {
		t.Fatalf("precondition: want a layering error of several lines, got %v", err)
	}
	err := healthcheck(src, s.dial, healthcheckTimeout)
	failed(t, err, "DASH_PLUGINS_PROXMOX_URLL")
	if !strings.Contains(err.Error(), "DASH_PLUGINS_GATUS_URLL") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("want both problems on the line and no secret: %v", err)
	}
}

// The command reads the process environment: a DASH_CONFIG that does not exist is one line and
// exit 1, as the server refuses to start on it.
func TestHealthcheckCommandWithAMissingConfigFile(t *testing.T) {
	t.Setenv("DASH_CONFIG", t.TempDir()+"/nope.yaml")
	failed(t, command([]string{"healthcheck"}, &bytes.Buffer{}), "DASH_CONFIG")
}

func TestHealthcheckCommandTakesNoArguments(t *testing.T) {
	err := command([]string{"healthcheck", "extra"}, &bytes.Buffer{})
	failed(t, err, "usage: "+healthcheckUsage)
}
