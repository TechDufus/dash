package hardware

import (
	"errors"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/queries"
)

// The host.* series keep their names and their expressions.
func TestSeriesNamesAndExpressions(t *testing.T) {
	m := newPlugin(fastTiming()).Manifest()
	var names []string
	for _, s := range m.Series {
		names = append(names, s.Name)
	}
	if want := []string{"host.power", "host.cpuTemp", "host.cpu", "host.mem", "host.temps", "host.fans"}; !slices.Equal(names, want) {
		t.Errorf("series = %v, want %v", names, want)
	}
	x := defaultExprs()
	for name, want := range map[string]string{
		"host.power":   x.power("n"),
		"host.cpuTemp": x.cpuTempMax("n"),
		"host.cpu":     x.cpu("n"),
		"host.mem":     x.mem("n"),
		"host.temps":   x.sensors("n"),
		"host.fans":    x.fans("n"),
	} {
		if got, err := hostSeries(x, name, "n"); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
	if _, err := hostSeries(x, "host.nope", "n"); !errors.Is(err, queries.ErrBadSeries) {
		t.Errorf("an unknown name: %v", err)
	}
}

// Before Start there is no state to answer from.
func TestSeriesBeforeStartAreBadRequests(t *testing.T) {
	p := newPlugin(fastTiming())
	for _, s := range p.Manifest().Series {
		if _, err := s.Expr(plugin.SeriesArgs{}); !errors.Is(err, queries.ErrBadSeries) {
			t.Errorf("%s before Start: %v, want ErrBadSeries", s.Name, err)
		}
	}
}

// A running plugin answers with the nodename of the moment: the configured one, else the Proxmox
// node as soon as it is known; with no host or no node it is a bad request, never an expression
// that matches nothing.
func TestSeriesFollowTheNodename(t *testing.T) {
	r := newRig(t, rigOpts{cfg: withHost(pluginconf.HardwareHost{}), nodeUnknown: true})
	r.q.fillHost(r.ex, node, "")
	spec, id, ok := r.sup.Series("host.cpu")
	if !ok || id != ID {
		t.Fatalf("host.cpu is not registered through the running plugin: %v %v", id, ok)
	}
	if _, err := spec.Expr(plugin.SeriesArgs{}); !errors.Is(err, queries.ErrBadSeries) {
		t.Fatalf("the node is unknown: %v, want ErrBadSeries", err)
	}
	r.node.set(node, true)
	got, err := spec.Expr(plugin.SeriesArgs{})
	if err != nil || got != r.ex.cpu(node) {
		t.Errorf("once the node is known = %q (%v), want %q", got, err, r.ex.cpu(node))
	}
}

func TestSeriesUseTheConfiguredNodenameAndSelectors(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Hardware) {
		c.Hosts = []pluginconf.HardwareHost{{Nodename: "box-7"}}
		c.Selectors.HwmonChip = "k10temp_.*"
	}})
	spec, _, _ := r.sup.Series("host.cpuTemp")
	got, err := spec.Expr(plugin.SeriesArgs{})
	want := `max(node_hwmon_temp_celsius{nodename="box-7",chip=~"k10temp_.*",sensor="temp1"})`
	if err != nil || got != want {
		t.Errorf("host.cpuTemp = %q (%v), want %q", got, err, want)
	}
}

// With no host configured (machines only) the host series are not available.
func TestSeriesWithoutAHostAreBadRequests(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas"}}
	}})
	spec, _, ok := r.sup.Series("host.power")
	if !ok {
		t.Fatal("host.power is not registered")
	}
	if _, err := spec.Expr(plugin.SeriesArgs{}); !errors.Is(err, queries.ErrBadSeries) {
		t.Errorf("no host: %v, want ErrBadSeries", err)
	}
}

// With the plugin not running a host series is unknown: the API answers 400 for it.
func TestSeriesAreUnknownWhileThePluginIsNotRunning(t *testing.T) {
	r := newRig(t, rigOpts{}) // no host and no machine: the plugin is disabled
	if got := r.row().State; got != model.PluginDisabled {
		t.Fatalf("state = %s, want disabled", got)
	}
	if _, _, ok := r.sup.Series("host.cpu"); ok {
		t.Error("host.cpu is served by a plugin that is not running")
	}
}
