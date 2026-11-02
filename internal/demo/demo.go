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
)

// tick is how often the fixtures drift, so the UI looks live.
const tick = 5 * time.Second

// Source serves the fixtures to the hub and the API.
type Source struct {
	// HasReal reports whether a plugin has a real factory (and so a demo twin of its own). Nothing
	// reads it any more: every integration is a plugin, and it goes with cmd/dash's hasRealPlugin
	// in P3.13.
	HasReal func(id plugin.ID) bool

	world *world.World
	start time.Time
	now   func() time.Time
	hosts plugin.HostProvider
}

// New returns a Source anchored at the current time. Call Attach before Run.
func New(cfg config.Config) *Source {
	s := &Source{start: time.Now(), now: time.Now}
	s.world = world.New(func() time.Time { return s.now() }, s.start, newLinks())
	return s
}

// Attach records the host provider the overview is published through (the supervisor the
// plugins are added to). Every integration is a plugin with a demo twin of its own, so the
// Source registers none.
func (s *Source) Attach(hosts plugin.HostProvider) { s.hosts = hosts }

// Run publishes the overview now and again on each tick until ctx ends; it does not block. Attach
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
	w := s.world.Build()
	at := s.now()
	src := func(names ...string) []model.SourceStatus {
		out := make([]model.SourceStatus, len(names))
		for i, n := range names {
			out[i] = model.SourceStatus{Name: n, OK: true, CheckedAt: at}
		}
		return out
	}
	// The alertmanager, flux, hardware, kubernetes and proxmox plugins' KPIs are on the overview
	// while their twins run, and so are the proxmox VMs, the hardware host and the kubernetes groups
	// in its topology (and the kubernetes and flux attention items). Their source chips are the
	// twin's own (each name once: flux reads the cluster through the same "kubernetes" chip as the
	// kubernetes plugin), except hardware's: the fake host's series come from the victoriametrics
	// chip every overview has. The topics of every plugin are its twin's to publish.
	ov, ovSrcs := w.Overview, src("victoriametrics")
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
		{"flux", []string{"kubernetes"}, func(o *model.OverviewData) {
			o.Attention = slices.DeleteFunc(slices.Clone(o.Attention), func(a model.AttentionItem) bool { return a.Plugin == "flux" })
		}},
	} {
		if s.hosts.Running(twin.id) {
			for _, c := range src(twin.chips...) {
				if !slices.ContainsFunc(ovSrcs, func(have model.SourceStatus) bool { return have.Name == c.Name }) {
					ovSrcs = append(ovSrcs, c)
				}
			}
			continue
		}
		ov.KPIList = slices.DeleteFunc(slices.Clone(ov.KPIList), func(k model.KPI) bool { return k.Plugin == string(twin.id) })
		twin.drop(&ov)
	}
	s.hosts.HostFor(plugin.Core).Publish(strings.TrimPrefix(string(model.TopicOverview), string(plugin.Core)+"."), ov, ovSrcs)
}

// Wrap signs every request in as the user "demo". Everything else is the API's own: the actions
// are the plugins' (their twins fake them), and the retired /api/actions/* routes answer 404 here
// as they do in production.
func Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-User") == "" {
			r.Header.Set("X-Forwarded-User", "demo")
		}
		next.ServeHTTP(w, r)
	})
}
