package kubernetes

import (
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// A workload bundle reads the logs of its worst pod: the worst health first, then the most
// restarts.
func TestWorsePodIsTheWorstHealthThenTheMostRestarts(t *testing.T) {
	crit := model.Pod{Name: "c", Health: model.HealthCrit, Restarts: 1}
	warn := model.Pod{Name: "w", Health: model.HealthWarn, Restarts: 9}
	unknown := model.Pod{Name: "u", Health: model.HealthUnknown, Restarts: 50}
	critMore := model.Pod{Name: "c2", Health: model.HealthCrit, Restarts: 2}
	for _, tc := range []struct {
		a, b model.Pod
		want bool
	}{
		{crit, warn, true}, {warn, crit, false}, {warn, unknown, true}, {unknown, warn, false},
		{critMore, crit, true}, {crit, critMore, false}, {crit, crit, false},
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
