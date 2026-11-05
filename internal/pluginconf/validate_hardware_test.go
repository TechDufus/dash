package pluginconf

import (
	"slices"
	"strings"
	"testing"
)

// defaultsWith is the default section with a change applied.
func defaultsWith(mod func(*Hardware)) Hardware {
	h := DefaultHardware()
	mod(&h)
	return h
}

func TestHardwareDefaultsAreValid(t *testing.T) {
	d := DefaultHardware()
	if err := d.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if d.Selectors.NodeExporterJob != "node-exporter" || d.Selectors.HwmonChip != "platform_coretemp_.*" {
		t.Errorf("selectors = %+v", d.Selectors)
	}
	if d.TempThresholds.Warn != 85 || d.TempThresholds.Crit != 90 {
		t.Errorf("thresholds = %+v", d.TempThresholds)
	}
	if len(d.Hosts) != 0 || len(d.Machines) != 0 || d.ZFSScrubMetricPrefix != "" {
		t.Errorf("the lab values have no default: %+v", d)
	}
}

// One host is supported until the plugin handles several; the rule and its message moved here
// from internal/config with the section.
func TestHardwareAtMostOneHost(t *testing.T) {
	if err := defaultsWith(func(h *Hardware) { h.Hosts = []HardwareHost{{Nodename: "a"}} }).Validate(); err != nil {
		t.Errorf("one host: %v", err)
	}
	err := defaultsWith(func(h *Hardware) { h.Hosts = []HardwareHost{{Nodename: "a"}, {Nodename: "b"}} }).Validate()
	if got := paths(err); !slices.Equal(got, []string{"hosts"}) || !strings.Contains(err.Error(), "has 2 entries; one host is supported") {
		t.Errorf("two hosts: %v", err)
	}
}

