package world

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// fakeLinks is a LinkSource that records nothing and links to example.com, so the world builds
// without the production link builder.
type fakeLinks struct{}

func (fakeLinks) Headlamp(kind, namespace, name string) []model.Link {
	return []model.Link{{Label: "Headlamp", Href: "https://headlamp.example.com/" + kind + "/" + namespace + "/" + name}}
}

func (fakeLinks) VMConsole(vmid int, name string) []model.Link {
	return []model.Link{{Label: "Console " + name, Href: "https://pve.example.com/console"}}
}

var (
	testStart = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	testNow   = time.Date(2026, 1, 3, 10, 20, 30, 0, time.UTC)
)

func testWorld() *World {
	return New(func() time.Time { return testNow }, testStart, fakeLinks{})
}

// Every topic builder yields the same payload for the same clock reading, in every call: the
// twins and the legacy source rebuild on each tick and must not disagree with one another.
func TestBuildersAreDeterministic(t *testing.T) {
	builders := map[string]func(w *World) any{
		"alerts":   func(w *World) any { return w.Alerts() },
		"status":   func(w *World) any { return w.Status() },
		"host":     func(w *World) any { return w.Host() },
		"vms":      func(w *World) any { return w.VMs() },
		"cluster":  func(w *World) any { return w.Cluster() },
		"storage":  func(w *World) any { return w.Storage() },
		"apps":     func(w *World) any { return w.Apps() },
		"flux":     func(w *World) any { return w.Flux() },
		"overview": func(w *World) any { return w.Overview() },
		"logs":     func(w *World) any { return w.Logs("web-1", "app") },
		"build":    func(w *World) any { return w.Build() },
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			a, b := build(testWorld()), build(testWorld())
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("two builds at the same instant differ:\n%+v\n%+v", a, b)
			}
		})
	}
	if a, b := Launcher(), Launcher(); !reflect.DeepEqual(a, b) {
		t.Error("launcher differs between calls")
	}
	from, to := testNow.Add(-time.Hour), testNow
	if a, b := Series("ipmi_fan_speed_rpm", from, to, time.Minute), Series("ipmi_fan_speed_rpm", from, to, time.Minute); !reflect.DeepEqual(a, b) {
		t.Error("series differs between calls")
	}
}

// The overview summarises the other topics, so a snapshot's overview equals the one built alone.
func TestOverviewAgreesWithSnapshot(t *testing.T) {
	w := testWorld()
	if got, want := w.Overview(), w.Build().Overview; !reflect.DeepEqual(got, want) {
		t.Fatalf("overview alone differs from the snapshot's:\n%+v\n%+v", got, want)
	}
}

// The fixtures drift with the clock, so the snapshot must move when time does.
func TestBuildFollowsTheClock(t *testing.T) {
	now := testNow
	w := New(func() time.Time { return now }, testStart, fakeLinks{})
	before := w.Host()
	now = now.Add(6 * time.Hour)
	if reflect.DeepEqual(before, w.Host()) {
		t.Fatal("host did not change after the clock moved")
	}
}

// Wave stays within [-1, 1] and Around within ±spread, for any key and instant.
func TestWaveAndAroundAreBounded(t *testing.T) {
	keys := []string{"", "cpu", "pkg0", strings.Repeat("k", 300), "ünï", "acme/web-7d9c5b8f4-0a1b2cpu"}
	for _, key := range keys {
		for i := range 2000 {
			at := testStart.Add(time.Duration(i) * 37 * time.Minute)
			if w := Wave(key, at); w < -1 || w > 1 {
				t.Fatalf("Wave(%q, %v) = %v outside [-1, 1]", key, at, w)
			}
			const base, spread = 100.0, 25.0
			if v := Around(key, at, base, spread); v < base-spread || v > base+spread {
				t.Fatalf("Around(%q, %v) = %v outside %v±%v", key, at, v, base, spread)
			}
		}
	}
	at := testNow
	if Around("k", at, 7, 0) != 7 {
		t.Error("a zero spread must return the base")
	}
	if Wave("k", at) != Wave("k", at) {
		t.Error("Wave is not repeatable")
	}
}

// The world is one homelab: the pieces that name each other agree.
func TestWorldIsCoherent(t *testing.T) {
	s := testWorld().Build()
	nodes := map[string]bool{}
	for _, n := range s.Cluster.Nodes {
		nodes[n.Name] = true
	}
	for _, vm := range s.VMs.VMs {
		if vm.K8sNode != nil && !nodes[vm.K8sNode.Name] {
			t.Errorf("VM %s points at node %s, which the cluster does not have", vm.Name, vm.K8sNode.Name)
		}
	}
	crash := crashPodName()
	found := false
	for _, p := range s.Cluster.Pods {
		found = found || p.Name == crash && p.Health == model.HealthCrit
	}
	if !found {
		t.Errorf("crash-looping pod %q is not in the cluster payload", crash)
	}
	if len(s.Overview.Attention) == 0 || !strings.Contains(s.Overview.Attention[0].ID, crash) {
		t.Error("the overview does not report the crash-looping pod")
	}
	if s.Host.Name != HostName {
		t.Errorf("host name = %q, want %q", s.Host.Name, HostName)
	}
}
