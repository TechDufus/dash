package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

var (
	pveDiskRe = regexp.MustCompile(`^(scsi|virtio|sata|ide|efidisk|tpmstate)\d*$`)
	pveNICRe  = regexp.MustCompile(`^net\d+$`)
	keyNumRe  = regexp.MustCompile(`^(\D+)(\d*)$`)
)

const maxPVETasks = 20

type PVEHost struct {
	UptimeSeconds                int64
	PVEVersion, Kernel, CPUModel string
}

type PVEVM struct {
	VMID                                                          int
	Name, Status                                                  string
	Tags                                                          []string
	CPUs, CPURatio                                                float64
	MemBytes, MaxMemBytes, UptimeSeconds, NetInBytes, NetOutBytes int64
	Template                                                      bool
	Lock                                                          string
}

// Proxmox reads the Proxmox VE API with an API token.
type Proxmox struct {
	c     client
	node  string
	token string
}

func NewProxmox(baseURL, node, token string, rt http.RoundTripper) *Proxmox {
	return &Proxmox{c: newClient(baseURL, rt, 5*time.Second), node: node, token: token}
}

// Enabled reports whether a token is configured.
func (p *Proxmox) Enabled() bool { return p.token != "" }

// flex decodes a JSON number, numeric string or bool: Proxmox mixes them.
type flex float64

func (f *flex) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(bytes.TrimSpace(b), `"`)
	switch string(b) {
	case "", "null", "false":
		*f = 0
		return nil
	case "true":
		*f = 1
		return nil
	}
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		*f = 0 // tolerate junk
		return nil
	}
	*f = flex(v)
	return nil
}

func (f flex) int64() int64 { return int64(f) }

func (p *Proxmox) get(ctx context.Context, path string, out any) error {
	if !p.Enabled() {
		return ErrDisabled
	}
	hdr := http.Header{"Authorization": {"PVEAPIToken=" + p.token}}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := p.c.getJSON(ctx, http.MethodGet, path, nil, nil, hdr, &env); err != nil {
		return err
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (p *Proxmox) nodePath(suffix string) string {
	return "/api2/json/nodes/" + url.PathEscape(p.node) + suffix
}

func (p *Proxmox) vmPath(vmid int, suffix string) (string, error) {
	if vmid <= 0 {
		return "", fmt.Errorf("%w: bad vmid", ErrInvalid)
	}
	return p.nodePath("/qemu/" + strconv.Itoa(vmid) + suffix), nil
}

// Nodes lists the names of the Proxmox cluster's nodes (GET /api2/json/nodes).
func (p *Proxmox) Nodes(ctx context.Context) ([]string, error) {
	var raw []struct {
		Node string `json:"node"`
	}
	if err := p.get(ctx, "/api2/json/nodes", &raw); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, n := range raw {
		out = append(out, n.Node)
	}
	return out, nil
}

// NodeStatus returns the host facts shown on the Host page.
func (p *Proxmox) NodeStatus(ctx context.Context) (PVEHost, error) {
	var d struct {
		Uptime        flex   `json:"uptime"`
		PVEVersion    string `json:"pveversion"`
		KVersion      string `json:"kversion"`
		CurrentKernel struct {
			Release string `json:"release"`
		} `json:"current-kernel"`
		CPUInfo struct {
			Model string `json:"model"`
		} `json:"cpuinfo"`
	}
	if err := p.get(ctx, p.nodePath("/status"), &d); err != nil {
		return PVEHost{}, err
	}
	h := PVEHost{UptimeSeconds: d.Uptime.int64(), PVEVersion: d.PVEVersion, Kernel: d.CurrentKernel.Release, CPUModel: d.CPUInfo.Model}
	// "pve-manager/9.2.2/b9984c6d90a4bd80" -> "9.2.2"
	if parts := strings.Split(d.PVEVersion, "/"); len(parts) >= 2 {
		h.PVEVersion = parts[1]
	}
	if h.Kernel == "" {
		h.Kernel = d.KVersion
	}
	return h, nil
}

// VMs lists qemu guests (including templates) of the whole cluster.
func (p *Proxmox) VMs(ctx context.Context) ([]PVEVM, error) {
	var raw []struct {
		VMID     flex   `json:"vmid"`
		Name     string `json:"name"`
		Status   string `json:"status"`
		Tags     string `json:"tags"`
		MaxCPU   flex   `json:"maxcpu"`
		CPU      flex   `json:"cpu"`
		Mem      flex   `json:"mem"`
		MaxMem   flex   `json:"maxmem"`
		Uptime   flex   `json:"uptime"`
		NetIn    flex   `json:"netin"`
		NetOut   flex   `json:"netout"`
		Template flex   `json:"template"`
		Lock     string `json:"lock"`
	}
	if err := p.get(ctx, "/api2/json/cluster/resources?type=vm", &raw); err != nil {
		return nil, err
	}
	out := make([]PVEVM, 0, len(raw))
	for _, r := range raw {
		out = append(out, PVEVM{
			VMID: int(r.VMID), Name: r.Name, Status: r.Status, Tags: splitTags(r.Tags),
			CPUs: float64(r.MaxCPU), CPURatio: float64(r.CPU),
			MemBytes: r.Mem.int64(), MaxMemBytes: r.MaxMem.int64(), UptimeSeconds: r.Uptime.int64(),
			NetInBytes: r.NetIn.int64(), NetOutBytes: r.NetOut.int64(),
			Template: r.Template != 0, Lock: r.Lock,
		})
	}
	return out, nil
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
		out = append(out, t)
	}
	return out
}

// Tasks returns the 20 most recent cluster tasks, newest first.
func (p *Proxmox) Tasks(ctx context.Context) ([]model.PVETask, error) {
	var raw []struct {
		UPID      string `json:"upid"`
		Type      string `json:"type"`
		ID        string `json:"id"`
		User      string `json:"user"`
		Status    string `json:"status"`
		StartTime flex   `json:"starttime"`
		EndTime   flex   `json:"endtime"`
	}
	if err := p.get(ctx, "/api2/json/cluster/tasks", &raw); err != nil {
		return nil, err
	}
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].StartTime > raw[j].StartTime })
	if len(raw) > maxPVETasks {
		raw = raw[:maxPVETasks]
	}
	out := make([]model.PVETask, 0, len(raw))
	for _, r := range raw {
		t := model.PVETask{
			UPID: r.UPID, Type: r.Type, ID: r.ID, User: r.User, Status: r.Status,
			StartTime: time.Unix(r.StartTime.int64(), 0).UTC(),
		}
		if r.EndTime > 0 {
			e := time.Unix(r.EndTime.int64(), 0).UTC()
			t.EndTime = &e
		} else if t.Status == "" {
			t.Status = "running"
		}
		out = append(out, t)
	}
	return out, nil
}

