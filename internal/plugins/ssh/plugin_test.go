package ssh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// A host without a pinned key is asked for its key only: the plugin publishes the offered key,
// the host never sees an authentication request, and no script is sent.
func TestUnverifiedHostGetsNoCredential(t *testing.T) {
	f := newFakeHost(t)
	r := newRig(t)
	r.start(pluginconf.SSHHost{Name: "alpha", Address: f.addr(), User: "alice"})
	h := r.await(0, sshmodel.StateUnverified)
	if h.OfferedKey == nil || h.OfferedKey.Key != f.hostKey() || h.OfferedKey.Type != "ssh-ed25519" || !strings.HasPrefix(h.OfferedKey.Fingerprint, "SHA256:") {
		t.Fatalf("offered key = %+v, want the host's", h.OfferedKey)
	}
	if h.Health != string(model.HealthUnknown) || h.Info != nil || h.Stats != nil {
		t.Errorf("unverified host = %+v", h)
	}
	time.Sleep(300 * time.Millisecond) // a few rescans
	if n := f.authCount(); n != 0 {
		t.Errorf("the host saw %d authentication requests", n)
	}
	if s := f.received(); len(s) != 0 {
		t.Errorf("the host received a script: %d", len(s))
	}
	if items := r.p.Attention(time.Now()); len(items) != 0 {
		t.Errorf("an unverified host raised %+v", items)
	}
	// Waiting for an administrator is not a failure of the plugin.
	if st := r.p.Status(); st.State != plugin.StateDegraded || !strings.Contains(st.Message, "alpha: unverified") {
		t.Errorf("status = %+v, want degraded naming the host", st)
	}
}

// Trusting the key and authorizing dash's key bring the host up: inventory, rates from the second
// frame, a history, sources, and a script that holds no configured string.
func TestTrustedAuthorizedHostIsUp(t *testing.T) {
	f := newFakeHost(t)
	r := newRig(t)
	f.authorize(r.publicKey())
	const name, user = "alpha-canary-name", "canaryuser"
	r.start(pluginconf.SSHHost{Name: name, Address: f.addr(), User: user, HostKey: f.hostKey()})
	r.await(0, sshmodel.StateUp)
	eventually(t, "a sample with rates", func() bool {
		h := r.host(0)
		return h.Stats != nil && h.Stats.NetRxBytesPerSec > 0
	})
	h := r.host(0)
	// The fixture's /srv/media files is 94 % full: the host is up and warns about it.
	if h.Info == nil || h.Info.Hostname != "alpha" || h.Info.Containers == nil || h.Health != string(model.HealthWarn) || h.OfferedKey != nil {
		t.Fatalf("host = %+v", h)
	}
	if items := r.p.Attention(time.Now()); len(items) != 1 || items[0].ID != "ssh:fs:"+name+"//srv/media files" || items[0].Severity != model.SeverityWarn {
		t.Errorf("attention = %+v", items)
	}
	if h.Ref != "ssh:Host/"+name || h.Message == "" {
		t.Errorf("ref %q message %q", h.Ref, h.Message)
	}
	eventually(t, "the topic", func() bool {
		d := r.h.published()
		return d != nil && d.Hosts[0].State == sshmodel.StateUp && d.Identity != nil
	})
	d := r.h.published()
	if !strings.HasPrefix(d.Identity.PublicKey, "ssh-ed25519 ") || !strings.HasSuffix(d.Identity.PublicKey, " dash") || d.IntervalSeconds != 2 {
		t.Errorf("identity %+v interval %d", d.Identity, d.IntervalSeconds)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), f.addr()) || strings.Contains(string(b), user) {
		t.Error("the topic carries the address or the user")
	}
	if st := r.p.Status(); st.State != plugin.StateHealthy || len(st.Sources) != 1 || st.Sources[0].Name != name || !st.Sources[0].OK {
		t.Errorf("status = %+v", st)
	}
	if hist, ok := historyOf(t, r.p, name); !ok || len(hist.Samples) == 0 || hist.IntervalSeconds != 2 {
		t.Errorf("history = %+v, %v", hist, ok)
	}
	scripts := f.received()
	if len(scripts) == 0 {
		t.Fatal("no script arrived")
	}
	for _, s := range scripts {
		for _, v := range []string{name, user, f.addr(), "127.0.0.1"} {
			if strings.Contains(s, v) {
				t.Errorf("the script holds the configured %q", v)
			}
		}
	}
	f.mu.Lock()
	cmds := append([]string{}, f.commands...)
	f.mu.Unlock()
	for _, c := range cmds {
		if c != remoteCommand {
			t.Errorf("command %q, want %q", c, remoteCommand)
		}
	}
	// The object store has the host, and its context has the inventory and the sample.
	hits := r.p.Search("canary", nil, 5)
	if len(hits) != 1 || hits[0].Kind != "Host" {
		t.Fatalf("search = %+v", hits)
	}
	part, err := r.p.Context(context.Background(), hits[0].Ref, model.ContextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	facts := map[string]string{}
	for _, f := range part.Facts {
		facts[f.Name] = f.Value
	}
	if facts["info.os"] != "Debian GNU/Linux 13 (trixie)" || facts["stats.cpuRatio"] == "" || facts["state"] != "up" {
		t.Errorf("facts = %v", facts)
	}
}

