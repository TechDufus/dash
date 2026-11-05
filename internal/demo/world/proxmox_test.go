package world

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
)

// The Proxmox parts are what the proxmox twin publishes: the node's facts and its storages, with
// refs of the plugin that publishes them and values that make sense together.
func TestPVEPartsAreWhatTheTwinPublishes(t *testing.T) {
	w := testWorld()

	n := w.PVENode()
	if n.Node != HostName || n.Version == "" || n.Kernel == "" || n.CPUModel == "" || n.UptimeSeconds <= 0 {
		t.Errorf("PVENode = %+v", n)
	}
	if p, kind, id, ok := plugin.ParseRef(n.Ref); !ok || p != "proxmox" || kind != "Node" || id != HostName {
		t.Errorf("node ref %q", n.Ref)
	}

	ps := w.PVEStorages()
	if len(ps) < 2 {
		t.Fatalf("PVEStorages = %+v", ps)
	}
	seen := map[string]bool{}
	for _, s := range ps {
		if s.Name == "" || seen[s.Name] || s.UsedBytes <= 0 || s.SizeBytes <= s.UsedBytes {
			t.Errorf("storage %+v: want a unique name and 0 < used < size", s)
		}
		seen[s.Name] = true
		if p, kind, id, ok := plugin.ParseRef(s.Ref); !ok || p != "proxmox" || kind != "Storage" || id != s.Name {
			t.Errorf("storage ref %q for %q", s.Ref, s.Name)
		}
	}
}

// The values depend on the clock only: two builds at the same instant are equal, and the uptime
// moves with it.
func TestPVENodeFollowsTheClock(t *testing.T) {
	now := testNow
	w := New(func() time.Time { return now }, testStart, fakeLinks{})
	a, b := w.PVENode(), w.PVENode()
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(w.PVEStorages(), w.PVEStorages()) {
		t.Fatal("two builds at the same instant differ")
	}
	now = now.Add(time.Hour)
	if got := w.PVENode().UptimeSeconds - a.UptimeSeconds; got != 3600 {
		t.Errorf("uptime moved by %d s in an hour", got)
	}
}

// The fake node names no real one.
func TestPVEPartsNameNothingReal(t *testing.T) {
	w := testWorld()
	text := w.PVENode().Node + w.PVENode().CPUModel + w.PVENode().Kernel
	for _, s := range w.PVEStorages() {
		text += s.Name
	}
	for _, lab := range []string{"pve" + "-730", "E5" + "-2683", "fast" + "-zfs", "homelab", "talos"} {
		if strings.Contains(text, lab) {
			t.Errorf("the Proxmox fixtures mention %q", lab)
		}
	}
}
