package plugin

import (
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardAddr(t *testing.T) {
	for _, c := range []struct {
		ip     string
		fromUI bool
		refuse bool
	}{
		{"169.254.169.254", false, true},
		{"169.254.0.1", false, true},
		{"fe80::1", false, true},
		{"::ffff:169.254.169.254", false, true}, // an IPv4-mapped form is the same address
		{"fd00:ec2::254", false, true},
		{"127.0.0.1", false, false},
		{"::1", false, false},
		{"127.0.0.1", true, true},
		{"::1", true, true},
		{"::ffff:127.0.0.1", true, true},
		{"0.0.0.0", true, true},
		{"::", true, true},
		{"10.0.0.5", true, false},
		{"192.0.2.10", false, false},
		{"2001:db8::1", true, false},
		{"fd00:ec2::255", false, false},
	} {
		err := guardAddr(netip.MustParseAddr(c.ip), c.fromUI)
		if (err != nil) != c.refuse {
			t.Errorf("guardAddr(%s, fromUI=%v) = %v, refuse=%v", c.ip, c.fromUI, err, c.refuse)
		}
	}
}

func body(t *testing.T, c *http.Client, url string) (string, error) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func TestHTTPClientGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()

	t.Run("loopback is allowed, and refused for a URL from the UI", func(t *testing.T) {
		c, err := newHTTPClient(HTTPConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := body(t, c, srv.URL); err != nil || got != "ok" {
			t.Fatalf("GET = %q, %v", got, err)
		}
		ui, _ := newHTTPClient(HTTPConfig{FromUI: true})
		if _, err := body(t, ui, srv.URL); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("FromUI loopback GET err = %v", err)
		}
	})

	t.Run("link-local and metadata are refused at dial time", func(t *testing.T) {
		c, _ := newHTTPClient(HTTPConfig{})
		for _, u := range []string{"http://169.254.169.254/latest/meta-data", "http://[fe80::1]:8080/"} {
			if _, err := body(t, c, u); err == nil || !strings.Contains(err.Error(), "refused") {
				t.Errorf("GET %s err = %v, want a refusal", u, err)
			}
		}
	})

	t.Run("a host name that resolves to a guarded address is refused", func(t *testing.T) {
		c, _ := newHTTPClient(HTTPConfig{FromUI: true})
		// localhost resolves to loopback: the guard sees the address, not the name.
		if _, err := body(t, c, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)); err == nil {
			t.Fatal("localhost was reached from a UI URL")
		}
	})

	t.Run("redirects to another host are refused", func(t *testing.T) {
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "other") }))
		defer other.Close()
		mux := http.NewServeMux()
		mux.HandleFunc("/to-metadata", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusFound)
		})
		mux.HandleFunc("/to-other", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
		mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/landing", http.StatusFound) })
		mux.HandleFunc("/landing", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "landed") })
		mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
		redir := httptest.NewServer(mux)
		defer redir.Close()
		c, _ := newHTTPClient(HTTPConfig{})
		for _, p := range []string{"/to-metadata", "/to-other", "/loop"} {
			if _, err := body(t, c, redir.URL+p); err == nil {
				t.Errorf("GET %s followed the redirect", p)
			}
		}
		if _, err := body(t, c, redir.URL+"/to-other"); err == nil || !strings.Contains(err.Error(), "another host") {
			t.Errorf("err = %v", err)
		}
		if got, err := body(t, c, redir.URL+"/same"); err != nil || got != "landed" {
			t.Errorf("a redirect on the same host: %q, %v", got, err)
		}
	})

	t.Run("Authorize runs on every request, redirects included, without touching the caller's request", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Header.Get("Authorization")) })
		mux.HandleFunc("/bounce", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/who", http.StatusFound) })
		s := httptest.NewServer(mux)
		defer s.Close()
		token := "one"
		c, _ := newHTTPClient(HTTPConfig{Authorize: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }})
		if got, _ := body(t, c, s.URL+"/who"); got != "Bearer one" {
			t.Fatalf("got %q", got)
		}
		token = "two" // a rotated secret applies at the next request
		if got, _ := body(t, c, s.URL+"/bounce"); got != "Bearer two" {
			t.Fatalf("got %q", got)
		}
		req, _ := http.NewRequest(http.MethodGet, s.URL+"/who", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if req.Header.Get("Authorization") != "" {
			t.Fatal("Authorize modified the caller's request")
		}
	})
}

