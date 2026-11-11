// Package ssh is the ssh plugin ("SSH hosts"): the Linux machines an administrator adds by
// address and user. dash connects to each over SSH with a key pair of its own, installs nothing,
// and runs one fixed, read-only POSIX sh script in one exec session (script.go); the script
// prints raw /proc, /sys and a few command outputs, and every fact and rate is computed here. It
// publishes ssh.hosts, keeps an hour of samples per host for the history route, raises the hosts'
// attention items and makes each host an object (ssh:Host/<name>).
//
// Host keys are pinned, never trusted blindly: a host without plugins.ssh.hosts[].hostKey is only
// asked for the key it presents (the handshake ends before any credential is sent), and a host
// presenting another key than the pinned one is refused. Viewers see no address, user or error
// text from the network: those stay in the settings API and in dash's log.
//
// Like every plugin it imports neither internal/config nor internal/api, nor internal/credentials:
// cmd/dash hands it the key pair through Factory.
package ssh

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// ID is the plugin's id and its instance name: plugins.ssh, topic ssh.hosts.
const ID plugin.ID = "ssh"

// topicHosts is the manifest name of the topic.
const topicHosts = "hosts"

// routeHosts is the Hosts page; a host's page is below it (hostRoute).
const routeHosts = "/hosts"

// Identities is where the plugin's key pair comes from: cmd/dash hands in one kept sealed in
// credentials.yaml. Identity returns the private key, made at the first call and the same at every
// later one. An error that wraps ErrNoIdentity means dash has nowhere to keep a key.
type Identities interface {
	Identity() (ed25519.PrivateKey, error)
}

// ErrNoIdentity is what Identities wraps when dash has no DASH_SECRET_KEY or no writable data
// directory: a key it cannot keep would change at every start, and every host would refuse it.
var ErrNoIdentity = errors.New("ssh: no place to keep the SSH key")

// msgNoIdentity is Start's error without a key pair: fixed words that name what to set.
const msgNoIdentity = "the ssh plugin needs DASH_SECRET_KEY and a writable data directory (DASH_DATA_DIR) to keep its SSH key; set both and restart dash"

// identityComment ends the public key's authorized_keys line.
const identityComment = "dash"

// timing is the plugin's cadence and its limits in time; tests shorten them.
type timing struct {
	// handshake caps the dial, the SSH handshake and the authentication together.
	handshake time.Duration
	// keepalive is how often an idle connection is asked for a reply, and how long the reply
	// may take before the connection counts as gone.
	keepalive time.Duration
	// frameGrace is how much longer than the interval a frame may take; inventoryGrace the same
	// for a frame that carries the inventory, whose commands may each take up to 20 s.
	frameGrace, inventoryGrace time.Duration
	// rescan is how often a host without a pinned key is asked for its key again, keyChanged
	// how often a host whose key changed is tried again, and unsupported how often a host that
	// is not Linux is.
	rescan, keyChanged, unsupported time.Duration
	// authFirst is the first wait after a host refused the key, so that the add-host flow sees
	// the authorization quickly; it grows to retryMax.
	authFirst time.Duration
	// retryMin and retryMax bound the backoff of a host that cannot be reached.
	retryMin, retryMax time.Duration
	// interval, when set, replaces plugins.ssh.intervalSeconds for the deadlines and the
	// publishing (the script still sleeps whole seconds).
	interval time.Duration
}

func defaultTiming() timing {
	return timing{
		handshake: 15 * time.Second, keepalive: 15 * time.Second,
		frameGrace: 20 * time.Second, inventoryGrace: 2 * time.Minute,
		rescan: 2 * time.Minute, keyChanged: time.Minute, unsupported: 5 * time.Minute,
		authFirst: 3 * time.Second, retryMin: 2 * time.Second, retryMax: time.Minute,
	}
}

// shared is what outlives a restart of the instance: the key pair's source and the history.
type shared struct {
	ident Identities
	hist  *history
}

// Factory is the plugin's factory. ident is the key pair's source (nil: the plugin cannot start,
// which is all the manifests, the routes and the settings need). The history lives here, so a
// settings change, which restarts the instance, keeps the charts' hour.
func Factory(ident Identities) plugin.Factory {
	sh := &shared{ident: ident, hist: newHistory()}
	return func() plugin.Plugin { return newPlugin(sh, defaultTiming()) }
}

func newPlugin(sh *shared, tm timing) *Plugin { return &Plugin{sh: sh, tm: tm} }

