package health

import (
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

var testNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestRatioBoundaries(t *testing.T) {
	tests := []struct {
		ratio float64
		want  model.Health
	}{
		{0.0, model.HealthOK}, {0.79, model.HealthOK}, {0.80, model.HealthWarn},
		{0.89, model.HealthWarn}, {0.90, model.HealthCrit}, {1.0, model.HealthCrit},
	}
	for _, tc := range tests {
		if got := RatioHealth(tc.ratio); got != tc.want {
			t.Errorf("RatioHealth(%v) = %s, want %s", tc.ratio, got, tc.want)
		}
	}
}

func TestCertBoundaries(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		name string
		left time.Duration
		want model.Health
	}{
		{"30 days", 30 * day, model.HealthOK},
		{"just over 21 days", 21*day + time.Second, model.HealthOK},
		{"exactly 21 days", 21 * day, model.HealthOK},
		{"just under 21 days", 21*day - time.Second, model.HealthWarn},
		{"exactly 7 days", 7 * day, model.HealthWarn},
		{"just under 7 days", 7*day - time.Second, model.HealthCrit},
		{"expired", -day, model.HealthCrit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CertHealth(testNow.Add(tc.left), testNow); got != tc.want {
				t.Errorf("CertHealth = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPodAndWorkloadHealth(t *testing.T) {
	created := testNow.Add(-10 * time.Minute)
	podTests := []struct {
		name    string
		phase   string
		waiting []string
		created time.Time
		want    model.Health
	}{
		{"running", "Running", nil, created, model.HealthOK},
		{"crashloop", "Running", []string{"CrashLoopBackOff"}, created, model.HealthCrit},
		{"imagepull", "Pending", []string{"ImagePullBackOff"}, testNow, model.HealthCrit},
		{"errimagepull", "Pending", []string{"ErrImagePull"}, testNow, model.HealthCrit},
		{"config error", "Pending", []string{"CreateContainerConfigError"}, testNow, model.HealthCrit},
		{"create error", "Pending", []string{"CreateContainerError"}, testNow, model.HealthCrit},
		{"container creating is fine", "Pending", []string{"ContainerCreating"}, testNow.Add(-time.Minute), model.HealthOK},
		{"pending 5m exactly", "Pending", nil, testNow.Add(-5 * time.Minute), model.HealthOK},
		{"pending over 5m", "Pending", nil, testNow.Add(-5*time.Minute - time.Second), model.HealthWarn},
		{"completed job", "Succeeded", nil, created, model.HealthOK},
	}
	for _, tc := range podTests {
		if got := PodHealth(tc.phase, tc.waiting, tc.created, testNow); got != tc.want {
			t.Errorf("pod %s: %s, want %s", tc.name, got, tc.want)
		}
	}
	wlTests := []struct {
		ready, desired int
		want           model.Health
	}{
		{1, 1, model.HealthOK}, {0, 0, model.HealthOK}, {3, 3, model.HealthOK},
		{1, 3, model.HealthWarn}, {0, 1, model.HealthCrit}, {0, 3, model.HealthCrit},
	}
	for _, tc := range wlTests {
		if got := WorkloadHealth(tc.ready, tc.desired); got != tc.want {
			t.Errorf("workload %d/%d: %s, want %s", tc.ready, tc.desired, got, tc.want)
		}
	}
}
