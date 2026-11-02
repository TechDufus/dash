package topics

import (
	"log/slog"
	"sort"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// orZero is *p, or the zero value while the topic is not built (its plugin is disabled).
func orZero[T any](p *T) T {
	if p == nil {
		var z T
		return z
	}
	return *p
}

// buildOverview composes core.overview from the legacy contribution (the Manager's rules over the
// snapshots that exist, zero values for the rest) and what each running plugin in cs adds.
func (m *Manager) buildOverview(b built, cs []plugin.Contributor, now time.Time) model.OverviewData {
	in := AttentionInput{
		Flux: orZero(b.flux), Cluster: orZero(b.cluster), Storage: orZero(b.storage),
		Resolver: m.resolver(), Now: now,
	}
	items := legacyAttention(in, m.tracker)
	topo := topology(in)
	contributions := []contribution{{attention: items, topology: &topo}}
	for _, c := range cs {
		contributions = append(contributions, m.contribution(c, now))
	}
	return compose(contributions, legacyOverview{kpis: kpis(in)})
}

// contributors lists the running plugins that implement an overview source.
func (m *Manager) contributors() []plugin.Contributor {
	if m.d.Contributors == nil {
		return nil
	}
	return m.d.Contributors()
}

// contribution reads one plugin's sources. A plugin that panics contributes nothing that round and
// is logged once until it answers again; it never stops the overview.
func (m *Manager) contribution(c plugin.Contributor, now time.Time) (out contribution) {
	defer func() {
		r := recover()
		m.mu.Lock()
		_, was := m.badSources[c.ID]
		if r == nil {
			delete(m.badSources, c.ID)
		} else {
			m.badSources[c.ID] = struct{}{}
		}
		m.mu.Unlock()
		if r != nil {
			out = contribution{}
			if !was {
				slog.Error("overview contributor panicked", "plugin", c.ID, "panic", r)
			}
		}
	}()
	out.plugin = c.ID
	if c.Attention != nil {
		out.attention = c.Attention.Attention(now)
	}
	if c.KPIs != nil {
		out.kpis = c.KPIs.KPIs()
	}
	if c.Topology != nil {
		t := c.Topology.Topology()
		out.topology = &t
	}
	return out
}

// contributorSources is the source health of the running plugins that contribute to the overview
// (Contributor.Sources), so a plugin's failing upstream shows on the Overview panels.
func (m *Manager) contributorSources(cs []plugin.Contributor) []model.SourceStatus {
	var out []model.SourceStatus
	for _, c := range cs {
		if c.Sources != nil {
			out = append(out, c.Sources()...)
		}
	}
	return out
}

func kpis(in AttentionInput) model.Kpis {
	k := model.Kpis{}
	count := func(l []model.FluxObj) {
		for _, o := range l {
			k.FluxTotal++
			if o.Ready {
				k.FluxReady++
			}
			if o.Suspended {
				k.FluxSuspended++
			}
		}
	}
	count(in.Flux.Kustomizations)
	count(in.Flux.HelmReleases)
	for _, s := range in.Flux.Sources {
		if s.Namespace == "flux-system" && s.Name == "flux-system" && s.Kind == model.FluxKindGitRepository {
			k.SourceVerified = s.Verified != nil && *s.Verified
		}
	}
	for _, n := range in.Cluster.Nodes {
		k.NodesTotal++
		if n.Ready {
			k.NodesReady++
		}
	}
	for _, p := range in.Storage.PVCs {
		if p.UsedRatio != nil && (k.WorstPVC == nil || *p.UsedRatio > k.WorstPVC.UsedRatio) {
			k.WorstPVC = &model.WorstPVC{Namespace: p.Namespace, Name: p.Name, UsedRatio: *p.UsedRatio}
		}
	}
	return k
}

func topology(in AttentionInput) model.Topology {
	t := model.Topology{VMs: []model.TopologyVM{}}

	groupOf := map[string]model.Group{}
	for _, ns := range in.Cluster.Namespaces {
		groupOf[ns.Name] = ns.Group
	}
	groups := map[model.Group]*model.TopologyGroup{}
	for _, g := range []model.Group{model.GroupPlatform, model.GroupApps, model.GroupTenants} {
		groups[g] = &model.TopologyGroup{Name: g, Health: model.HealthOK, Namespaces: []model.TopologyNamespace{}, PodsByNode: map[string]int{}}
	}
	for _, ns := range in.Cluster.Namespaces {
		g := groups[ns.Group]
		if g == nil {
			continue
		}
		g.Namespaces = append(g.Namespaces, model.TopologyNamespace{Name: ns.Name, Health: ns.Health, Pods: ns.PodsTotal})
		g.Health = Worst(g.Health, ns.Health)
	}
	for _, g := range groups {
		sort.Slice(g.Namespaces, func(i, j int) bool { return g.Namespaces[i].Name < g.Namespaces[j].Name })
	}
	for _, p := range in.Cluster.Pods {
		if p.Node == "" || p.Phase == "Succeeded" || p.Phase == "Failed" {
			continue
		}
		if g, ok := groups[groupOf[p.Namespace]]; ok {
			g.PodsByNode[p.Node]++
		}
	}
	for _, g := range []model.Group{model.GroupPlatform, model.GroupApps, model.GroupTenants} {
		t.Groups = append(t.Groups, *groups[g])
	}
	return t
}
