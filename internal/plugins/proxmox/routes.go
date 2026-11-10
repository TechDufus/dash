package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/upstream"
)

// Routes implements plugin.Router: GET /api/v1/plugins/proxmox/vms/{vmid}.
func (p *Plugin) Routes(r plugin.Routes) {
	r.Get("/vms/{vmid}", p.vmDetail, plugin.RouteSpec{
		Summary:  "Configuration, guest-agent addresses and snapshots of one VM",
		Scope:    "read",
		Response: model.VMDetail{},
	})
}

var (
	errInvalidVMID = &plugin.ActionError{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Message: "invalid vmid"}
	errVMNotFound  = &plugin.ActionError{Status: http.StatusNotFound, Code: model.ErrCodeNotFound, Message: "vm not found"}
	errNotQemu     = &plugin.ActionError{Status: http.StatusNotFound, Code: model.ErrCodeNotFound, Message: "detail is for qemu guests"}
)

// errNodeOffline is the answer for a guest whose node is offline: Proxmox cannot read its config.
func errNodeOffline(node string) error {
	return &plugin.ActionError{Status: http.StatusServiceUnavailable, Code: model.ErrCodeUpstreamUnavailable, Message: "node " + node + " is offline"}
}

// missingConfig reports whether Proxmox answered that the guest's config does not exist on the
// node asked: the guest moved to another node since the list was read. Proxmox before
// libpve-http-server-perl 5.2.0 (PVE 8.3 and older) says so in the status line only.
func missingConfig(err error) bool {
	var se *upstream.StatusError
	return errors.As(err, &se) && se.Code == http.StatusInternalServerError &&
		(strings.Contains(se.Body, "does not exist") || strings.Contains(se.Text, "does not exist"))
}

// vmDetail assembles a VM's configuration, guest-agent addresses and snapshots from Proxmox, asked
// of the node the VM runs on: another node answers that the VM's config does not exist, and then
// the guest list is read again once, in case the guest has just moved. The VM itself is the one the
// last poll found, with its links, critical flag and node. The id is checked before anything is
// read; an unknown id, a guest without a node and a container (the detail is qemu's) are a 404, and
// a guest of an offline node a 503, without a request to Proxmox; a failure of Proxmox is the
// core's category-only 502.
func (p *Plugin) vmDetail(w http.ResponseWriter, req *http.Request) {
	raw := req.PathValue("vmid")
	if !queries.ValidVMID(raw) {
		plugin.Fail(w, req, errInvalidVMID)
		return
	}
	vmid, _ := strconv.Atoi(raw)
	r := p.rt.Load()
	if r == nil {
		plugin.Fail(w, req, errNotRunning)
		return
	}
	src := r.detail
	var vm *model.VM
	built := r.buildVMs(r.nodeIndex())
	for i := range built.VMs {
		if built.VMs[i].VMID == vmid {
			vm = &built.VMs[i]
		}
	}
	if vm == nil || vm.Node == "" {
		plugin.Fail(w, req, errVMNotFound)
		return
	}
	if g, ok := r.guest(vmid); ok && g.Type != "" && g.Type != "qemu" {
		plugin.Fail(w, req, errNotQemu)
		return
	}
	if r.offlineNodes()[vm.Node] {
		plugin.Fail(w, req, errNodeOffline(vm.Node))
		return
	}
	cfg, err := src.VMConfig(req.Context(), vm.Node, vmid)
	if missingConfig(err) && r.client != nil {
		if node := r.movedTo(req.Context(), vmid, vm.Node); node != "" {
			vm.Node = node
			cfg, err = src.VMConfig(req.Context(), vm.Node, vmid)
		}
	}
	if err != nil {
		plugin.Fail(w, req, err)
		return
	}
	snaps, err := src.Snapshots(req.Context(), vm.Node, vmid)
	if err != nil {
		plugin.Fail(w, req, err)
		return
	}
	d := model.VMDetail{VM: *vm, Config: cfg, IPs: src.VMIPs(req.Context(), vm.Node, vmid), Snapshots: snaps}
	if d.IPs == nil {
		d.IPs = []model.IfaceAddr{}
	}
	if d.Snapshots == nil {
		d.Snapshots = []model.Snapshot{}
	}
	if d.Config.Disks == nil {
		d.Config.Disks = []model.KeySpec{}
	}
	if d.Config.NICs == nil {
		d.Config.NICs = []model.KeySpec{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(d)
}

// movedTo reads the guest list again and returns the node vmid runs on now when it differs from
// node; empty when the guest has not moved or the read fails. The list it read is not kept: the
// next poll publishes it.
func (r *runtime) movedTo(ctx context.Context, vmid int, node string) string {
	vms, err := r.client.VMs(ctx)
	if err != nil {
		return ""
	}
	for _, v := range vms {
		if v.VMID == vmid && v.Node != node {
			return v.Node
		}
	}
	return ""
}
