package plugin

import "github.com/TechDufus/dash/internal/model"

// Contributor is a running instance and the overview sources it implements; each is nil when the
// plugin does not implement it.
type Contributor struct {
	ID        ID
	Attention AttentionSource
	KPIs      KPISource
	Topology  TopologySource
	// Sources reports the instance's own source health (its Status().Sources) as it is now; the
	// overview merges them, deduplicated by name, into core.overview.sources, so a dead upstream
	// shows on the Overview panels even though the plugin's data no longer goes through the
	// topic Manager. A panic in Status gives none.
	Sources func() []model.SourceStatus
}

// Contributors lists, in registry order, the running instances that implement at least one of
// AttentionSource, KPISource and TopologySource. The core composes core.overview from them
// (design A.7); an instance that is not running (disabled, unconfigured, blocked, failed to
// start) contributes nothing, and so has no source chip on the Overview.
func (s *Supervisor) Contributors() []Contributor {
	var out []Contributor
	for _, in := range s.registry() {
		in.mu.RLock()
		running, p := in.running, in.p
		in.mu.RUnlock()
		if !running || p == nil {
			continue
		}
		c := Contributor{ID: in.id}
		c.Attention, _ = p.(AttentionSource)
		c.KPIs, _ = p.(KPISource)
		c.Topology, _ = p.(TopologySource)
		if c.Attention != nil || c.KPIs != nil || c.Topology != nil {
			c.Sources = sourcesOf(p)
			out = append(out, c)
		}
	}
	return out
}

// sourcesOf is the Sources function of a plugin: a copy of its Status().Sources, none when Status
// panics (liveState reports that as the instance's error).
func sourcesOf(p Plugin) func() []model.SourceStatus {
	return func() []model.SourceStatus {
		var st Status
		if rec, _ := protect(func() { st = p.Status() }); rec != nil {
			return nil
		}
		return append([]model.SourceStatus(nil), st.Sources...)
	}
}
