package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// demoEvery is how often the twin's values move on, like the rest of demo mode.
const demoEvery = 5 * time.Second

// Demo is the twin DASH_DEMO=true runs instead of the plugin (plugin.Demo).
func (p *Plugin) Demo() plugin.Plugin { return &twin{} }

// twin is the plugin's demo twin: the same manifest and route, the fake world's three hosts, a
// made-up identity, and no network. It is enabled and configured whatever the settings say, and
// raises the same attention items as the plugin.
type twin struct {
	plugin.ObjectStore
	mu     sync.Mutex
	h      plugin.Host
	w      *world.World
	cancel context.CancelFunc
	done   chan struct{}
}

func (t *twin) Manifest() plugin.Manifest { return manifest() }

// demoIdentity is the twin's public key: made from a fixed seed, so every demo run shows the
// same one, and a key no host anywhere trusts.
func demoIdentity() sshmodel.Identity {
	seed := sha256.Sum256([]byte("dash demo ssh identity"))
	pub, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(seed[:]).Public())
	if err != nil {
		panic(err) // an ed25519 key always converts
	}
	return identityOf(pub)
}

// Start publishes the fake hosts now and then every demoEvery.
func (t *twin) Start(_ context.Context, h plugin.Host, _ any) error {
	h = t.Wrap(h)
	w := world.New(h.Now, h.Now(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.mu.Lock()
	t.h, t.w, t.cancel, t.done = h, w, cancel, done
	t.mu.Unlock()

	t.publish()
	h.Go("ssh demo", func() {
		defer close(done)
		tick := time.NewTicker(demoEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				t.publish()
			}
		}
	})
	return nil
}

func (t *twin) Stop(ctx context.Context) error {
	t.mu.Lock()
	cancel, done := t.cancel, t.done
	t.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// hosts are the fake hosts as the plugin would publish them.
func (t *twin) hosts() ([]sshmodel.Host, plugin.Host) {
	t.mu.Lock()
	h, w := t.h, t.w
	t.mu.Unlock()
	if h == nil {
		return nil, nil
	}
	hosts := w.SSHHosts()
	for i := range hosts {
		hosts[i].Message = stateMessages[hosts[i].State]
		hosts[i].Health = string(healthOf(hosts[i]))
	}
	return hosts, h
}

func (t *twin) data() (sshmodel.HostsData, []model.SourceStatus, plugin.Host) {
	hosts, h := t.hosts()
	if h == nil {
		return sshmodel.HostsData{}, nil, nil
	}
	id := demoIdentity()
	now := h.Now()
	var srcs []model.SourceStatus
	for _, x := range hosts {
		if s, ok := sourceOf(x, now); ok {
			srcs = append(srcs, s)
		}
	}
	return sshmodel.HostsData{Hosts: hosts, Identity: &id, IntervalSeconds: world.SSHIntervalSeconds}, srcs, h
}

func (t *twin) publish() {
	d, srcs, h := t.data()
	if h == nil {
		return
	}
	h.Publish(topicHosts, d, srcs)
}

func (t *twin) Status() plugin.Status {
	d, srcs, h := t.data()
	if h == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	// The status's sources carry no time of their own, as the other twins' do.
	for i := range srcs {
		srcs[i].CheckedAt = time.Time{}
	}
	return statusOf(d.Hosts, srcs)
}

func (t *twin) Attention(time.Time) []model.AttentionItem {
	hosts, h := t.hosts()
	if h == nil {
		return nil
	}
	return attentionOf(hosts)
}

// Routes answers the history route from the fake world: the same route and 404 as the plugin.
func (t *twin) Routes(r plugin.Routes) {
	historyRoute(r, func(name string) (sshmodel.History, bool) {
		t.mu.Lock()
		w := t.w
		t.mu.Unlock()
		if w == nil {
			return sshmodel.History{}, false
		}
		return w.SSHHistory(name)
	})
}

// Context implements plugin.Objects for the twin, like the plugin.
func (t *twin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return hostContext(&t.ObjectStore, ref)
}

var (
	_ plugin.Plugin          = (*twin)(nil)
	_ plugin.AttentionSource = (*twin)(nil)
	_ plugin.Router          = (*twin)(nil)
	_ plugin.Objects         = (*twin)(nil)
)
