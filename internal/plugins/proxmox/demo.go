package proxmox

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/upstream"
)

// demoEvery is how often the twin's numbers move on, like the rest of demo mode.
const demoEvery = 5 * time.Second

// demoURL is where the twin's links point: the fake lab's Proxmox, on example.com.
const demoURL = "https://pve.example.com:8006"

// twin is the plugin's demo twin (design 1.9): the same manifest, the guests, cluster and storages
// of the fake world, and no network. It is the plugin itself run on fixtures: the payloads, the
// status, the links, the attention items, the KPIs, the topology, the VM charts and the VM
// detail are the real code's, so what consumes them behaves as it does against a real Proxmox.
type twin struct {
	Plugin
	w *world.World
}

func newTwin() *twin { return &twin{} }

// twinLinks is the world's link source; the twin adds the Proxmox links itself.
type twinLinks struct{}

func (twinLinks) Headlamp(string, string, string) []model.Link { return []model.Link{} }

// Start publishes the world's payloads now and then every demoEvery.
func (t *twin) Start(ctx context.Context, h plugin.Host, _ any) error {
	h = t.Wrap(h)
	t.w = world.New(h.Now, h.Now(), twinLinks{})
	cfg := pluginconf.DefaultProxmox()
	cfg.URL = demoURL
	cfg.CriticalVMs.Tags = []string{"control-plane"}
	rctx, cancel := context.WithCancel(ctx)
	r := &runtime{
		h: h, cfg: cfg, t: defaultTiming(), ctx: rctx, cancel: cancel,
		facts: map[string]upstream.PVEHost{}, logged: map[string]string{},
		build:  t.vmsPayload,
		detail: twinDetail{w: t.w, now: h.Now},
	}
	// The fake cluster has two nodes; the first is the hardware twin's host, as
	// plugins.proxmox.node names it in a real cluster of several.
	r.cfg.Node = world.HostName
	t.rt.Store(r)
	t.refresh(r)
	r.spawn("demo", func() {
		tick := time.NewTicker(demoEvery)
		defer tick.Stop()
		for {
			select {
			case <-rctx.Done():
				return
			case <-tick.C:
				t.refresh(r)
			}
		}
	})
	return nil
}

// refresh moves the fake state on and publishes the three topics.
func (t *twin) refresh(r *runtime) {
	now := r.h.Now()
	ok := outcome{polled: true, at: now}
	r.mu.Lock()
	r.vmsOut, r.clusterOut, r.storOut = ok, ok, ok
	r.cluster = t.w.PVECluster()
	r.storages = t.w.PVEStorages()
	data, storages := r.clusterPayloadLocked(), storageData(r.storages)
	r.mu.Unlock()
	r.publishVMs()
	r.h.Publish(topicCluster, data, []model.SourceStatus{src(sourceName, ok)})
	r.h.Publish(topicStorage, storages, []model.SourceStatus{src(sourceName, ok)})
}

// vmsPayload is the world's guests, decorated the way buildVMs decorates a real poll: the ref,
// the critical flag (a stopped one is critical, as the setting says) and the health that follows.
func (t *twin) vmsPayload() model.VMsData {
	d := t.w.VMs()
	out := model.VMsData{VMs: make([]model.VM, len(d.VMs)), Tasks: d.Tasks}
	for i, vm := range d.VMs {
		vm.Ref = plugin.Ref(ID, "VM", fmt.Sprint(vm.VMID))
		vm.Critical = hasTag(vm.Tags, "control-plane")
		vm.Health = vmHealth(vm.Critical, vm.Status, vm.Template, false)
		// The links are the plugin's own, not the world's, so the world's link source can lose
		// its console method without the twin losing its console links.
		vm.Links = slices.Concat(vmLinks(demoURL, vm.Node, vm.VMID, vm.Name), proxmoxUI(demoURL))
		out.VMs[i] = vm
	}
	if out.Tasks == nil {
		out.Tasks = []model.PVETask{}
	}
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, want) {
			return true
		}
	}
	return false
}

// twinDetail answers the VM detail from the world's guests.
type twinDetail struct {
	w   *world.World
	now func() time.Time
}

// find is the guest vmid when it runs on node: like Proxmox, the twin knows a guest's detail on
// its own node only.
func (d twinDetail) find(node string, vmid int) (model.VM, bool) {
	for _, vm := range d.w.VMs().VMs {
		if vm.VMID == vmid && vm.Node == node {
			return vm, true
		}
	}
	return model.VM{}, false
}

func (d twinDetail) VMConfig(_ context.Context, node string, vmid int) (model.VMConfig, error) {
	vm, ok := d.find(node, vmid)
	if !ok {
		return model.VMConfig{}, fmt.Errorf("%w: no such vm", upstream.ErrInvalid)
	}
	return model.VMConfig{
		Cores: int(vm.CPUs), Sockets: 1, MemoryMiB: int(vm.MaxMemBytes >> 20),
		OSType: "l26", Agent: "1",
		Disks: []model.KeySpec{{Key: "scsi0", Spec: fmt.Sprintf("local-zfs:vm-%d-disk-0,size=64G,ssd=1", vmid)}},
		NICs:  []model.KeySpec{{Key: "net0", Spec: fmt.Sprintf("virtio=BC:24:11:00:00:%02X,bridge=vmbr0", vmid%256)}},
	}, nil
}

func (d twinDetail) Snapshots(_ context.Context, node string, vmid int) ([]model.Snapshot, error) {
	if _, ok := d.find(node, vmid); !ok {
		return nil, fmt.Errorf("%w: no such vm", upstream.ErrInvalid)
	}
	at := d.now().Add(-26 * time.Hour)
	return []model.Snapshot{{Name: "pre-upgrade", Time: &at, Description: "before the last Kubernetes upgrade"}}, nil
}

func (d twinDetail) VMIPs(_ context.Context, node string, vmid int) []model.IfaceAddr {
	if vm, ok := d.find(node, vmid); ok && vm.Status == "running" {
		return []model.IfaceAddr{{Iface: "eth0", Address: fmt.Sprintf("192.0.2.%d", vmid%200)}}
	}
	return []model.IfaceAddr{}
}
