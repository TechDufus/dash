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
	"github.com/TechDufus/dash/internal/secret"
)

var (
	// pveNodeRe is the rule Proxmox applies to node (and cluster) names. A name from the API that
	// breaks it is not used in a path, a ref, a link or a message.
	pveNodeRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)
	pveDiskRe = regexp.MustCompile(`^(scsi|virtio|sata|ide|efidisk|tpmstate)\d*$`)
	pveNICRe  = regexp.MustCompile(`^net\d+$`)
	keyNumRe  = regexp.MustCompile(`^(\D+)(\d*)$`)
)

const maxPVETasks = 20

// PVEHost is what a node reports about itself (GET /nodes/{node}/status).
type PVEHost struct {
	UptimeSeconds                int64
	PVEVersion, Kernel, CPUModel string
	// Sockets and Cores are its CPU sockets and its physical cores over all of them.
	Sockets, Cores int
}

// PVEVM is one guest of the cluster; Node is the node it runs on and Type is qemu or lxc.
type PVEVM struct {
	VMID                                                          int
	Node, Type                                                    string
	Name, Status                                                  string
	Tags                                                          []string
	CPUs, CPURatio                                                float64
	MemBytes, MaxMemBytes, UptimeSeconds, NetInBytes, NetOutBytes int64
	Template                                                      bool
	Lock                                                          string
}

// Proxmox reads the Proxmox VE API of a cluster (or a standalone server) with an API token, through
// whichever node its URL reaches: any node answers the cluster calls and proxies a node's own calls.
type Proxmox struct {
	c           client
	tokenID     string       // "user@realm!name"
	tokenSecret secret.Value // read per request, so a rotated secret file applies without a restart
}

// NewProxmox builds the client. Requests time out after 5s unless opts say otherwise (Timeout).
func NewProxmox(baseURL, tokenID string, tokenSecret secret.Value, rt http.RoundTripper, opts ...Option) *Proxmox {
	return &Proxmox{c: newClient(baseURL, rt, 5*time.Second, opts...), tokenID: tokenID, tokenSecret: tokenSecret}
}

// errNoNode is what a call that addresses a node returns when it is given none, or a name Proxmox
// would not give a node.
var errNoNode = fmt.Errorf("%w: no node", ErrInvalid)

// ValidNodeName reports whether name follows the Proxmox rule for node names.
func ValidNodeName(name string) bool { return pveNodeRe.MatchString(name) }

// nodeOrEmpty is name when it is a valid node name, else empty.
func nodeOrEmpty(name string) string {
	if ValidNodeName(name) {
		return name
	}
	return ""
}

// Enabled reports whether a token is configured.
func (p *Proxmox) Enabled() bool { return p.tokenID != "" && p.tokenSecret.IsSet() }

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
	hdr := http.Header{"Authorization": {"PVEAPIToken=" + p.tokenID + "=" + p.tokenSecret.Reveal()}}
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
		return fmt.Errorf("%w: decode %s: %w", ErrInvalidResponse, path, err)
	}
	return nil
}

func nodePath(node, suffix string) string {
	return "/api2/json/nodes/" + url.PathEscape(node) + suffix
}

// vmPath addresses a guest on its own node: another node answers that the guest's config does not
// exist.
func vmPath(node string, vmid int, suffix string) (string, error) {
	if !ValidNodeName(node) {
		return "", errNoNode
	}
	if vmid <= 0 {
		return "", fmt.Errorf("%w: bad vmid", ErrInvalid)
	}
	return nodePath(node, "/qemu/"+strconv.Itoa(vmid)+suffix), nil
}

// PVEClusterStatus is the cluster as GET /cluster/status reports it. A standalone server is a
// cluster of one: no name, always quorate. Transient marks an answer without the "cluster" entry
// from a server that is not standalone (the cluster is being formed, or its file system is
// restarting): its name and quorum are unknown.
type PVEClusterStatus struct {
	Name       string
	Standalone bool
	Transient  bool
	Quorate    bool
	Nodes      []PVEClusterNode // sorted by name
}

