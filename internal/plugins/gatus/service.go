package gatus

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// revisions numbers every state change of every service in the process. A reconfigured plugin
// builds a new service (Supervisor.Reload stops the instance and starts a fresh one), and a
// consumer that remembers the last revision it saw would miss a change if the new service
// started counting from 1 again: one counter for the whole process keeps the values of a
// successor above its predecessor's.
var revisions atomic.Uint64

// sourceName is the name of the plugin's source on the envelopes it appears in.
const sourceName = "gatus"

// notPolled is the error of the source before its first poll has finished.
const notPolled = "not polled yet"

// service holds what the last poll found and answers the questions of svc.EndpointHealth. It is
// the plugin's whole state: Start makes one, the poller feeds it, Status, Attention and the
// service interface read it.
type service struct {
	critical []string // plugins.gatus.criticalGroups

	mu     sync.RWMutex
	polled bool                    // a poll has finished, successfully or not
	eps    []model.Endpoint        // the latest successful answer, in Gatus' order, with refs
	src    model.SourceStatus      // the outcome of the latest poll
	byHost map[string]model.Health // lowercased host of an endpoint's URL; the last endpoint wins
	rev    uint64
	sig    string // what the last revision stood for
}

var _ svc.EndpointHealth = (*service)(nil)

func newService(critical []string) *service {
	return &service{critical: critical, byHost: map[string]model.Health{}}
}

// record stores the outcome of one poll: eps when err is nil (a failed poll keeps the endpoints it
// had, as the source status says they may be stale), and the source status either way. It bumps
// Revision when the endpoint list, a health value or the source status differ from what the last
// revision stood for, and always for the first poll, so a Gatus that is down at start still opens
// the consumers' gate. It returns what the topic publishes.
func (s *service) record(eps []model.Endpoint, err error, at time.Time) (model.StatusData, []model.SourceStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.eps = decorate(eps)
		s.byHost = make(map[string]model.Health, len(s.eps))
		for _, e := range s.eps {
			if host := hostOf(e.URL); host != "" {
				s.byHost[host] = EndpointHealth(e.Group, e.Healthy, s.critical)
			}
		}
	}
	s.src = model.SourceStatus{Name: sourceName, OK: err == nil, Error: upstream.Category(err), CheckedAt: at.UTC()}
	if sig := s.signature(); !s.polled || sig != s.sig {
		s.rev, s.sig = revisions.Add(1), sig
	}
	s.polled = true
	return s.data(), []model.SourceStatus{s.src}
}

// signature is a string that differs whenever a consumer of svc.EndpointHealth could see a
// difference: the endpoints (identity, where they probe, whether they pass) and the source
// status without its time.
func (s *service) signature() string {
	var b strings.Builder
	for _, e := range s.eps {
		b.WriteString(e.Key)
		b.WriteByte(0)
		b.WriteString(e.Group)
		b.WriteByte(0)
		b.WriteString(e.Name)
		b.WriteByte(0)
		b.WriteString(e.URL)
		b.WriteByte(0)
		if e.Healthy {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
		b.WriteByte('\n')
	}
	if s.src.OK {
		b.WriteString("ok")
	} else {
		b.WriteString("err:" + s.src.Error)
	}
	return b.String()
}

// data is the status topic's payload. The caller holds s.mu.
func (s *service) data() model.StatusData {
	return model.StatusData{Endpoints: sorted(s.eps), CriticalGroups: append([]string(nil), s.critical...)}
}

// snapshot is a copy of the endpoints in Gatus' order, the source status and whether a poll has
// finished, read under one lock.
func (s *service) snapshot() (eps []model.Endpoint, src model.SourceStatus, polled bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.Endpoint(nil), s.eps...), s.src, s.polled
}

// ByHost implements svc.EndpointHealth: the health of the check whose URL has this host.
func (s *service) ByHost(host string) (model.Health, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.byHost[strings.ToLower(host)]
	return h, ok
}

// ByName implements svc.EndpointHealth. A check with no results yet counts as down, as it
// always has; the last of several checks with the same name wins.
func (s *service) ByName(name string) (up, known bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.eps {
		if strings.EqualFold(e.Name, name) {
			up, known = e.Healthy, true
		}
	}
	return up, known
}

// Revision implements svc.EndpointHealth.
func (s *service) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}

// SourceStatuses implements svc.EndpointHealth; before the first poll the source is reported as
// not polled, which keeps its chip on the pages that show Gatus data.
func (s *service) SourceStatuses() []model.SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.polled {
		return []model.SourceStatus{{Name: sourceName, Error: notPolled, CheckedAt: time.Now().UTC()}}
	}
	return []model.SourceStatus{s.src}
}

// status is the instance's state: starting until the first poll has finished, then healthy, or
// error with the category of the failure.
func (s *service) status() plugin.Status {
	srcs := s.SourceStatuses()
	s.mu.RLock()
	polled, ok, cat := s.polled, s.src.OK, s.src.Error
	s.mu.RUnlock()
	switch {
	case !polled:
		return plugin.Status{State: plugin.StateStarting, Message: "waiting for the first poll", Sources: srcs}
	case !ok:
		return plugin.Status{State: plugin.StateError, Message: cat, Sources: srcs}
	}
	return plugin.Status{State: plugin.StateHealthy, Sources: srcs}
}

// decorate returns eps with a ref on each (design L.6). A check Gatus reported without a key gets
// the group_name form Gatus itself builds keys in. The input is not changed.
func decorate(eps []model.Endpoint) []model.Endpoint {
	out := make([]model.Endpoint, len(eps))
	for i, e := range eps {
		if e.Key == "" {
			e.Key = e.Group + "_" + e.Name
		}
		e.Ref = plugin.Ref(pluginID, "Endpoint", e.Key)
		out[i] = e
	}
	return out
}