// A host that presents another key than the pinned one is key_changed, offers its key, and is
// sent no credential.
func TestChangedHostKeyIsRefused(t *testing.T) {
	f := newFakeHost(t)
	other := newFakeHost(t)
	r := newRig(t)
	f.authorize(r.publicKey())
	r.start(pluginconf.SSHHost{Name: "alpha", Address: f.addr(), User: "alice", HostKey: other.hostKey()})
	h := r.await(0, sshmodel.StateKeyChanged)
	if h.OfferedKey == nil || h.OfferedKey.Key != f.hostKey() || h.Health != string(model.HealthCrit) {
		t.Fatalf("host = %+v", h)
	}
	time.Sleep(300 * time.Millisecond)
	if n := f.authCount(); n != 0 {
		t.Errorf("the host saw %d authentication requests", n)
	}
	items := r.p.Attention(time.Now())
	if len(items) != 1 || items[0].ID != "ssh:host:alpha" || items[0].Severity != model.SeverityCrit || items[0].Route != "/hosts/alpha" {
		t.Errorf("attention = %+v", items)
	}
	if st := r.p.Status(); st.State != plugin.StateError || !strings.Contains(st.Message, "alpha: key_changed") {
		t.Errorf("status = %+v", st)
	}
}

// A host that refuses the key is unauthorized, and up soon after the key is authorized.
func TestUnauthorizedThenAuthorized(t *testing.T) {
	f := newFakeHost(t)
	r := newRig(t)
	r.start(pluginconf.SSHHost{Name: "alpha", Address: f.addr(), User: "alice", HostKey: f.hostKey()})
	h := r.await(0, sshmodel.StateUnauthorized)
	if h.Health != string(model.HealthWarn) {
		t.Errorf("unauthorized health = %s", h.Health)
	}
	if items := r.p.Attention(time.Now()); len(items) != 1 || items[0].Severity != model.SeverityWarn {
		t.Errorf("attention = %+v", items)
	}
	f.authorize(r.publicKey())
	r.await(0, sshmodel.StateUp)
}

// A host whose address the dial guard refuses is unreachable, and its message says that the
// address is the reason, in words that name the rule and not the address.
func TestRefusedAddressSaysSo(t *testing.T) {
	r := newRig(t)
	r.refuse = true
	r.start(pluginconf.SSHHost{Name: "alpha", Address: "192.0.2.7:2222", User: "alice"})
	h := r.await(0, sshmodel.StateUnreachable)
	if h.Message != blockedMessage || strings.Contains(h.Message, "192.0.2.7") {
		t.Errorf("message = %q, want the dial guard's", h.Message)
	}
	if got := stateMessages[sshmodel.StateUnreachable]; got == blockedMessage {
		t.Error("the refused-address message must differ from the unreachable one")
	}
}

