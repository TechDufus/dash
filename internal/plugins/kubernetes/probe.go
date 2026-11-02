package kubernetes

import (
	"time"

	"github.com/TechDufus/dash/internal/kube"
)

// probeState is what the API-server probe knows.
type probeState struct {
	failingSince time.Time     // when the probe began to fail; zero while it succeeds
	cat          string        // the category of the last failure
	outage       time.Duration // the time of the finished outages, never reset: see downForLocked
}

// clockMark is a moment of the plugin's life and the API-server down-time it had counted by then
// (downForLocked). A timeout that must count only the time the API server answered starts from
// a mark (see upSince).
type clockMark struct {
	at   time.Time
	down time.Duration
}

// downForLocked is how long the API server has not answered since the plugin started: the
// finished outages and the one in progress, while the probe is failing (not the debounced
// isUnavailable). It never decreases. The caller holds r.mu.
func (r *runtime) downForLocked(now time.Time) time.Duration {
	d := r.probe.outage
	if !r.probe.failingSince.IsZero() {
		d += now.Sub(r.probe.failingSince)
	}
	return d
}

// markNowLocked is the mark of now. The caller holds r.mu.
func (r *runtime) markNowLocked(now time.Time) clockMark {
	return clockMark{at: now, down: r.downForLocked(now)}
}

// upSince is the time the API server has been answering since m: the wall time since m less the
// down-time since then. The clock pauses (it never resets) while the probe fails, so a timeout
// of a list that never completes counts only the time the list had a chance to complete, and the
// first round after an outage does not judge it. It never decreases.
func (r *runtime) upSince(now time.Time, m clockMark) time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return now.Sub(m.at) - (r.downForLocked(now) - m.down)
}

// probeLoop asks the API server for its version every t.probe.
func (r *runtime) probeLoop() { r.every(r.t.probe, r.probeOnce) }

// probeOnce runs one probe. The first success after failures ends any unavailability at once: the
// debounce is on the way down only.
func (r *runtime) probeOnce() {
	err := kube.Probe(r.ctx, r.cl.Client)
	if r.ctx.Err() != nil {
		return
	}
	now := r.h.Now()
	r.mu.Lock()
	was := r.probe
	retry := r.disc.retry
	if err == nil {
		r.probe = probeState{outage: r.downForLocked(now)}
	} else {
		r.probe.cat = kube.Category(err)
		if r.probe.failingSince.IsZero() {
			r.probe.failingSince = now
		}
	}
	r.mu.Unlock()
	if err == nil && !was.failingSince.IsZero() && retry {
		// The API server answers again: do not wait for the backoff of a discovery that failed
		// while it did not.
		select {
		case r.rediscover <- struct{}{}:
		default:
		}
	}
	switch {
	case err == nil && !was.failingSince.IsZero():
		r.changed("probe", "")
		r.h.Logger().Info("the API server answers again", "failedFor", now.Sub(was.failingSince).Round(time.Second).String())
	case err != nil && r.changed("probe", kube.Category(err)):
		r.h.Logger().Warn("the API server does not answer", "category", kube.Category(err), "error", r.h.ScrubLog(err.Error()))
	}
}

// isUnavailable reports that the probe has failed continuously for t.unavailableAfter.
func (r *runtime) isUnavailable(now time.Time) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.probe.failingSince.IsZero() && now.Sub(r.probe.failingSince) >= r.t.unavailableAfter
}