// PVEClusterNode is one node of the cluster as /cluster/status lists it. NodeID is 0 for a
// standalone server.
type PVEClusterNode struct {
	Name   string
	NodeID int
	Online bool
}

// ClusterStatus reads the cluster's name, its quorum and its nodes (GET /cluster/status; the token
// needs Sys.Audit on /). A standalone server answers with its node entry only, node id 0, and no
// "cluster" entry. Names that break the Proxmox rule are dropped.
func (p *Proxmox) ClusterStatus(ctx context.Context) (PVEClusterStatus, error) {
	var raw []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		NodeID  flex   `json:"nodeid"`
		Quorate flex   `json:"quorate"`
		Online  flex   `json:"online"`
	}
	if err := p.get(ctx, "/api2/json/cluster/status", &raw); err != nil {
		return PVEClusterStatus{}, err
	}
	out := PVEClusterStatus{Nodes: []PVEClusterNode{}}
	cluster := false
	for _, r := range raw {
		switch r.Type {
		case "cluster":
			cluster = true
			out.Name, out.Quorate = nodeOrEmpty(r.Name), r.Quorate != 0
		case "node":
			if ValidNodeName(r.Name) {
				out.Nodes = append(out.Nodes, PVEClusterNode{Name: r.Name, NodeID: int(r.NodeID), Online: r.Online != 0})
			}
		}
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Name < out.Nodes[j].Name })
	if !cluster {
		if len(out.Nodes) == 1 && out.Nodes[0].NodeID == 0 {
			out.Standalone, out.Quorate = true, true
		} else {
			out.Transient = true
		}
	}
	return out, nil
}

// PVENodeUsage is one node's usage as the cluster state reports it. Status is "online", "offline" or
// "unknown"; an offline node is still listed, without usage.
type PVENodeUsage struct {
	Node, Status                       string
	CPURatio                           float64 // 0..1 of all its threads
	Threads                            int
	Mem, MaxMem, Disk, MaxDisk, Uptime int64
}