// An empty scrub prefix is valid (it hides the scrub panel); anything set must be a metric name.
func TestHardwareScrubPrefix(t *testing.T) {
	for _, ok := range []string{"", "node_zfs_scrub", "a:b_c", "_x"} {
		if err := defaultsWith(func(h *Hardware) { h.ZFSScrubMetricPrefix = ok }).Validate(); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a-b", "1abc", "has space", "a.b"} {
		err := defaultsWith(func(h *Hardware) { h.ZFSScrubMetricPrefix = bad }).Validate()
		if got := paths(err); !slices.Equal(got, []string{"zfsScrubMetricPrefix"}) || !strings.Contains(err.Error(), "is not a metric name") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestHardwareHostRanges(t *testing.T) {
	host := func(x HardwareHost) Hardware { return defaultsWith(func(h *Hardware) { h.Hosts = []HardwareHost{x} }) }
	for _, ok := range []HardwareHost{{}, {Bays: 0}, {Bays: 128}, {FanMaxRPM: 100000}, {Bays: 16, FanMaxRPM: 18000}} {
		if err := host(ok).Validate(); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for name, bad := range map[string]HardwareHost{
		"negative bays": {Bays: -1}, "129 bays": {Bays: 129},
		"negative fan": {FanMaxRPM: -5}, "fan over": {FanMaxRPM: 100001},
	} {
		err := host(bad).Validate()
		if got := paths(err); !slices.Equal(got, []string{"hosts"}) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := host(HardwareHost{Bays: 129}).Validate(); err == nil || !strings.Contains(err.Error(), "bays must be 0 to 128") || strings.Contains(err.Error(), "129") {
		t.Errorf("the message must name the field and the range, never the value: %v", err)
	}
}

func TestHardwareMachineRules(t *testing.T) {
	machines := func(ms ...HardwareMachine) Hardware { return defaultsWith(func(h *Hardware) { h.Machines = ms }) }
	if err := machines(
		HardwareMachine{Name: "nas", NodeExporterNodename: "nas"},
		HardwareMachine{Name: "pi", GatusEndpoint: "pi SSH"},
	).Validate(); err != nil {
		t.Errorf("a valid pair: %v", err)
	}
	cases := map[string]struct {
		ms   []HardwareMachine
		want string
	}{
		"no name":      {[]HardwareMachine{{NodeExporterNodename: "a"}}, "name is required"},
		"blank name":   {[]HardwareMachine{{Name: "  ", GatusEndpoint: "a"}}, "name is required"},
		"long name":    {[]HardwareMachine{{Name: strings.Repeat("x", 65), GatusEndpoint: "a"}}, "longer than 64 bytes"},
		"repeat":       {[]HardwareMachine{{Name: "nas", GatusEndpoint: "a"}, {Name: "NAS", GatusEndpoint: "b"}}, "repeats an earlier machine"},
		"neither":      {[]HardwareMachine{{Name: "nas"}}, "exactly one of nodeExporterNodename and gatusEndpoint"},
		"both":         {[]HardwareMachine{{Name: "nas", NodeExporterNodename: "a", GatusEndpoint: "b"}}, "exactly one of nodeExporterNodename and gatusEndpoint"},
		"blank source": {[]HardwareMachine{{Name: "nas", NodeExporterNodename: "  "}}, "exactly one of nodeExporterNodename and gatusEndpoint"},
	}
	for name, c := range cases {
		err := machines(c.ms...).Validate()
		if got := paths(err); !slices.Equal(got, []string{"machines"}) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a machines error containing %q", name, err, c.want)
		}
	}
	if err := machines(HardwareMachine{Name: strings.Repeat("x", 64), GatusEndpoint: "a"}).Validate(); err != nil {
		t.Errorf("a 64 byte name: %v", err)
	}
}

func TestHardwareSelectorsAndThresholds(t *testing.T) {
	for name, c := range map[string]struct {
		mod  func(*Hardware)
		path string
	}{
		"no job":          {func(h *Hardware) { h.Selectors.NodeExporterJob = " " }, "selectors.nodeExporterJob"},
		"no chip":         {func(h *Hardware) { h.Selectors.HwmonChip = "" }, "selectors.hwmonChip"},
		"chip not RE2":    {func(h *Hardware) { h.Selectors.HwmonChip = "platform_(" }, "selectors.hwmonChip"},
		"warn zero":       {func(h *Hardware) { h.TempThresholds.Warn = 0 }, "tempThresholds"},
		"warn negative":   {func(h *Hardware) { h.TempThresholds.Warn = -3 }, "tempThresholds"},
		"crit equals":     {func(h *Hardware) { h.TempThresholds.Crit = h.TempThresholds.Warn }, "tempThresholds"},
		"crit below warn": {func(h *Hardware) { h.TempThresholds.Crit = 70 }, "tempThresholds"},
	} {
		if got := paths(defaultsWith(c.mod).Validate()); !slices.Equal(got, []string{c.path}) {
			t.Errorf("%s: paths = %v, want [%s]", name, got, c.path)
		}
	}
	if err := defaultsWith(func(h *Hardware) {
		h.Selectors = HardwareSelectors{NodeExporterJob: "ne", HwmonChip: "k10temp_.*"}
		h.TempThresholds = TempThresholds{Warn: 70, Crit: 80}
	}).Validate(); err != nil {
		t.Errorf("custom values: %v", err)
	}
}

// Normalized trims names and fills the sensor aliases' defaults, and leaves its input alone.
func TestHardwareNormalized(t *testing.T) {
	in := defaultsWith(func(h *Hardware) {
		h.Hosts = []HardwareHost{{Name: " server1 ", Nodename: " pve ", Model: " Dell ", SensorAliases: HardwareSensorAliases{Exhaust: " outlet "}}}
		h.Machines = []HardwareMachine{{Name: " nas ", NodeExporterNodename: " nas ", GatusEndpoint: ""}}
	})
	out := in.Normalized()
	if x := out.Hosts[0]; x.Name != "server1" || x.Nodename != "pve" || x.Model != "Dell" ||
		x.SensorAliases.Inlet != "inlet" || x.SensorAliases.Exhaust != "outlet" {
		t.Errorf("host = %+v", x)
	}
	if m := out.Machines[0]; m.Name != "nas" || m.NodeExporterNodename != "nas" {
		t.Errorf("machine = %+v", m)
	}
	if in.Hosts[0].Name != " server1 " || in.Hosts[0].SensorAliases.Inlet != "" || in.Machines[0].Name != " nas " {
		t.Errorf("Normalized changed its input: %+v", in)
	}
}

func TestHardwareHooks(t *testing.T) {
	var section any = &Hardware{}
	if _, ok := section.(interface{ Validate() error }); !ok {
		t.Error("*Hardware has no Validate")
	}
	if _, ok := section.(interface{ ActionsConfig() Actions }); ok {
		t.Error("hardware has an action policy")
	}
}
