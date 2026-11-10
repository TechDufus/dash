package proxmox

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

// caFileOf writes the certificate of an httptest TLS server as a PEM file.
func caFileOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A self-signed Proxmox works with tls.caFile and fails, with the category tls, without it.
func TestProxmoxSelfSignedNeedsCAFile(t *testing.T) {
	t.Run("with the CA file", func(t *testing.T) {
		pve := newFakePVE(t, true)
		e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.TLS.CAFile = caFileOf(t, pve.Server) }})
		e.state(t, plugin.StateHealthy)
		if len(e.current().Status().Sources) != 1 {
			t.Errorf("sources = %+v: a verified connection has no tls note", e.current().Status().Sources)
		}
	})
	t.Run("without it", func(t *testing.T) {
		pve := newFakePVE(t, true)
		e := newEnv(t, envOpts{pve: pve})
		pi := e.state(t, plugin.StateError)
		if !strings.Contains(pi.Message, "vms: tls") || strings.Contains(pi.Message, "127.0.0.1") {
			t.Errorf("message = %q, want the category tls and no address", pi.Message)
		}
		if srcs := e.current().Status().Sources; len(srcs) != 1 || srcs[0].Error != "tls" {
			t.Errorf("sources = %+v", srcs)
		}
	})
	t.Run("a CA file that holds no certificate fails the start", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		pve := newFakePVE(t, true)
		e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.TLS.CAFile = bad }})
		pi := e.state(t, plugin.StateError)
		if pve.total() != 0 {
			t.Errorf("%d requests with a CA file that cannot be read", pve.total())
		}
		_ = pi
	})
}

// insecureSkipVerify makes the connection work, logs the startup WARN of Host.HTTPClient and adds
// the "tls: verification disabled" source to the status (and so to the Overview's sources).
func TestProxmoxInsecureSkipVerifyWarnsAndNotes(t *testing.T) {
	pve := newFakePVE(t, true)
	e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.TLS.InsecureSkipVerify = true }})
	e.state(t, plugin.StateHealthy)
	if !strings.Contains(e.logs.String(), "certificate verification is disabled") {
		t.Errorf("no startup WARN in the log:\n%s", e.logs.String())
	}
	srcs := e.current().Status().Sources
	if len(srcs) != 2 || srcs[0].Name != "proxmox" || srcs[1] != (model.SourceStatus{Name: "tls: verification disabled", OK: true}) {
		t.Errorf("sources = %+v", srcs)
	}
	var overview []model.SourceStatus
	for _, c := range e.sup.Contributors() {
		if c.ID == ID {
			overview = c.Sources()
		}
	}
	if len(overview) != 2 || overview[1].Name != "tls: verification disabled" {
		t.Errorf("the contributor's sources = %+v", overview)
	}
	// The note draws no chip: chips are for sources that are not ok.
	if !overview[1].OK {
		t.Error("the note is a failing source")
	}
}

// A Proxmox that redirects is not followed to another host, nor from https to http, and the token
// never reaches the second request.
func TestProxmoxRefusesCrossHostAndHTTPSToHTTPRedirects(t *testing.T) {
	t.Run("to another host", func(t *testing.T) {
		other := newFakePVE(t, false)
		pve := newFakePVE(t, false)
		pve.redirectTo(other.URL)
		e := newEnv(t, envOpts{pve: pve})
		e.state(t, plugin.StateError)
		if other.total() != 0 {
			t.Errorf("the redirect was followed: %d requests, Authorization %v", other.total(), other.authHeaders())
		}
		for _, a := range other.authHeaders() {
			if strings.Contains(a, testToken) {
				t.Errorf("the token reached the second host: %q", a)
			}
		}
	})
	t.Run("from https to http", func(t *testing.T) {
		plain := newFakePVE(t, false)
		pve := newFakePVE(t, true)
		pve.redirectTo(plain.URL)
		e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.TLS.CAFile = caFileOf(t, pve.Server) }})
		e.state(t, plugin.StateError)
		if plain.total() != 0 {
			t.Errorf("the redirect to plain http was followed: %d requests", plain.total())
		}
	})
}

// The token and the URL stay out of everything a viewer or a log reader sees, even when the
// upstream echoes the Authorization header and its own host in an error body.
func TestProxmoxDoesNotLeakTheTokenOrTheURL(t *testing.T) {
	reg := secret.NewRegistry()
	tok := tokenValue()
	reg.Add(tok)
	pve := newFakePVE(t, false)
	pve.echoErrors()
	pve.fail("/cluster/status", http.StatusInternalServerError)
	pve.fail("/cluster/resources?type=storage", http.StatusInternalServerError)
	pve.fail("/nodes/pve-1/qemu/101/config", http.StatusInternalServerError)
	e := newEnv(t, envOpts{pve: pve, scrub: reg.ScrubLogLine, cfg: func(c *pluginconf.Proxmox) { c.TokenSecret = tok }})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	e.state(t, plugin.StateDegraded)

	host := strings.TrimPrefix(pve.URL, "http://")
	check := func(what, text string) {
		t.Helper()
		if strings.Contains(text, testToken) {
			t.Errorf("%s holds the token: %s", what, text)
		}
		if what != "the log" && strings.Contains(text, host) {
			t.Errorf("%s holds the upstream's address: %s", what, text)
		}
	}
	// What a viewer sees: the row, the status sources, every envelope, and the detail answer.
	pi := e.info(t)
	check("the row", pi.Message)
	for _, s := range e.current().Status().Sources {
		check("a source", s.Name+" "+s.Error)
	}
	for _, topic := range []string{"proxmox.vms", "proxmox.cluster", "proxmox.storage"} {
		waitFor(t, topic, func() bool { _, ok := e.hub.Latest("", model.Topic(topic)); return ok })
		ev, _ := e.hub.Latest("", model.Topic(topic))
		// The VM links hold the configured URL on purpose (they are links into Proxmox); what must
		// not appear is the token, and anything an error carried: the sources are checked whole.
		var env model.Envelope
		if err := json.Unmarshal(ev.Data, &env); err != nil {
			t.Fatal(err)
		}
		srcs, _ := json.Marshal(env.Sources)
		check(topic+" sources", string(srcs))
		if strings.Contains(string(ev.Data), testToken) {
			t.Errorf("%s holds the token", topic)
		}
	}
	w := e.get(t, "/vms/101")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("detail status %d", w.Code)
	}
	check("the detail answer", w.Body.String())
	// What a log reader sees: the failure is logged, and without the token.
	waitFor(t, "the failure in the log", func() bool { return strings.Contains(e.logs.String(), "upstream source failed") })
	check("the log", e.logs.String())
	if !strings.Contains(e.logs.String(), "[redacted]") {
		t.Error("the echoed Authorization header was not scrubbed from the log (it should read [redacted])")
	}
}