// NodeUsage lists every node of the cluster with its usage (GET /cluster/resources?type=node),
// sorted by name: the cluster state lists an offline node too. Names that break the Proxmox rule
// are dropped.
func (p *Proxmox) NodeUsage(ctx context.Context) ([]PVENodeUsage, error) {
	var raw []struct {
		Node    string `json:"node"`
		Status  string `json:"status"`
		CPU     flex   `json:"cpu"`
		MaxCPU  flex   `json:"maxcpu"`
		Mem     flex   `json:"mem"`
		MaxMem  flex   `json:"maxmem"`
		Disk    flex   `json:"disk"`
		MaxDisk flex   `json:"maxdisk"`
		Uptime  flex   `json:"uptime"`
	}
	if err := p.get(ctx, "/api2/json/cluster/resources?type=node", &raw); err != nil {
		return nil, err
	}
	out := make([]PVENodeUsage, 0, len(raw))
	for _, r := range raw {
		if !ValidNodeName(r.Node) {
			continue
		}
		out = append(out, PVENodeUsage{
			Node: r.Node, Status: r.Status, CPURatio: float64(r.CPU), Threads: int(r.MaxCPU),
			Mem: r.Mem.int64(), MaxMem: r.MaxMem.int64(), Disk: r.Disk.int64(), MaxDisk: r.MaxDisk.int64(),
			Uptime: r.Uptime.int64(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}

// NodeStatus returns what a node reports about itself. Any node proxies the call to the node named;
// a node that is down answers HTTP 595 through the one asked.
func (p *Proxmox) NodeStatus(ctx context.Context, node string) (PVEHost, error) {
	var d struct {
		Uptime        flex   `json:"uptime"`
		PVEVersion    string `json:"pveversion"`
		KVersion      string `json:"kversion"`
		CurrentKernel struct {
			Release string `json:"release"`
		} `json:"current-kernel"`
		CPUInfo struct {
			Model   string `json:"model"`
			Sockets flex   `json:"sockets"`
			Cores   flex   `json:"cores"`
		} `json:"cpuinfo"`
	}
	if !ValidNodeName(node) {
		return PVEHost{}, errNoNode
	}
	if err := p.get(ctx, nodePath(node, "/status"), &d); err != nil {
		return PVEHost{}, err
	}
	h := PVEHost{
		UptimeSeconds: d.Uptime.int64(), PVEVersion: d.PVEVersion, Kernel: d.CurrentKernel.Release, CPUModel: d.CPUInfo.Model,
		Sockets: int(d.CPUInfo.Sockets), Cores: int(d.CPUInfo.Cores),
	}
	// "pve-manager/9.2.2/b9984c6d90a4bd80" -> "9.2.2"
	if parts := strings.Split(d.PVEVersion, "/"); len(parts) >= 2 {
		h.PVEVersion = parts[1]
	}
	if h.Kernel == "" {
		h.Kernel = d.KVersion
	}
	return h, nil
}

// PVEStorage is one storage of one node with its usage. A shared storage (NFS, Ceph...) is listed
// once per node, each time with the same usage. Status is "available" while the node reaches it.
type PVEStorage struct {
	Name, Node, Status   string
	Shared               bool
	UsedBytes, SizeBytes int64
}

// Storages lists every node's storages that report a size (GET /cluster/resources?type=storage),
// sorted by node and name; a storage that is not active on a node has none there and is left out,
// and so are an offline node's, whose sizes the cluster forgets. The token needs Datastore.Audit.
func (p *Proxmox) Storages(ctx context.Context) ([]PVEStorage, error) {
	var raw []struct {
		Storage string `json:"storage"`
		Node    string `json:"node"`
		Status  string `json:"status"`
		Shared  flex   `json:"shared"`
		Disk    flex   `json:"disk"`
		MaxDisk flex   `json:"maxdisk"`
	}
	if err := p.get(ctx, "/api2/json/cluster/resources?type=storage", &raw); err != nil {
		return nil, err
	}
	out := make([]PVEStorage, 0, len(raw))
	for _, r := range raw {
		if r.Storage == "" || r.MaxDisk <= 0 || !ValidNodeName(r.Node) {
			continue
		}
		out = append(out, PVEStorage{Name: r.Storage, Node: r.Node, Status: r.Status, Shared: r.Shared != 0, UsedBytes: r.Disk.int64(), SizeBytes: r.MaxDisk.int64()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// VMs lists the guests (including templates) of the whole cluster, each with its node (empty when
// the API names one that breaks the Proxmox rule). A guest of a node that has been offline for a
// while comes back with status "unknown" and without its name, template flag and memory size.
func (p *Proxmox) VMs(ctx context.Context) ([]PVEVM, error) {
	var raw []struct {
		VMID     flex   `json:"vmid"`
		Node     string `json:"node"`
		Type     string `json:"type"`
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
			VMID: int(r.VMID), Node: nodeOrEmpty(r.Node), Type: r.Type, Name: r.Name, Status: r.Status, Tags: splitTags(r.Tags),
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
		Node      string `json:"node"`
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
			UPID: r.UPID, Node: nodeOrEmpty(r.Node), Type: r.Type, ID: r.ID, User: r.User, Status: r.Status,
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

// VMConfig returns the parts of a guest's config the VM detail shows; node is the node it runs on.
func (p *Proxmox) VMConfig(ctx context.Context, node string, vmid int) (model.VMConfig, error) {
	path, err := vmPath(node, vmid, "/config")
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

// VMIPs lists guest-agent addresses of a guest on node; any failure (no agent, VM stopped) gives
// an empty list.
func (p *Proxmox) VMIPs(ctx context.Context, node string, vmid int) []model.IfaceAddr {
	out := []model.IfaceAddr{}
	path, err := vmPath(node, vmid, "/agent/network-get-interfaces")
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

// Snapshots lists the snapshots of a guest on node, without the "current" pseudo entry.
func (p *Proxmox) Snapshots(ctx context.Context, node string, vmid int) ([]model.Snapshot, error) {
	path, err := vmPath(node, vmid, "/snapshot")
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
