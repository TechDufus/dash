package world

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/svc"
)

// Series answers a range query with generated lines shaped like the real query's result: the
// metric family is picked by a keyword of expr, and every point drifts around a fixed base.
func Series(expr string, start, end time.Time, step time.Duration) []model.SeriesLine {
	type shape struct {
		labels       map[string]string
		base, spread float64
	}
	one := func(base, spread float64) []shape { return []shape{{map[string]string{}, base, spread}} }
	var shapes []shape
	// A series of the second host names its nodename as a literal; the sum over every host (the
	// Overview's power spark) names both in one regular expression.
	spec, all := hostSpecs[0], strings.HasPrefix(expr, "sum(")
	if strings.Contains(expr, `"`+SecondNodeName+`"`) {
		spec = hostSpecs[1]
	}
	switch {
	case strings.Contains(expr, "ipmi_fan_speed_rpm"):
		for i := range spec.fans {
			shapes = append(shapes, shape{map[string]string{"name": fmt.Sprintf("Fan%d", i+1)}, spec.fanBase + float64(i%3)*60, 240})
		}
	case strings.Contains(expr, "ipmi_temperature_celsius"):
		for _, x := range spec.sensors {
			shapes = append(shapes, shape{map[string]string{"name": x.Name}, x.ValueC, 3})
		}
	case strings.Contains(expr, "ipmi_power_watts") && all:
		shapes = one(hostSpecs[0].power+hostSpecs[1].power, hostSpecs[0].powerSpread+hostSpecs[1].powerSpread)
	case strings.Contains(expr, "ipmi_power_watts"):
		shapes = one(spec.power, spec.powerSpread)
	case strings.Contains(expr, "node_hwmon_temp_celsius"):
		shapes = one(spec.pkgs[0], 6)
	case strings.Contains(expr, "pve_cpu_usage_ratio"), strings.Contains(expr, "node_cpu_seconds_total"):
		shapes = one(0.22, 0.12)
	case strings.Contains(expr, "pve_memory"), strings.Contains(expr, "node_memory"):
		shapes = one(0.61, 0.06)
	case strings.Contains(expr, "network_receive"):
		shapes = one(3.2e6, 2.4e6)
	case strings.Contains(expr, "network_transmit"):
		shapes = one(1.4e6, 1.1e6)
	case strings.Contains(expr, "disk_read"):
		shapes = one(8.5e5, 7e5)
	case strings.Contains(expr, "disk_written"):
		shapes = one(2.1e6, 1.6e6)
	case strings.Contains(expr, "container_cpu_usage"):
		shapes = one(0.045, 0.03)
	case strings.Contains(expr, "container_memory"):
		shapes = one(1.8e8, 2.5e7)
	default:
		shapes = one(0.5, 0.2)
	}
	out := make([]model.SeriesLine, 0, len(shapes))
	for _, sh := range shapes {
		key := expr + fmt.Sprint(sh.labels)
		var pts [][2]float64
		for t := start.Truncate(step); !t.After(end); t = t.Add(step) {
			pts = append(pts, [2]float64{float64(t.Unix()), math.Max(0, Around(key, t, sh.base, sh.spread))})
		}
		out = append(out, model.SeriesLine{Labels: sh.labels, Points: pts})
	}
	return out
}

// Instant answers an instant query the way Series answers a range query: one sample per line,
// the latest point of the line drawn up to now. Labels are copied, so a caller may keep them.
func Instant(expr string, now time.Time) []svc.Sample {
	const step = 15 * time.Second
	lines := Series(expr, now.Add(-step), now, step)
	out := make([]svc.Sample, 0, len(lines))
	for _, l := range lines {
		if len(l.Points) == 0 {
			continue
		}
		labels := make(map[string]string, len(l.Labels))
		for k, v := range l.Labels {
			labels[k] = v
		}
		out = append(out, svc.Sample{Labels: labels, Value: l.Points[len(l.Points)-1][1]})
	}
	return out
}
