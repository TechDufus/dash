package kubernetes

import (
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// A workload bundle reads the logs of its worst pod: one that started before a Pending pod that
// never did (it has no logs), then the worst health, then the most restarts.
func TestWorsePodStartedThenTheWorstHealthThenTheMostRestarts(t *testing.T) {
	crit := model.Pod{Name: "c", Phase: "Running", Health: model.HealthCrit, Restarts: 1}
	warn := model.Pod{Name: "w", Phase: "Running", Health: model.HealthWarn, Restarts: 9}
	unknown := model.Pod{Name: "u", Phase: "Running", Health: model.HealthUnknown, Restarts: 50}
	critMore := model.Pod{Name: "c2", Phase: "Running", Health: model.HealthCrit, Restarts: 2}
	// Never started: Pending, nothing ready, no restarts (an image that never pulled).
	pending := model.Pod{Name: "p", Phase: "Pending", Health: model.HealthCrit}
	pendingRestarted := model.Pod{Name: "pr", Phase: "Pending", Health: model.HealthWarn, Restarts: 1}
	pendingReady := model.Pod{Name: "py", Phase: "Pending", Health: model.HealthWarn, ReadyContainers: 1}
	failed := model.Pod{Name: "f", Phase: "Failed", Health: model.HealthWarn}
	succeeded := model.Pod{Name: "s", Phase: "Succeeded", Health: model.HealthWarn}
	running := model.Pod{Name: "r", Phase: "Running", Health: model.HealthWarn} // nothing ready yet
	for _, tc := range []struct {
		a, b model.Pod
		want bool
	}{
		{crit, warn, true}, {warn, crit, false}, {warn, unknown, true}, {unknown, warn, false},
		{critMore, crit, true}, {crit, critMore, false}, {crit, crit, false},
		{warn, pending, true}, {pending, warn, false},
		{pendingRestarted, pending, true}, {pending, pendingRestarted, false},
		{pendingReady, pending, true}, {failed, pending, true}, {pending, failed, false},
		{succeeded, pending, true}, {running, pending, true},
	} {
		if got := worse(tc.a, tc.b); got != tc.want {
			t.Errorf("worse(%s, %s) = %v, want %v", tc.a.Name, tc.b.Name, got, tc.want)
		}
	}
	p := model.Pod{Health: model.HealthCrit, Phase: "Running", Reason: "CrashLoopBackOff", ReadyContainers: 0, TotalContainers: 2, Restarts: 7}
	if got := podSummary(p); got != "health crit, phase Running (CrashLoopBackOff), ready 0/2, restarts 7" {
		t.Errorf("podSummary = %q", got)
	}
}