// A host that goes silent is dropped after the frame deadline and read again on a new connection.
func TestSilentHostReconnects(t *testing.T) {
	f := newFakeHost(t)
	r := newRig(t)
	f.authorize(r.publicKey())
	f.setBehaviour(func(n int) behaviour {
		if n == 0 {
			return stayQuiet
		}
		return sendFrames
	})
	r.start(pluginconf.SSHHost{Name: "alpha", Address: f.addr(), User: "alice", HostKey: f.hostKey()})
	r.await(0, sshmodel.StateUnreachable)
	eventually(t, "the silent session to be closed", func() bool { return f.openSessions() <= 1 })
	r.await(0, sshmodel.StateUp)
}

// A frame larger than the cap breaks the protocol: the connection is dropped and tried again.
func TestOversizedFrameDropsTheConnection(t *testing.T) {
	f := newFakeHost(t)
	r := newRig(t)
	f.authorize(r.publicKey())
	f.setBehaviour(func(n int) behaviour {
		if n == 0 {
			return sendHuge
		}
		return sendFrames
	})
	r.start(pluginconf.SSHHost{Name: "alpha", Address: f.addr(), User: "alice", HostKey: f.hostKey()})
	r.await(0, sshmodel.StateUnreachable)
	r.await(0, sshmodel.StateUp)
	f.mu.Lock()
	sessions := f.sessions
	f.mu.Unlock()
	if sessions < 2 {
		t.Errorf("%d sessions, want a new one after the violation", sessions)
	}
}

// A host that is not Linux, or that cannot run the script at all, is unsupported.
func TestNonLinuxIsUnsupported(t *testing.T) {
	for name, b := range map[string]behaviour{"freebsd": sendFreeBSD, "no posix shell": exitAtOnce} {
		t.Run(name, func(t *testing.T) {
			f := newFakeHost(t)
			r := newRig(t)
			f.authorize(r.publicKey())
			f.setBehaviour(func(int) behaviour { return b })
			r.start(pluginconf.SSHHost{Name: "alpha", Address: f.addr(), User: "alice", HostKey: f.hostKey()})
			h := r.await(0, sshmodel.StateUnsupported)
			if h.Stats != nil || h.Health != string(model.HealthUnknown) {
				t.Errorf("host = %+v", h)
			}
		})
	}
}

// Stop closes the session and the connection, within its context.
func TestStopClosesTheSession(t *testing.T) {
	f := newFakeHost(t)
	r := newRig(t)
	f.authorize(r.publicKey())
	r.start(pluginconf.SSHHost{Name: "alpha", Address: f.addr(), User: "alice", HostKey: f.hostKey()})
	r.await(0, sshmodel.StateUp)
	if f.openSessions() != 1 {
		t.Fatalf("%d open sessions", f.openSessions())
	}
	start := time.Now()
	r.stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Stop took %v", d)
	}
	eventually(t, "the session to close", func() bool { return f.openSessions() == 0 })
}

// The history belongs to the factory: a restart of the instance (a settings change) keeps it, a
// host removed from the settings loses it, and an unknown host is a 404.
func TestHistoryIsKeptAcrossARestart(t *testing.T) {
	f := newFakeHost(t)
	r := newRig(t)
	f.authorize(r.publicKey())
	host := pluginconf.SSHHost{Name: "alpha", Address: f.addr(), User: "alice", HostKey: f.hostKey()}
	r.start(host)
	eventually(t, "three samples", func() bool { h, _ := historyOf(t, r.p, "alpha"); return len(h.Samples) >= 3 })
	r.stop()
	before, _ := historyOf(t, r.p, "alpha")

	r.start(host, pluginconf.SSHHost{Name: "beta", Address: f.addr(), User: "bob", HostKey: f.hostKey()})
	after, ok := historyOf(t, r.p, "ALPHA") // names compare ignoring case
	if !ok || len(after.Samples) < len(before.Samples) || after.Samples[0].T != before.Samples[0].T {
		t.Fatalf("after a restart: %d samples (%v), before %d", len(after.Samples), ok, len(before.Samples))
	}
	if _, ok := historyOf(t, r.p, "gamma"); ok {
		t.Error("an unknown host has a history")
	}
	r.stop()
	r.start(pluginconf.SSHHost{Name: "beta", Address: f.addr(), User: "bob", HostKey: f.hostKey()})
	if h := r.sh.hist.samples("alpha", time.Now()); len(h) != 0 {
		t.Errorf("a removed host kept %d samples", len(h))
	}
}

