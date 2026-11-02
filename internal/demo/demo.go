// Package demo runs dash without a cluster: DASH_DEMO=true publishes a fixed, obviously fake
// homelab on every topic and answers the REST endpoints from the same fixtures. It exists for
// screenshots and UI work; nothing in it talks to Kubernetes, Proxmox or any monitoring service.
package demo

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/topics"
)

// tick is how often the fixtures drift, so the UI looks live.
const tick = 5 * time.Second

// Source serves the fixtures to the hub and the API.
type Source struct {
	// HasReal reports whether a plugin has a real factory (and so publishes its own demo data
	// through its twin). Nil means none does. Set it before Plugins.
	HasReal func(id plugin.ID) bool

	world *world.World
	start time.Time
	now   func() time.Time
	hosts plugin.HostProvider
}

// New returns a Source anchored at the current time. Call Plugins before Run.
func New(cfg config.Config) *Source {
	s := &Source{start: time.Now(), now: time.Now}
	s.world = world.New(func() time.Time { return s.now() }, s.start, newLinks())
	return s
}

// Plugins returns the demo shims of the legacy manifests, all healthy: the demo has nothing to
// poll. Unlike the production shims they can be switched off and on at runtime. Each topic is
// published through the Host of its plugin in hosts (the supervisor they are added to), so
// disabling a plugin in demo mode removes its topics like it does for real. A plugin for which
// HasReal reports a factory is left out and its topics are not published here: its demo twin
// builds them from the world, so a twin and a shim never both register.
func (s *Source) Plugins(hosts plugin.HostProvider) []plugin.Factory {
	s.hosts = hosts
	ms := topics.LegacyManifests()
	out := make([]plugin.Factory, 0, len(ms))
	for _, m := range ms {
		if s.real(m.ID) {
			continue
		}
		out = append(out, func() plugin.Plugin { return demoPlugin{m} })
	}
	return out
}

// real reports whether the plugin has a real factory, and with it a demo twin.
func (s *Source) real(id plugin.ID) bool { return s.HasReal != nil && s.HasReal(id) }

// Run publishes every topic now and again on each tick until ctx ends; it does not block. Plugins
// must have been called.
func (s *Source) Run(ctx context.Context) {
	s.publish()
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.publish()
			}
		}
	}()
}

func (s *Source) publish() {
	w := s.build()
	at := s.now()
	src := func(names ...string) []model.SourceStatus {
		out := make([]model.SourceStatus, len(names))
		for i, n := range names {
			out[i] = model.SourceStatus{Name: n, OK: true, CheckedAt: at}
		}
		return out
	}
	// The alertmanager, hardware, kubernetes and proxmox plugins' KPIs are on the overview while
	// their twins run, and so are the proxmox VMs, the hardware host and the kubernetes groups in
	// its topology (and the kubernetes attention items). Their source chips are the twin's own,
	// except hardware's: the fake host's series come from the victoriametrics chip every overview
	// has. The cluster, storage and apps topics are the kubernetes twin's to publish.
	ov, ovSrcs := w.overview, src("victoriametrics")
	for _, twin := range []struct {
		id    plugin.ID
		chips []string
		drop  func(*model.OverviewData)
	}{
		{"proxmox", []string{"proxmox"}, func(o *model.OverviewData) { o.Topology.VMs = []model.TopologyVM{} }},
		{"alertmanager", []string{"alertmanager"}, func(*model.OverviewData) {}},
		{"hardware", nil, func(o *model.OverviewData) { o.Topology.Host = model.TopologyHost{} }},
		{"kubernetes", []string{"kubernetes"}, func(o *model.OverviewData) {
			o.Topology.Groups = []model.TopologyGroup{}
			o.Attention = slices.DeleteFunc(slices.Clone(o.Attention), func(a model.AttentionItem) bool { return a.Plugin == "kubernetes" })
		}},
	} {
		if s.hosts.Running(twin.id) {
			ovSrcs = append(ovSrcs, src(twin.chips...)...)
			continue
		}
		ov.KPIList = slices.DeleteFunc(slices.Clone(ov.KPIList), func(k model.KPI) bool { return k.Plugin == string(twin.id) })
		twin.drop(&ov)
	}
	for _, p := range []struct {
		topic model.Topic
		data  any
		srcs  []model.SourceStatus
	}{
		{model.TopicOverview, ov, ovSrcs},
		{model.TopicFlux, w.flux, src("kubernetes")},
	} {
		if id, local, ok := topics.Owner(p.topic); ok {
			if s.hosts.Running(id) && !s.real(id) { // a disabled plugin publishes nothing; a twin publishes for itself
				s.hosts.HostFor(id).Publish(local, p.data, p.srcs)
			}
		} else {
			s.hosts.HostFor(plugin.Core).Publish(strings.TrimPrefix(string(p.topic), string(plugin.Core)+"."), p.data, p.srcs)
		}
	}
}

// Wrap signs every request in as the user "demo" and answers the legacy flux reconcile route with
// 202 without doing anything, since demo mode has no cluster to patch. The restart action is the
// kubernetes twin's (a framework action), and the retired /api/actions/restart is not answered here.
func Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-User") == "" {
			r.Header.Set("X-Forwarded-User", "demo")
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/actions/reconcile" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"accepted"}` + "\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
