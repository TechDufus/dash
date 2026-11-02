package proxmox

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
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
)

// vmDetail assembles a VM's configuration, guest-agent addresses and snapshots from Proxmox. The
// VM itself is the one the last poll found, with its links, critical flag and node. The id is
// checked before anything is read, and an unknown id is a 404 without a request to Proxmox; a
// failure of Proxmox is the core's category-only 502.
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
	src := r.detailer()
	if src == nil {
		plugin.Fail(w, req, errVMNotFound) // no node yet, so no VM has been read
		return
	}
	var vm *model.VM
	built := r.buildVMs(r.nodeIndex())
	for i := range built.VMs {
		if built.VMs[i].VMID == vmid {
			vm = &built.VMs[i]
		}
	}
	if vm == nil {
		plugin.Fail(w, req, errVMNotFound)
		return
	}
	cfg, err := src.VMConfig(req.Context(), vmid)
	if err != nil {
		plugin.Fail(w, req, err)
		return
	}
	snaps, err := src.Snapshots(req.Context(), vmid)
	if err != nil {
		plugin.Fail(w, req, err)
		return
	}
	d := model.VMDetail{VM: *vm, Config: cfg, IPs: src.VMIPs(req.Context(), vmid), Snapshots: snaps}
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