func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "SSH hosts",
		Description: "Linux machines read over SSH: their hardware, what runs on them and live metrics.",
		Version:     "1.0.0",
		Config:      func() any { return &pluginconf.SSH{} },
		Topics: []plugin.TopicSpec{
			{Name: topicHosts, Type: sshmodel.HostsData{}, Doc: "Every SSH host: its state, inventory and latest sample."},
		},
		UI: plugin.UISpec{Nav: []model.NavItem{
			{Path: routeHosts, Label: "Hosts", Title: "Hosts", Icon: "Server"},
		}},
	}
}

// Plugin is the ssh integration. The supervisor makes a fresh one for every start of the
// instance; the factory's shared state is the same for all of them.
type Plugin struct {
	plugin.ObjectStore
	sh *shared
	tm timing
	rt atomic.Pointer[runtime]

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *Plugin) Manifest() plugin.Manifest { return manifest() }

// settings is the plugin's section as the supervisor hands it over: a pointer, or a value.
func settings(cfg any) (pluginconf.SSH, error) {
	switch c := cfg.(type) {
	case *pluginconf.SSH:
		if c != nil {
			return *c, nil
		}
	case pluginconf.SSH:
		return c, nil
	}
	return pluginconf.SSH{}, fmt.Errorf("ssh: unexpected configuration type %T", cfg)
}

// Configured is false without a host: the plugin is then disabled, or unconfigured when it was
// switched on.
func (p *Plugin) Configured(cfg any) bool {
	c, err := settings(cfg)
	return err == nil && len(c.Hosts) > 0
}

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.ssh.hosts" }

// Validate applies the section's own rules, so that a configuration the supervisor gets by
// another way than the config loader is refused for the same reasons.
func (p *Plugin) Validate(cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	return c.Validate()
}

// runtime is what one Start knows and what its goroutines share.
type runtime struct {
	h        plugin.Host
	cfg      pluginconf.SSH // normalized
	tm       timing
	interval time.Duration
	signer   ssh.Signer
	identity sshmodel.Identity
	hist     *history
	dirty    chan struct{} // a host changed: the publisher sends the topic again

	mu    sync.Mutex
	hosts []hostState // by the host's index in plugins.ssh.hosts
}

// hostState is where one host stands, as the topic shows it.
type hostState struct {
	state sshmodel.HostState
	// blocked is set while the host is unreachable because the dial guard refused its address.
	blocked bool
	since   time.Time
	offered *sshmodel.HostKey
	info    *sshmodel.Info
	stats   *sshmodel.Stats
	checked time.Time
}

// intervalSeconds is the configured interval, 5 when a zero config reaches the plugin.
func intervalSeconds(c pluginconf.SSH) int { return cmp.Or(c.IntervalSeconds, 5) }