// historyOf asks the plugin's history route for host name.
func historyOf(t *testing.T, p plugin.Router, name string) (sshmodel.History, bool) {
	t.Helper()
	routes := plugin.CollectRoutes(p)
	if len(routes) != 1 || routes[0].Pattern != "/hosts/{name}/history" {
		t.Fatalf("routes = %+v", routes)
	}
	req := httptest.NewRequest(http.MethodGet, "/hosts/"+name+"/history", nil)
	req.SetPathValue("name", name)
	w := httptest.NewRecorder()
	routes[0].Handler(w, req)
	if w.Code == http.StatusNotFound {
		return sshmodel.History{}, false
	}
	var h sshmodel.History
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &h) != nil {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}
	return h, true
}

// Without a place to keep the key, Start fails with the fixed message that names what to set;
// another failure of the key's source is reported as such.
func TestStartNeedsAnIdentity(t *testing.T) {
	cfg := &pluginconf.SSH{Hosts: []pluginconf.SSHHost{{Name: "a", Address: "192.0.2.1", User: "alice"}}, IntervalSeconds: 5}
	for name, ident := range map[string]Identities{
		"none":          nil,
		"no secret key": fixedIdentity{err: errors.Join(ErrNoIdentity, errors.New("credentials: the SSH identity needs DASH_SECRET_KEY"))},
	} {
		p := Factory(ident)().(*Plugin)
		err := p.Start(context.Background(), &stubHost{}, cfg)
		if err == nil || err.Error() != msgNoIdentity || !strings.Contains(err.Error(), "DASH_SECRET_KEY") || !strings.Contains(err.Error(), "DASH_DATA_DIR") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	p := Factory(fixedIdentity{err: errors.New("disk full")})().(*Plugin)
	if err := p.Start(context.Background(), &stubHost{}, cfg); err == nil || err.Error() == msgNoIdentity {
		t.Errorf("another failure: %v", err)
	}
}

// The manifest, the settings hooks and the ref of the plugin.
func TestManifestAndSettings(t *testing.T) {
	p := Factory(nil)()
	m := p.Manifest()
	if m.ID != "ssh" || m.Name != "SSH hosts" || len(m.Topics) != 1 || m.Topics[0].Name != "hosts" || len(m.UI.Nav) != 1 || m.UI.Nav[0].Path != "/hosts" || m.UI.Nav[0].Icon != "Server" {
		t.Errorf("manifest = %+v", m)
	}
	if plugin.TopicName(m.ID, m.Topics[0]) != model.TopicSSHHosts {
		t.Errorf("topic %s, want %s", plugin.TopicName(m.ID, m.Topics[0]), model.TopicSSHHosts)
	}
	c := p.(plugin.Configured)
	if c.Configured(&pluginconf.SSH{}) || !c.Configured(&pluginconf.SSH{Hosts: []pluginconf.SSHHost{{}}}) {
		t.Error("Configured is not len(hosts) > 0")
	}
	if p.(plugin.ConfigHinter).ConfigHint() != "plugins.ssh.hosts" {
		t.Error("hint")
	}
	if err := p.(plugin.Validator).Validate(&pluginconf.SSH{IntervalSeconds: 1}); err == nil {
		t.Error("an interval of 1 was accepted")
	}
}
