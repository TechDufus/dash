package hardware

import (
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// The CPU package rule at the edges of the default thresholds and of custom ones: from the
// threshold up, not above it.
func TestTempHealthBoundaries(t *testing.T) {
	cases := []struct {
		warn, crit int
		c          float64
		want       model.Health
	}{
		{85, 90, 0, model.HealthOK},
		{85, 90, 84.9, model.HealthOK},
		{85, 90, 85, model.HealthWarn},
		{85, 90, 89.9, model.HealthWarn},
		{85, 90, 90, model.HealthCrit},
		{85, 90, 120, model.HealthCrit},
		{70, 80, 69.9, model.HealthOK},
		{70, 80, 70, model.HealthWarn},
		{70, 80, 79.9, model.HealthWarn},
		{70, 80, 80, model.HealthCrit},
	}
	for _, c := range cases {
		if got := tempHealth(c.warn, c.crit, c.c); got != c.want {
			t.Errorf("tempHealth(%d, %d, %v) = %s, want %s", c.warn, c.crit, c.c, got, c.want)
		}
	}
}

func TestWorstAndMaxOf(t *testing.T) {
	if got := worst(); got != model.HealthOK {
		t.Errorf("worst() = %s", got)
	}
	if got := worst(model.HealthOK, model.HealthWarn, model.HealthOK); got != model.HealthWarn {
		t.Errorf("worst = %s", got)
	}
	if got := worst(model.HealthWarn, model.HealthCrit, model.HealthUnknown); got != model.HealthCrit {
		t.Errorf("worst = %s", got)
	}
	if got := worst(model.HealthOK, model.HealthUnknown); got != model.HealthUnknown {
		t.Errorf("worst = %s", got)
	}
	if maxOf(nil) != 0 || maxOf([]float64{3, 9.5, 2}) != 9.5 {
		t.Error("maxOf")
	}
}
