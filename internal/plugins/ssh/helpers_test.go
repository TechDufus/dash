package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
	"github.com/TechDufus/dash/internal/secret"
)

// testTiming is the plugin's cadence shortened for tests: frames every 100 ms, a host silent for
// 400 ms is gone, and every retry comes within a fraction of a second.
func testTiming() timing {
	return timing{
		handshake: 2 * time.Second, keepalive: time.Second,
		frameGrace: 400 * time.Millisecond, inventoryGrace: 400 * time.Millisecond,
		rescan: 100 * time.Millisecond, keyChanged: 100 * time.Millisecond, unsupported: 100 * time.Millisecond,
		authFirst: 50 * time.Millisecond, retryMin: 50 * time.Millisecond, retryMax: 200 * time.Millisecond,
		interval: 100 * time.Millisecond,
	}
}

// fixedIdentity is an Identities with one key, or an error.
type fixedIdentity struct {
	key ed25519.PrivateKey
	err error
}

func (f fixedIdentity) Identity() (ed25519.PrivateKey, error) { return f.key, f.err }

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func signerOf(t *testing.T, k ed25519.PrivateKey) ssh.Signer {
	t.Helper()
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// behaviour is what the fake host does once the script arrives.
type behaviour int

const (
	sendFrames  behaviour = iota // a frame every 100 ms, the inventory on the first
	stayQuiet                    // no output at all, the connection open
	sendHuge                     // one frame larger than maxFrameBytes
	sendFreeBSD                  // frames of a host that is not Linux
	exitAtOnce                   // the command exits with status 127 before any output
)

// fakeHost is an SSH server in the test process: a host key, the public keys it accepts, a
// record of every authentication request and every script it received, and a behaviour per
// session.
type fakeHost struct {
	t    *testing.T
	ln   net.Listener
	key  ssh.Signer
	mu   sync.Mutex
	auth map[string]bool // accepted public keys, by their wire form
	// authRequests counts every authentication request, the "none" method included.
	authRequests int
	scripts      []string
	commands     []string
	behave       func(session int) behaviour
	sessions     int
	open         int // sessions whose channel is open
	conns        []net.Conn
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHost{t: t, ln: ln, key: signerOf(t, newKey(t)), auth: map[string]bool{}, behave: func(int) behaviour { return sendFrames }}
	go f.accept()
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.conns {
			_ = c.Close()
		}
	})
	return f
}

func (f *fakeHost) addr() string { return f.ln.Addr().String() }

// hostKey is the host's key as plugins.ssh.hosts[].hostKey holds it.
func (f *fakeHost) hostKey() string { return authorizedKey(f.key.PublicKey()) }

func (f *fakeHost) authorize(pub ssh.PublicKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth[string(pub.Marshal())] = true
}

func (f *fakeHost) setBehaviour(b func(int) behaviour) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.behave = b
}

func (f *fakeHost) authCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authRequests
}

func (f *fakeHost) openSessions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open
}

func (f *fakeHost) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.scripts...)
}

func (f *fakeHost) accept() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		go f.serve(c)
	}
}

func (f *fakeHost) serve(c net.Conn) {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.auth[string(key.Marshal())] {
				return nil, nil
			}
			return nil, errors.New("key not authorized")
		},
		AuthLogCallback: func(ssh.ConnMetadata, string, error) {
			f.mu.Lock()
			f.authRequests++
			f.mu.Unlock()
		},
	}
	cfg.AddHostKey(f.key)
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		_ = c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go f.session(ch, creqs)
	}
}

var nonceRE = regexp.MustCompile(`M='@@([0-9a-f]+)'`)

