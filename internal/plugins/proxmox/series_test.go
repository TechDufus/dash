package proxmox

import (
	"errors"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/queries"
)

// The six expressions of the default selector are the ones dash has always sent.
var defaultSeries = map[string]string{
	"vm.cpu":       `pve_cpu_usage_ratio{id="qemu/101"}`,
	"vm.mem":       `pve_memory_usage_bytes{id="qemu/101"} / pve_memory_size_bytes{id="qemu/101"}`,
	"vm.netIn":     `rate(pve_network_receive_bytes_total{id="qemu/101"}[2m])`,
	"vm.netOut":    `rate(pve_network_transmit_bytes_total{id="qemu/101"}[2m])`,
	"vm.diskRead":  `rate(pve_disk_read_bytes_total{id="qemu/101"}[2m])`,
	"vm.diskWrite": `rate(pve_disk_written_bytes_total{id="qemu/101"}[2m])`,
}

func TestDefaultSeriesExpressions(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.state(t, "healthy")
	for name, want := range defaultSeries {
		spec, id, ok := e.sup.Series(name)
		if !ok || id != ID {
			t.Fatalf("%s: found %v from %q", name, ok, id)
		}
		got, err := spec.Expr(queries.Args{VMID: "101"})
		if err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
}

// plugins.proxmox.series.vmSelector shapes all six expressions.
func TestVMSelectorShapesTheSeries(t *testing.T) {
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Proxmox) { c.Series.VMSelector = `vmid=~"{vmid}"` }})
	e.state(t, "healthy")
	for name, def := range defaultSeries {
		spec, _, _ := e.sup.Series(name)
		got, err := spec.Expr(queries.Args{VMID: "101"})
		if err != nil {
			t.Fatal(err)
		}
		want := strings.ReplaceAll(def, `id="qemu/101"`, `vmid=~"101"`)
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A selector that could carry more than one matcher fails Validate, so it never gets as far as a
// query; and an unvalidated one that does is refused at the expression too.
func TestVMSelectorInjectionIsRejected(t *testing.T) {
	p := New().(*Plugin)
	for _, sel := range []string{
		`id="qemu/{vmid}"} or vector(1) or {id="x`,
		`id="qemu/{vmid}",job="x"`,
		`id="{vmid}" or id="y"`,
		`id="qemu/\"{vmid}"`,
		`id!="qemu/{vmid}"`,
		`{id="qemu/{vmid}"}`,
		`id="qemu/{vmid}{vmid}"`,
		`id="qemu/x"`,
		`id="qemu/{vmid}}"`,
		`1id="{vmid}"`,
		`id=qemu/{vmid}`,
		``,
	} {
		c := defaultConfig("https://pve.example.com:8006")
		c.Series.VMSelector = sel
		if sel == "" {
			// An empty selector is the default, not an error.
			if err := p.Validate(&c); err != nil {
				t.Errorf("an empty selector: %v", err)
			}
			continue
		}
		if err := p.Validate(&c); err == nil {
			t.Errorf("Validate accepted the selector %q", sel)
		}
		if _, err := vmMatcher(sel, "101"); !errors.Is(err, queries.ErrBadSeries) {
			t.Errorf("vmMatcher(%q) = %v, want ErrBadSeries", sel, err)
		}
	}
	if got, err := vmMatcher(`id=~"qemu/{vmid}"`, "7"); err != nil || got != `id=~"qemu/7"` {
		t.Errorf("a regex matcher: %q, %v", got, err)
	}
}

// The VM id is digits only: nothing else reaches the expression.
func TestSeriesRefusesABadVMID(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.state(t, "healthy")
	spec, _, _ := e.sup.Series("vm.cpu")
	for _, id := range []string{"", "x", "101}or{id=\"", "1234567890", "-1", "1 2"} {
		if _, err := spec.Expr(queries.Args{VMID: id}); !errors.Is(err, queries.ErrBadSeries) {
			t.Errorf("vmid %q: err = %v, want ErrBadSeries", id, err)
		}
	}
}

// Before Start there is no instance and no settings: every expression is a bad series.
func TestSeriesBeforeStartIsBad(t *testing.T) {
	for _, s := range New().Manifest().Series {
		if _, err := s.Expr(queries.Args{VMID: "101"}); !errors.Is(err, queries.ErrBadSeries) {
			t.Errorf("%s before Start: %v", s.Name, err)
		}
	}
}

// The series exist only while the plugin runs: stopped, vm.cpu is an unknown series.
func TestSeriesAreServedOnlyWhileProxmoxRuns(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.state(t, "healthy")
	if _, _, ok := e.sup.Series("vm.cpu"); !ok {
		t.Fatal("vm.cpu is not served while the plugin runs")
	}
	off := false
	for _, ic := range e.set.Instances() {
		if ic.Name == ID {
			ic.Enabled = &off
			e.set.Set(ic)
		}
	}
	e.sup.Reload()
	e.state(t, model.PluginDisabled)
	if _, _, ok := e.sup.Series("vm.cpu"); ok {
		t.Error("vm.cpu is served by a disabled plugin")
	}
}

// A reload builds a new instance, and the series follow its settings, not the first one's.
func TestSeriesFollowAReload(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.state(t, "healthy")
	spec, _, _ := e.sup.Series("vm.cpu")
	if got, _ := spec.Expr(queries.Args{VMID: "5"}); got != `pve_cpu_usage_ratio{id="qemu/5"}` {
		t.Fatalf("before: %q", got)
	}
	c := *e.cfg
	c.Series.VMSelector = `vmid="{vmid}"`
	for _, ic := range e.set.Instances() {
		ic.Config = &c
		e.set.Set(ic)
	}
	e.sup.Reload()
	waitFor(t, "the new selector", func() bool {
		s, _, ok := e.sup.Series("vm.cpu")
		if !ok {
			return false
		}
		got, _ := s.Expr(queries.Args{VMID: "5"})
		return got == `pve_cpu_usage_ratio{vmid="5"}`
	})
}