func TestHTTPClientCAFileAndInsecureSkipVerify(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "secure") }))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the handshake of the client that does not trust it
	srv.StartTLS()
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	plain, _ := newHTTPClient(HTTPConfig{})
	if _, err := body(t, plain, srv.URL); err == nil {
		t.Fatal("an unknown CA was trusted")
	}
	withCA, err := newHTTPClient(HTTPConfig{CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := body(t, withCA, srv.URL); err != nil || got != "secure" {
		t.Fatalf("with the CA file: %q, %v", got, err)
	}
	insecure, _ := newHTTPClient(HTTPConfig{InsecureSkipVerify: true})
	if got, err := body(t, insecure, srv.URL); err != nil || got != "secure" {
		t.Fatalf("InsecureSkipVerify: %q, %v", got, err)
	}

	if _, err := newHTTPClient(HTTPConfig{CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Fatal("a missing CA file was accepted")
	}
	junk := filepath.Join(t.TempDir(), "junk.pem")
	_ = os.WriteFile(junk, []byte("not a certificate"), 0o600)
	if _, err := newHTTPClient(HTTPConfig{CAFile: junk}); err == nil {
		t.Fatal("a CA file without certificates was accepted")
	}
}

func TestHTTPClientTimeout(t *testing.T) {
	c, _ := newHTTPClient(HTTPConfig{})
	if c.Timeout != DefaultHTTPTimeout {
		t.Fatalf("default timeout = %v", c.Timeout)
	}
	c, _ = newHTTPClient(HTTPConfig{Timeout: 3 * 1e9})
	if c.Timeout != 3*1e9 {
		t.Fatalf("timeout = %v", c.Timeout)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A redirect from https to plain http on the same host is refused, so the credentials that
// Authorize adds to every hop never cross the network in clear text.
func TestHTTPClientRefusesAnHTTPSToHTTPRedirect(t *testing.T) {
	var plainAuth []string // Authorization of every request that reached the http hop
	upstream := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme == "http" {
			plainAuth = append(plainAuth, r.Header.Get("Authorization"))
		}
		h := http.Header{}
		if r.URL.Scheme == "https" && r.URL.Path == "/start" {
			h.Set("Location", "http://prom.example.com/downgraded")
			return &http.Response{StatusCode: http.StatusFound, Header: h, Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: http.NoBody, Request: r}, nil
	})
	c, err := newHTTPClient(HTTPConfig{Authorize: func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }})
	if err != nil {
		t.Fatal(err)
	}
	c.Transport = authorizing{rt: upstream, authorize: func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }}

	if _, err := c.Get("https://prom.example.com/start"); err == nil || !strings.Contains(err.Error(), "https to http") {
		t.Fatalf("the downgrade was followed: err = %v", err)
	}
	if len(plainAuth) != 0 {
		t.Fatalf("a request reached http: Authorization %q", plainAuth)
	}

	// A redirect that stays on https, and one on plain http from the start, still work.
	secure := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		if r.URL.Path == "/start" {
			h.Set("Location", "https://prom.example.com/next")
			return &http.Response{StatusCode: http.StatusFound, Header: h, Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: http.NoBody, Request: r}, nil
	})
	c.Transport = secure
	for _, u := range []string{"https://prom.example.com/start", "http://prom.example.com/start"} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		_ = resp.Body.Close()
	}
}