func (f *fakeHost) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	f.mu.Lock()
	n := f.sessions
	f.sessions++
	f.open++
	b := f.behave(n)
	f.mu.Unlock()
	defer func() {
		_ = ch.Close()
		f.mu.Lock()
		f.open--
		f.mu.Unlock()
	}()
	var cmd string
	for r := range reqs {
		if r.Type == "exec" {
			var p struct{ Command string }
			_ = ssh.Unmarshal(r.Payload, &p)
			cmd = p.Command
			_ = r.Reply(true, nil)
			break
		}
		_ = r.Reply(false, nil)
	}
	// The requests channel closes when the client closes the session or the connection.
	gone := make(chan struct{})
	go func() { ssh.DiscardRequests(reqs); close(gone) }()
	script, _ := io.ReadAll(ch)
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.scripts = append(f.scripts, string(script))
	f.mu.Unlock()
	m := nonceRE.FindStringSubmatch(string(script))
	if m == nil {
		return
	}
	nonce := m[1]
	switch b {
	case exitAtOnce:
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{127}))
		return
	case stayQuiet:
		<-gone // open and silent until the client closes it
		return
	case sendHuge:
		big := strings.Repeat(strings.Repeat("x", 1000)+"\n", maxFrameBytes/1000+10)
		_, _ = io.WriteString(ch, "@@"+nonce+" frame 0\n@@"+nonce+" stat\n"+big+"@@"+nonce+" end\n")
		<-gone
		return
	}
	inv := debianInventory
	if b == sendFreeBSD {
		inv = []string{"uname\nFreeBSD\n14.3-RELEASE\namd64\nbsd-1"}
	}
	for seq := 0; ; seq++ {
		sections := fastSections(seq, debianHwmon, debianThermal)
		if seq == 0 {
			sections = append(append([]string{}, inv...), sections...)
		}
		if _, err := io.WriteString(ch, frameText(nonce, seq, sections...)); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stubHost is the plugin.Host of the tests: it records the payloads and dials without a guard,
// or, with refuse, answers every dial as the guard answers a refused address.
type stubHost struct {
	refuse bool
	mu     sync.Mutex
	last   *sshmodel.HostsData
	srcs   []model.SourceStatus
	count  int
}

func (h *stubHost) Publish(_ string, data any, sources []model.SourceStatus) {
	d := data.(sshmodel.HostsData)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last, h.srcs = &d, sources
	h.count++
}
func (h *stubHost) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }
func (h *stubHost) HTTPClient(plugin.HTTPConfig) (*http.Client, error) {
	return nil, errors.New("the ssh plugin makes no HTTP request")
}
func (h *stubHost) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if h.refuse {
		return nil, fmt.Errorf("dial: refused: %w", plugin.ErrBlockedAddress)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}
func (h *stubHost) ScrubLog(s string) string { return s }
func (h *stubHost) CutLog(s string, n int) (string, bool) {
	return (*secret.Registry)(nil).CutRunes(s, n)
}
func (h *stubHost) Links() plugin.LinkResolver     { return plugin.NoLinks }
func (h *stubHost) IconBaseURL() string            { return "" }
func (h *stubHost) Service(plugin.ID) (any, bool)  { return nil, false }
func (h *stubHost) Now() time.Time                 { return time.Now() }
func (h *stubHost) Go(_ string, fn func())         { go fn() }
func (h *stubHost) published() *sshmodel.HostsData { h.mu.Lock(); defer h.mu.Unlock(); return h.last }

// rig is one plugin over a stub host, with its factory's shared state.
type rig struct {
	t   *testing.T
	sh  *shared
	key ed25519.PrivateKey
	p   *Plugin
	h   *stubHost
	// refuse makes the next start's host refuse every dial as the dial guard does.
	refuse bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	key := newKey(t)
	return &rig{t: t, key: key, sh: &shared{ident: fixedIdentity{key: key}, hist: newHistory()}}
}

// start starts a fresh plugin of the rig's factory with hosts.
func (r *rig) start(hosts ...pluginconf.SSHHost) {
	r.t.Helper()
	r.p, r.h = newPlugin(r.sh, testTiming()), &stubHost{refuse: r.refuse}
	cfg := pluginconf.SSH{Hosts: hosts, IntervalSeconds: 2}
	if err := cfg.Validate(); err != nil {
		r.t.Fatal(err)
	}
	if err := r.p.Start(context.Background(), r.h, &cfg); err != nil {
		r.t.Fatal(err)
	}
	p := r.p
	r.t.Cleanup(func() { _ = p.Stop(context.Background()) })
}

func (r *rig) stop() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.p.Stop(ctx); err != nil {
		r.t.Fatalf("Stop: %v", err)
	}
}

// host is the plugin's current view of host i (not only what was last published).
func (r *rig) host(i int) sshmodel.Host { return r.p.hosts()[i] }

// await waits until host i is in state, and returns it.
func (r *rig) await(i int, state sshmodel.HostState) sshmodel.Host {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h := r.host(i); h.State == state {
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("host %d is %s, want %s", i, r.host(i).State, state)
	return sshmodel.Host{}
}

// publicKey is the rig's identity as a host authorizes it.
func (r *rig) publicKey() ssh.PublicKey { return signerOf(r.t, r.key).PublicKey() }

// eventually waits for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
