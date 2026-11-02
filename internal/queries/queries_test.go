package queries

import (
	"errors"
	"testing"
	"time"
)

// dash builds no series itself: the kubernetes plugin owns the node and pod series, and the
// others belong to their plugins.
func TestSeriesRejectsEveryName(t *testing.T) {
	a := Args{VMID: "2020", Instance: "node-a", Namespace: "homepage", Pod: "homepage-abc"}
	for _, name := range []string{"nope", "node.cpu", "node.mem", "pod.cpu", "pod.mem", "host.power", "host.cpu"} {
		if _, err := Series(name, a); !errors.Is(err, ErrBadSeries) {
			t.Errorf("%s: err = %v, want ErrBadSeries", name, err)
		}
	}
}

func TestRange(t *testing.T) {
	tests := []struct {
		name         string
		window, step time.Duration
		ok           bool
	}{
		{"15m", 15 * time.Minute, 15 * time.Second, true},
		{"1h", time.Hour, 30 * time.Second, true},
		{"6h", 6 * time.Hour, 2 * time.Minute, true},
		{"24h", 24 * time.Hour, 5 * time.Minute, true},
		{"7d", 7 * 24 * time.Hour, 30 * time.Minute, true},
		{"2d", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tc := range tests {
		w, s, ok := Range(tc.name)
		if w != tc.window || s != tc.step || ok != tc.ok {
			t.Errorf("Range(%q) = %v %v %v", tc.name, w, s, ok)
		}
	}
}
