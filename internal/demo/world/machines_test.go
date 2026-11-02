package world

import (
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/plugin"
)

// The machines are valid hardware.machines entries: names the plugin's own rules would accept
// (unique ignoring case, at most 64 bytes), refs of the hardware plugin, and both sources of
// the real plugin present, so the demo page shows a node-exporter machine and a Gatus one.
func TestMachinesAreValidEntries(t *testing.T) {
	ms := testWorld().Machines()
	if len(ms) < 2 {
		t.Fatalf("machines = %+v, want at least one per source", ms)
	}
	seen := map[string]bool{}
	sources := map[string]int{}
	for _, m := range ms {
		low := strings.ToLower(m.Name)
		if m.Name == "" || len(m.Name) > 64 || seen[low] {
			t.Errorf("name %q is empty, over 64 bytes or repeated", m.Name)
		}
		seen[low] = true
		sources[m.Source]++
		if m.Source != "prometheus" && m.Source != "gatus" {
			t.Errorf("%s: source %q", m.Name, m.Source)
		}
		p, kind, id, ok := plugin.ParseRef(m.Ref)
		if !ok || p != "hardware" || kind != "Machine" || id != m.Name {
			t.Errorf("%s: ref %q", m.Name, m.Ref)
		}
		if !m.Up {
			t.Errorf("%s is down in the fake lab", m.Name)
		}
	}
	if sources["prometheus"] == 0 || sources["gatus"] == 0 {
		t.Errorf("sources = %v, want both", sources)
	}
}

// Every call returns its own slice, so a twin can change what it got without changing the next.
func TestMachinesAreFreshAndStable(t *testing.T) {
	w := testWorld()
	a, b := w.Machines(), w.Machines()
	a[0].Up = false
	a[0].Name = "changed"
	if b[0].Up != true || b[0].Name == "changed" {
		t.Error("two calls share a slice")
	}
	if c := w.Machines(); c[0].Name != b[0].Name || len(c) != len(b) {
		t.Errorf("the machines differ between calls: %+v vs %+v", c, b)
	}
}

// No machine of the fake lab is a real lab's: the names are generic.
func TestMachineNamesAreGeneric(t *testing.T) {
	for _, m := range testWorld().Machines() {
		for _, lab := range []string{"rpi-control", "mac-mini", "talos", "homelab"} {
			if strings.Contains(strings.ToLower(m.Name), lab) {
				t.Errorf("machine %q names %q", m.Name, lab)
			}
		}
	}
}
