package kubernetes

import (
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/plugin"
)

// cutProbeFor cuts the API server's /version for d of the plugin's clock and restores it, and
// returns once the plugin has seen the probe answer again.
func (e *env) cutProbeFor(d time.Duration) {
	e.t.Helper()
	e.failProbe.Store(true)
	e.probeFailing()
	e.clock.advance(d)
	e.failProbe.Store(false)
	e.probeAnswering()
}

// The sync timeouts count the time the API server answered. A list that is refused is judged
// after 60 s of up-time, not 60 s of wall time that an outage may have used up: the first round
// after the outage must not publish the topic without the resource and degrade the row.
func TestRefusedListIsJudgedAfterSixtySecondsOfAPIServerUptime(t *testing.T) {
	e := newEnv(t, envOpts{
		fakeClock: true,
		prefill: func(e *env) {
			e.dc.PrependReactor("list", "certificates", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "certificates"}, "", errors.New("no"))
			})
		},
	})
	r := e.runtime()
	published := func() bool { _, ok := e.hub.Latest("", "kubernetes.cluster"); return ok }
	waitFor(t, "the storage topic, which does not wait for certificates", func() bool {
		_, ok := e.hub.Latest("", "kubernetes.storage")
		return ok
	})

	e.clock.advance(30 * time.Second) // 30 s of up-time
	e.cutProbeFor(90 * time.Second)   // 120 s of wall time, still 30 s of up-time
	if r.stuck(kube.GVRCertificates) {
		t.Fatal("the list counts as refused after 30 s of up-time and a 90 s outage")
	}
	if published() {
		t.Fatal("kubernetes.cluster was published without the certificates after the outage")
	}
	if st := e.info().State; st == plugin.StateDegraded || st == plugin.StateError {
		t.Fatalf("state = %s after the outage, want the row to wait for the verdict", st)
	}

	e.clock.advance(30 * time.Second) // 60 s of up-time
	waitFor(t, "kubernetes.cluster after 60 s of up-time", published)
	pi := e.state(plugin.StateDegraded)
	if want := "cannot list: cert-manager (auth)"; !strings.Contains(pi.Message, want) {
		t.Errorf("message = %q, want %q", pi.Message, want)
	}

	// Monotone: a list that is stuck stays stuck through a later outage.
	e.cutProbeFor(90 * time.Second)
	if !r.stuck(kube.GVRCertificates) {
		t.Error("a refused list un-stuck itself during an outage")
	}
}

// The typed informers' sync timeout counts up-time too: an outage right after the start does not
// make the row an error the moment the API server is back.
func TestTypedSyncTimeoutCountsAPIServerUptime(t *testing.T) {
	var forbid atomicBool
	forbid.set(true)
	e := newEnv(t, envOpts{fakeClock: true, prefill: func(e *env) {
		e.kc.PrependReactor("list", "pods", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
			if forbid.get() {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no"))
			}
			return false, nil, nil
		})
	}})
	r := e.runtime()
	waitFor(t, "the watch error", func() bool { return r.cl.SyncError() != nil })

	e.clock.advance(30 * time.Second)
	e.cutProbeFor(90 * time.Second)
	if st := e.info().State; st != plugin.StateStarting {
		t.Fatalf("state = %s after 30 s of up-time and a 90 s outage, want starting", st)
	}
	e.clock.advance(30 * time.Second)
	if pi := e.state(plugin.StateError); !strings.Contains(pi.Message, "auth") {
		t.Errorf("message = %q, want the category auth after 60 s of up-time", pi.Message)
	}
}

// upSince is the wall time less the down-time since the mark; a mark taken during an outage
// starts counting when the API server answers again; the finished outages are never forgotten.
func TestUpSinceCountsOnlyTheTimeTheAPIServerAnswered(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	r := &runtime{}

	// Up for 10 s, then the probe fails.
	r.probe.failingSince = at(10)
	r.mu.Lock()
	mid := r.markNowLocked(at(20)) // a resource that appears 10 s into the outage
	r.mu.Unlock()
	for _, c := range []struct {
		name string
		now  int
		m    clockMark
		want time.Duration
	}{
		{"from the start, before the outage", 10, clockMark{at: at(0)}, 10 * time.Second},
		{"from the start, paused during the outage", 50, clockMark{at: at(0)}, 10 * time.Second},
		{"the mark made during the outage, still failing", 50, mid, 0},
	} {
		if got := r.upSince(at(c.now), c.m); got != c.want {
			t.Errorf("%s: up-time = %v, want %v", c.name, got, c.want)
		}
	}

	// The probe answers again at 50 s: the outage (40 s) is booked and never reset.
	r.mu.Lock()
	r.probe = probeState{outage: r.downForLocked(at(50))}
	r.mu.Unlock()
	for _, c := range []struct {
		name string
		now  int
		m    clockMark
		want time.Duration
	}{
		{"from the start, after the outage", 80, clockMark{at: at(0)}, 40 * time.Second},
		{"the mark made during the outage", 80, mid, 30 * time.Second},
		{"a mark made after the outage", 80, clockMark{at: at(60), down: 40 * time.Second}, 20 * time.Second},
	} {
		if got := r.upSince(at(c.now), c.m); got != c.want {
			t.Errorf("%s: up-time = %v, want %v", c.name, got, c.want)
		}
	}

	// A second outage adds to the first.
	r.probe.failingSince = at(90)
	if got := r.upSince(at(100), clockMark{at: at(0)}); got != 50*time.Second {
		t.Errorf("up-time during a second outage = %v, want 50s", got)
	}
}