// Start reads the key pair, then starts one goroutine per host and the publisher; it does not
// wait for a connection. Without a place to keep the key it fails with msgNoIdentity.
func (p *Plugin) Start(_ context.Context, h plugin.Host, cfg any) error {
	h = p.Wrap(h)
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	c = c.Normalized()
	if p.sh.ident == nil {
		return errors.New(msgNoIdentity)
	}
	priv, err := p.sh.ident.Identity()
	switch {
	case errors.Is(err, ErrNoIdentity):
		return errors.New(msgNoIdentity)
	case err != nil:
		return fmt.Errorf("ssh: reading the SSH key from credentials.yaml: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return fmt.Errorf("ssh: the SSH key: %w", err)
	}
	pins := make([]ssh.PublicKey, len(c.Hosts))
	for i, x := range c.Hosts {
		if x.HostKey == "" {
			continue
		}
		if pins[i], err = pluginconf.ParseSSHHostKey(x.HostKey); err != nil {
			return fmt.Errorf("ssh: plugins.ssh.hosts entry %d: hostKey does not parse", i)
		}
	}
	interval := cmp.Or(p.tm.interval, time.Duration(intervalSeconds(c))*time.Second)
	rt := &runtime{
		h: h, cfg: c, tm: p.tm, interval: interval, signer: signer, identity: identityOf(signer.PublicKey()),
		hist: p.sh.hist, dirty: make(chan struct{}, 1), hosts: make([]hostState, len(c.Hosts)),
	}
	now := h.Now()
	names := make([]string, len(c.Hosts))
	for i, x := range c.Hosts {
		rt.hosts[i] = hostState{state: sshmodel.StateConnecting, since: now}
		names[i] = x.Name
	}
	p.sh.hist.keep(names)
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	p.rt.Store(rt)

	rt.publish()
	for i, x := range c.Hosts {
		r := newRunner(rt, i, x, pins[i])
		p.spawn(h, "ssh host "+x.Name, func() { r.loop(ctx) })
	}
	p.spawn(h, "ssh publisher", func() { rt.publisher(ctx) })
	return nil
}

// identityOf is the public half of the key pair as the topic shows it.
func identityOf(pub ssh.PublicKey) sshmodel.Identity {
	return sshmodel.Identity{PublicKey: authorizedKey(pub) + " " + identityComment, Fingerprint: ssh.FingerprintSHA256(pub)}
}

// authorizedKey is key as one authorized_keys entry without a comment: "ssh-ed25519 AAAA...".
func authorizedKey(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// hostKeyOf is a key a host presented, as the topic shows it.
func hostKeyOf(key ssh.PublicKey) *sshmodel.HostKey {
	return &sshmodel.HostKey{Type: key.Type(), Fingerprint: ssh.FingerprintSHA256(key), Key: authorizedKey(key)}
}

// spawn runs fn on Host.Go and counts it, so that Stop can wait for it.
func (p *Plugin) spawn(h plugin.Host, name string, fn func()) {
	p.wg.Add(1)
	h.Go(name, func() {
		defer p.wg.Done()
		fn()
	})
}

// Stop closes every connection and waits for the goroutines, as long as ctx allows.
func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// update changes host i's state and asks the publisher to send the topic. A state that changes
// restarts since.
func (rt *runtime) update(i int, fn func(s *hostState)) {
	now := rt.h.Now()
	rt.mu.Lock()
	s := &rt.hosts[i]
	was := s.state
	fn(s)
	if s.state != was {
		s.since = now
	}
	s.checked = now
	rt.mu.Unlock()
	select {
	case rt.dirty <- struct{}{}:
	default:
	}
}

// publisher sends the topic when a host changed, at most once per interval.
func (rt *runtime) publisher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.dirty:
		}
		rt.publish()
		t := time.NewTimer(rt.interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (rt *runtime) publish() {
	data, srcs := rt.data()
	rt.h.Publish(topicHosts, data, srcs)
}

// data is the topic's payload and its sources, one per host that has left connecting.
func (rt *runtime) data() (sshmodel.HostsData, []model.SourceStatus) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	id := rt.identity
	d := sshmodel.HostsData{Hosts: make([]sshmodel.Host, len(rt.hosts)), Identity: &id, IntervalSeconds: intervalSeconds(rt.cfg)}
	var srcs []model.SourceStatus
	for i, s := range rt.hosts {
		d.Hosts[i] = hostOf(rt.cfg.Hosts[i].Name, s)
		if src, ok := sourceOf(d.Hosts[i], s.checked); ok {
			srcs = append(srcs, src)
		}
	}
	return d, srcs
}

// hostOf is one host as the topic shows it.
func hostOf(name string, s hostState) sshmodel.Host {
	h := sshmodel.Host{
		Ref: plugin.Ref(ID, "Host", name), Name: name, State: s.state, Message: stateMessages[s.state],
		Since: s.since.UTC(), Info: cloneInfo(s.info),
	}
	if s.state == sshmodel.StateUnreachable && s.blocked {
		h.Message = blockedMessage
	}
	switch s.state {
	case sshmodel.StateUnverified, sshmodel.StateKeyChanged:
		if s.offered != nil {
			k := *s.offered
			h.OfferedKey = &k
		}
	case sshmodel.StateUp:
		if s.stats != nil {
			st := *s.stats
			h.Stats = &st
		}
	}
	h.Health = string(healthOf(h))
	return h
}

// cloneInfo copies an inventory so the payload shares no slice with the runner's state.
func cloneInfo(in *sshmodel.Info) *sshmodel.Info {
	if in == nil {
		return nil
	}
	out := *in
	out.Disks, out.Filesystems, out.Pools = slices.Clone(in.Disks), slices.Clone(in.Filesystems), slices.Clone(in.Pools)
	if in.Containers != nil {
		c := *in.Containers
		c.Items = slices.Clone(c.Items)
		out.Containers = &c
	}
	if in.Services != nil {
		s := *in.Services
		s.Failed = slices.Clone(s.Failed)
		out.Services = &s
	}
	return &out
}

// stateMessages say what each state means, in fixed words: never what a host or the network
// sent.
var stateMessages = map[sshmodel.HostState]string{
	sshmodel.StateConnecting:   "dash has not reached the host yet.",
	sshmodel.StateUnverified:   "The host's key is not trusted yet: compare its fingerprint with the one the host shows (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub) and trust it. dash sends the host nothing until then.",
	sshmodel.StateKeyChanged:   "The host presents another key than the trusted one, so dash refuses it: check the host, then trust the new key if the change is expected.",
	sshmodel.StateUnauthorized: "The host refuses dash's key for this user: add dash's public key to the user's ~/.ssh/authorized_keys.",
	sshmodel.StateUnreachable:  "dash cannot reach the host, or lost the connection; it keeps trying.",
	sshmodel.StateUnsupported:  "The host answered, but it is not Linux or cannot run dash's read-only script.",
	sshmodel.StateUp:           "dash reads the host.",
}

// blockedMessage replaces the unreachable message of a host whose address the dial guard
// refuses: waiting does not help there, so the administrator is told what kind of address it is.
// It names the rule, never the address.
const blockedMessage = "dash does not connect to this address: link-local addresses are always refused, and a loopback address is allowed only for a host set in the config file or the environment."

// sourceCategory is the source error of each state that is not up.
var sourceCategory = map[sshmodel.HostState]string{
	sshmodel.StateUnverified:   "unverified",
	sshmodel.StateKeyChanged:   "key_changed",
	sshmodel.StateUnauthorized: "auth",
	sshmodel.StateUnreachable:  "unreachable",
	sshmodel.StateUnsupported:  "unsupported",
}

// sourceOf is the source a host adds to the envelope and the status: its name and a category.
// A host still connecting has none yet.
func sourceOf(h sshmodel.Host, checked time.Time) (model.SourceStatus, bool) {
	if h.State == sshmodel.StateConnecting {
		return model.SourceStatus{}, false
	}
	return model.SourceStatus{Name: h.Name, OK: h.State == sshmodel.StateUp, Error: sourceCategory[h.State], CheckedAt: checked.UTC()}, true
}

// Status is starting until every host has been tried once, then healthy when every host is up,
// error when every host fails, and degraded otherwise; the message names each host that is not
// up and its category. A host whose key waits to be trusted has not failed: it is a step of
// adding it that only an administrator can take, so by itself it makes the plugin degraded,
// never error.
func (p *Plugin) Status() plugin.Status {
	rt := p.rt.Load()
	if rt == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	d, srcs := rt.data()
	return statusOf(d.Hosts, srcs)
}

func statusOf(hosts []sshmodel.Host, srcs []model.SourceStatus) plugin.Status {
	if slices.ContainsFunc(hosts, func(h sshmodel.Host) bool { return h.State == sshmodel.StateConnecting }) {
		return plugin.Status{State: plugin.StateStarting, Message: "connecting to the hosts", Sources: srcs}
	}
	var bad []string
	failed := 0
	for _, s := range srcs {
		if s.OK {
			continue
		}
		bad = append(bad, s.Name+": "+s.Error)
		if s.Error != sourceCategory[sshmodel.StateUnverified] {
			failed++
		}
	}
	state := plugin.StateHealthy
	switch {
	case len(srcs) > 0 && failed == len(srcs):
		state = plugin.StateError
	case len(bad) > 0:
		state = plugin.StateDegraded
	}
	return plugin.Status{State: state, Message: strings.Join(bad, "; "), Sources: srcs}
}

// hosts is the payload's hosts as they are now; nil before Start.
func (p *Plugin) hosts() []sshmodel.Host {
	rt := p.rt.Load()
	if rt == nil {
		return nil
	}
	d, _ := rt.data()
	return d.Hosts
}

// Attention raises the items of design 1.2 for every host. It has no opinion (nil) until every
// host has been tried once.
func (p *Plugin) Attention(time.Time) []model.AttentionItem {
	hosts := p.hosts()
	if hosts == nil || slices.ContainsFunc(hosts, func(h sshmodel.Host) bool { return h.State == sshmodel.StateConnecting }) {
		return nil
	}
	return attentionOf(hosts)
}

// Routes implements plugin.Router: GET /api/v1/plugins/ssh/hosts/{name}/history.
func (p *Plugin) Routes(r plugin.Routes) {
	historyRoute(r, func(name string) (sshmodel.History, bool) {
		rt := p.rt.Load()
		if rt == nil {
			return sshmodel.History{}, false
		}
		i := slices.IndexFunc(rt.cfg.Hosts, func(x pluginconf.SSHHost) bool { return strings.EqualFold(x.Name, name) })
		if i < 0 {
			return sshmodel.History{}, false
		}
		return sshmodel.History{IntervalSeconds: intervalSeconds(rt.cfg), Samples: rt.hist.samples(rt.cfg.Hosts[i].Name, rt.h.Now())}, true
	})
}

var (
	_ plugin.Plugin          = (*Plugin)(nil)
	_ plugin.Configured      = (*Plugin)(nil)
	_ plugin.ConfigHinter    = (*Plugin)(nil)
	_ plugin.Validator       = (*Plugin)(nil)
	_ plugin.AttentionSource = (*Plugin)(nil)
	_ plugin.Router          = (*Plugin)(nil)
	_ plugin.Objects         = (*Plugin)(nil)
	_ plugin.Demo            = (*Plugin)(nil)
)
