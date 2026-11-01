package prometheus_test

import (
	"context"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
)

// TestDetailsResolve is the fan-series assertion of internal/demo's TestDetailsResolve, now that
// the series come from this plugin's twin.
func TestDemoTwinDrawsTheFanSeries(t *testing.T) {
	// No URL: in demo mode the twin runs enabled and configured all the same.
	q := start(t, &pluginconf.Prometheus{}, true).querier(t)
	end := time.Now()
	lines, err := q.Range(context.Background(), `ipmi_fan_speed_rpm{nodename="pve-demo"}`, end.Add(-time.Hour), end, 30*time.Second)
	if err != nil || len(lines) != 6 || len(lines[0].Points) < 100 {
		t.Fatalf("fan series: %d lines, err %v", len(lines), err)
	}
}

func TestDemoTwinAnswersInstantQueries(t *testing.T) {
	q := start(t, &pluginconf.Prometheus{}, true).querier(t)
	got, err := q.Instant(context.Background(), `ipmi_power_watts{nodename="pve-demo"}`)
	if err != nil || len(got) != 1 || got[0].Value <= 0 || got[0].Labels == nil {
		t.Fatalf("Instant = %+v, %v", got, err)
	}
	fans, err := q.Instant(context.Background(), `ipmi_fan_speed_rpm`)
	if err != nil || len(fans) != 6 {
		t.Fatalf("fans = %d samples, %v", len(fans), err)
	}
}

func TestDemoTwinKeepsTheManifestAndIsHealthy(t *testing.T) {
	real := prometheus.New()
	twin := real.(plugin.Demo).Demo()
	a, b := real.Manifest(), twin.Manifest()
	if a.ID != b.ID || a.Name != b.Name || a.Version != b.Version || a.Description != b.Description {
		t.Errorf("twin manifest %+v differs from %+v", b, a)
	}
	if st := twin.Status(); st.State != plugin.StateHealthy {
		t.Errorf("twin status = %+v", st)
	}
	e := start(t, &pluginconf.Prometheus{}, true)
	e.querier(t)
	if state, _ := e.row(t); state != "healthy" {
		t.Errorf("demo row = %s, want healthy", state)
	}
}

// Without Options.Demo the same instance with no URL is disabled: the twin is only for demo.
func TestRealPluginWithoutURLIsNotADemo(t *testing.T) {
	e := start(t, &pluginconf.Prometheus{}, false)
	if _, ok := e.sup.Service(prometheus.ID); ok {
		t.Error("served without a URL outside demo mode")
	}
}
