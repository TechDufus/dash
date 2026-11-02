package topics

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// noEndpoints is what the Manager sees while the gatus plugin is not running (disabled,
// unconfigured or failed to start): it knows no check, never changes, and keeps the gatus chip on
// the pages that show its data, reading "not configured".
type noEndpoints struct{}

// noGatus is the one stub, so its identity never differs between two lookups.
var noGatus = &noEndpoints{}

func (*noEndpoints) ByHost(string) (model.Health, bool) { return "", false }
func (*noEndpoints) ByName(string) (up, known bool)     { return false, false }
func (*noEndpoints) Revision() uint64                   { return 0 }
func (*noEndpoints) SourceStatuses() []model.SourceStatus {
	return []model.SourceStatus{{Name: "gatus", Error: upstream.CategoryNotConfigured, CheckedAt: time.Now().UTC()}}
}

var _ svc.EndpointHealth = (*noEndpoints)(nil)

// lookupEndpoints is the gatus plugin's service with its identity (what Supervisor.Service
// returned, which differs after every restart of the plugin), or the stub and false while the
// plugin does not run.
func (m *Manager) lookupEndpoints() (id any, ep svc.EndpointHealth, running bool) {
	if m.d.Services != nil {
		if v, ok := m.d.Services("gatus"); ok {
			if ep, ok := v.(svc.EndpointHealth); ok {
				return v, ep, true
			}
		}
	}
	return noGatus, noGatus, false
}

// endpoints is the gatus plugin's svc.EndpointHealth while it runs, otherwise a stub that knows no
// check and reports the gatus source as not configured. It is looked up per call: the plugin
// restarts on a reload and its service changes with it.
func (m *Manager) endpoints() svc.EndpointHealth {
	_, ep, _ := m.lookupEndpoints()
	return ep
}

// endpointsReady is the apps gate: the gatus plugin is absent, or its first poll has finished
// (Revision is above 0 then, whether the poll succeeded or not).
func (m *Manager) endpointsReady() bool {
	_, ep, running := m.lookupEndpoints()
	return !running || ep.Revision() > 0
}

// watchEndpoints marks apps and the machines when the gatus service or its Revision differs from the last
// round's. A plugin cannot mark the Manager's topics, and a callback registered once would not
// survive a reload (which builds a new service), so the Manager compares on its own tick. It
// runs at the top of rebuild, ahead of the early return for a quiet round.
func (m *Manager) watchEndpoints() {
	id, ep, _ := m.lookupEndpoints()
	rev := ep.Revision()
	if id == m.seenEndpoints && rev == m.seenRevision {
		return
	}
	m.seenEndpoints, m.seenRevision = id, rev
	m.mark(model.TopicApps, model.TopicHardwareMachines)
}
