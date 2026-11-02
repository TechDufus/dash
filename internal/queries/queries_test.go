package queries

import (
	"testing"
	"time"
)

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
