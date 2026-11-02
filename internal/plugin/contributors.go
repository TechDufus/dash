package plugin

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

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
	// overview publisher. A panic in Status gives none.
	Sources func() []model.SourceStatus
}

// Contributors lists, in registry order, the running instances that implement at least one of
// AttentionSource, KPISource and TopologySource. The core composes core.overview from them
// (design A.7); an instance that is not running (disabled, unconfigured, blocked, failed to
// start) contributes nothing, and so has no source chip on the Overview. A running instance
// whose row is hidden (only unavailable can be: its data describes something nobody can reach)
// keeps its Sources and has Attention, KPIs and Topology nil, so what it last built is not shown.
func (s *Supervisor) Contributors() []Contributor {
	var out []Contributor
	for _, in := range s.registry() {
		in.mu.RLock()
		running, p, gen := in.running, in.p, in.gen
		in.mu.RUnlock()
		if !running || p == nil {
			continue
		}
		c := Contributor{ID: in.id}
		c.Attention = stampedOf(p, in, gen)
		c.KPIs, _ = p.(KPISource)
		c.Topology, _ = p.(TopologySource)
		if c.Attention != nil || c.KPIs != nil || c.Topology != nil {
			c.Sources = sourcesOf(p)
			if st, _ := s.liveState(in); !Visible(st) {
				c.Attention, c.KPIs, c.Topology = nil, nil, nil
			}
			out = append(out, c)
		}
	}
	return out
}

// stamped is an AttentionSource that gives the items of src that have no Since the time they
// were first seen, by the instance's tracker. It belongs to one run of the instance (gen): an
// overview rebuild can hold it across a disable, and a late call must not write into the tracker
// that the disable reset, or the next run's items would carry the time of the disable.
type stamped struct {
	src AttentionSource
	in  *instance
	gen uint64
}

// stampedOf wraps p, the plugin of in's run gen, when it is an AttentionSource, and is nil
// otherwise.
func stampedOf(p Plugin, in *instance, gen uint64) AttentionSource {
	src, ok := p.(AttentionSource)
	if !ok {
		return nil
	}
	return stamped{src: src, in: in, gen: gen}
}

// Attention returns nil, and leaves the tracker alone, while the plugin has no opinion yet (a nil
// list) or its run is over; any other list, an empty one included, is stamped, which prunes the
// sightings of items that are gone. src runs outside the instance's lock; the tracker is written
// under its read lock, which excludes the reset (setNotRunning and a failed start hold it for
// writing), after checking that the instance still runs the same generation.
func (s stamped) Attention(now time.Time) []model.AttentionItem {
	items := s.src.Attention(now)
	if items == nil {
		return nil
	}
	s.in.mu.RLock()
	defer s.in.mu.RUnlock()
	if !s.in.running || s.in.gen != s.gen {
		return nil
	}
	return s.in.seen.Stamp(now, items)
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