// VMConfig returns the parts of a guest's config the VM detail shows.
func (p *Proxmox) VMConfig(ctx context.Context, vmid int) (model.VMConfig, error) {
	path, err := p.vmPath(vmid, "/config")
	if err != nil {
		return model.VMConfig{}, err
	}
	var raw map[string]json.RawMessage
	if err := p.get(ctx, path, &raw); err != nil {
		return model.VMConfig{}, err
	}
	str := func(k string) string {
		b, ok := raw[k]
		if !ok {
			return ""
		}
		var s string
		if json.Unmarshal(b, &s) == nil {
			return s
		}
		return strings.Trim(string(b), `"`)
	}
	num := func(k string, def int) int {
		if _, ok := raw[k]; !ok {
			return def
		}
		var f flex
		_ = json.Unmarshal(raw[k], &f)
		return int(f)
	}
	c := model.VMConfig{
		Cores: num("cores", 1), Sockets: num("sockets", 1),
		MemoryMiB: num("memory", 512), BalloonMiB: num("balloon", 0),
		OSType: str("ostype"), Agent: str("agent"),
		Disks: []model.KeySpec{}, NICs: []model.KeySpec{},
	}
	for k := range raw {
		switch {
		case pveDiskRe.MatchString(k):
			c.Disks = append(c.Disks, model.KeySpec{Key: k, Spec: str(k)})
		case pveNICRe.MatchString(k):
			c.NICs = append(c.NICs, model.KeySpec{Key: k, Spec: str(k)})
		}
	}
	sortKeySpecs(c.Disks)
	sortKeySpecs(c.NICs)
	return c, nil
}

// sortKeySpecs orders by name then numeric suffix (scsi2 before scsi10).
func sortKeySpecs(s []model.KeySpec) {
	split := func(k string) (string, int) {
		m := keyNumRe.FindStringSubmatch(k)
		if m == nil {
			return k, 0
		}
		n, _ := strconv.Atoi(m[2])
		return m[1], n
	}
	slices.SortFunc(s, func(a, b model.KeySpec) int {
		an, ai := split(a.Key)
		bn, bi := split(b.Key)
		if c := strings.Compare(an, bn); c != 0 {
			return c
		}
		return ai - bi
	})
}

// VMIPs lists guest-agent addresses; any failure (no agent, VM stopped) gives
// an empty list.
func (p *Proxmox) VMIPs(ctx context.Context, vmid int) []model.IfaceAddr {
	out := []model.IfaceAddr{}
	path, err := p.vmPath(vmid, "/agent/network-get-interfaces")
	if err != nil {
		return out
	}
	var d struct {
		Result []struct {
			Name string `json:"name"`
			IPs  []struct {
				Address string `json:"ip-address"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	if p.get(ctx, path, &d) != nil {
		return out
	}
	for _, i := range d.Result {
		if i.Name == "lo" {
			continue
		}
		for _, a := range i.IPs {
			if ip, err := netip.ParseAddr(a.Address); err == nil && !ip.IsLoopback() {
				out = append(out, model.IfaceAddr{Iface: i.Name, Address: a.Address})
			}
		}
	}
	return out
}

// Snapshots lists a guest's snapshots, without the "current" pseudo entry.
func (p *Proxmox) Snapshots(ctx context.Context, vmid int) ([]model.Snapshot, error) {
	path, err := p.vmPath(vmid, "/snapshot")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		SnapTime    flex   `json:"snaptime"`
	}
	if err := p.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]model.Snapshot, 0, len(raw))
	for _, r := range raw {
		if r.Name == "current" {
			continue
		}
		s := model.Snapshot{Name: r.Name, Description: r.Description}
		if r.SnapTime > 0 {
			t := time.Unix(r.SnapTime.int64(), 0).UTC()
			s.Time = &t
		}
		out = append(out, s)
	}
	return out, nil
}
