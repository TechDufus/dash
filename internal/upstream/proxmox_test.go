package upstream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

func pveServer(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var auth []string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		body, ok := routes[r.URL.RequestURI()]
		if !ok {
			http.Error(w, `{"data":null,"message":"no such "}`, 501)
			return
		}
		io.WriteString(w, body)
	})
	return s, &auth
}

func TestProxmoxNodeStatusAndVMs(t *testing.T) {
	s, auth := pveServer(t, map[string]string{
		"/api2/json/nodes/pve-1/status": `{"data":{"uptime":2422190,"kversion":"Linux 7.0.2-6-pve #1 SMP","pveversion":"pve-manager/9.2.2/b9984c6d90a4bd80","current-kernel":{"release":"7.0.2-6-pve"},"cpuinfo":{"model":"Example CPU @ 2.10GHz","cpus":64}}}`,
		"/api2/json/cluster/resources?type=vm": `{"data":[
		  {"uptime":2421124,"type":"qemu","id":"qemu/101","maxdisk":68719476736,"netout":475752853144,"status":"running","node":"pve-1","template":0,"netin":628308715878,"name":"k8s-cp-1","maxmem":25769803776,"maxcpu":8,"tags":"lab;k8s","cpu":0.265756734687807,"mem":21573279744,"vmid":101},
		  {"id":"qemu/9000","vmid":"9000","name":"tmpl","status":"stopped","template":"1","maxcpu":"2","maxmem":"2147483648","mem":0,"lock":"clone"}]}`,
	})
	tok := secret.New("secret-value", "")
	p := NewProxmox(s.URL, "ci@pve!dash", tok, nil)
	if !p.Enabled() || NewProxmox(s.URL, "", secret.Value{}, nil).Enabled() || NewProxmox(s.URL, "ci@pve!dash", secret.Value{}, nil).Enabled() {
		t.Fatal("Enabled")
	}
	h, err := p.NodeStatus(ctx, "pve-1")
	if err != nil {
		t.Fatal(err)
	}
	if h != (PVEHost{UptimeSeconds: 2422190, PVEVersion: "9.2.2", Kernel: "7.0.2-6-pve", CPUModel: "Example CPU @ 2.10GHz"}) {
		t.Fatalf("host: %+v", h)
	}
	vms, err := p.VMs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []PVEVM{
		{VMID: 101, Node: "pve-1", Type: "qemu", Name: "k8s-cp-1", Status: "running", Tags: []string{"lab", "k8s"}, CPUs: 8, CPURatio: 0.265756734687807,
			MemBytes: 21573279744, MaxMemBytes: 25769803776, UptimeSeconds: 2421124, NetInBytes: 628308715878, NetOutBytes: 475752853144},
		{VMID: 9000, Name: "tmpl", Status: "stopped", Tags: []string{}, CPUs: 2, MaxMemBytes: 2147483648, Template: true, Lock: "clone"},
	}
	if !reflect.DeepEqual(vms, want) {
		t.Fatalf("vms:\n%+v\nwant\n%+v", vms, want)
	}
	for _, a := range *auth {
		if a != "PVEAPIToken=ci@pve!dash=secret-value" {
			t.Fatalf("Authorization = %q", a)
		}
	}
	// A rotated secret (hot reload of a file ref) is on the very next request.
	tok.Update("rotated")
	*auth = nil
	if _, err := p.NodeStatus(ctx, "pve-1"); err != nil {
		t.Fatal(err)
	}
	if len(*auth) != 1 || (*auth)[0] != "PVEAPIToken=ci@pve!dash=rotated" {
		t.Fatalf("Authorization after rotation = %q", *auth)
	}

	if _, err := NewProxmox(s.URL, "", secret.Value{}, nil).VMs(ctx); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}

func TestProxmoxTasks(t *testing.T) {
	var tasks []string
	tasks = append(tasks, `{"upid":"UPID:run","type":"qmstart","id":"101","user":"root@pam","starttime":1793390000,"node":"pve-1"}`)
	for i := range 25 {
		tasks = append(tasks, `{"upid":"UPID:`+string(rune('a'+i))+`","type":"qmconfig","id":"101","user":"ci@pve","status":"OK","starttime":`+
			itoa(1793380000+i)+`,"endtime":`+itoa(1793380001+i)+`}`)
	}
	body := `{"data":[` + strings.Join(tasks, ",") + `]}`
	s, _ := pveServer(t, map[string]string{"/api2/json/cluster/tasks": body})
	got, err := NewProxmox(s.URL, "u@pve!t", secret.New("t", ""), nil).Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Fatalf("len = %d", len(got))
	}
	run := got[0]
	if run.UPID != "UPID:run" || run.Node != "pve-1" || run.Status != "running" || run.EndTime != nil || run.StartTime.Unix() != 1793390000 {
		t.Fatalf("running task: %+v", run)
	}
	if got[1].UPID != "UPID:"+string(rune('a'+24)) || got[1].Status != "OK" || got[1].EndTime == nil || got[1].EndTime.Unix() != 1793380025 {
		t.Fatalf("done task: %+v", got[1])
	}
	for i := 1; i < len(got); i++ {
		if got[i].StartTime.After(got[i-1].StartTime) {
			t.Fatal("not newest first")
		}
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestProxmoxVMConfig(t *testing.T) {
	s, _ := pveServer(t, map[string]string{
		"/api2/json/nodes/pve-1/qemu/101/config": `{"data":{"name":"k8s-cp-1","ostype":"l26","onboot":1,"cores":8,"sockets":1,"memory":"24576","balloon":24576,
		  "agent":"enabled=1,fstrim_cloned_disks=0,type=virtio","ide2":"local-zfs:vm-101-cloudinit,media=cdrom","scsi0":"local-zfs:vm-101-disk-0,size=64G",
		  "scsi10":"local-zfs:vm-999-pvc-z,size=1G","scsi2":"local-zfs:vm-999-pvc-a,size=1G","efidisk0":"local-zfs:vm-101-disk-1,size=1M",
		  "net1":"virtio=02:00:5e:00:53:21,bridge=vmbr1","net0":"virtio=02:00:5e:00:53:20,bridge=vmbr0,firewall=0","netfoo":"x","scsihw":"virtio-scsi-single","smbios1":"uuid=x"}}`,
		"/api2/json/nodes/pve-1/qemu/9/config": `{"data":{}}`,
	})
	p := NewProxmox(s.URL, "u@pve!t", secret.New("t", ""), nil)
	got, err := p.VMConfig(ctx, "pve-1", 101)
	if err != nil {
		t.Fatal(err)
	}
	want := model.VMConfig{
		Cores: 8, Sockets: 1, MemoryMiB: 24576, BalloonMiB: 24576, OSType: "l26",
		Agent: "enabled=1,fstrim_cloned_disks=0,type=virtio",
		Disks: []model.KeySpec{
			{Key: "efidisk0", Spec: "local-zfs:vm-101-disk-1,size=1M"},
			{Key: "ide2", Spec: "local-zfs:vm-101-cloudinit,media=cdrom"},
			{Key: "scsi0", Spec: "local-zfs:vm-101-disk-0,size=64G"},
			{Key: "scsi2", Spec: "local-zfs:vm-999-pvc-a,size=1G"},
			{Key: "scsi10", Spec: "local-zfs:vm-999-pvc-z,size=1G"},
		},
		NICs: []model.KeySpec{
			{Key: "net0", Spec: "virtio=02:00:5e:00:53:20,bridge=vmbr0,firewall=0"},
			{Key: "net1", Spec: "virtio=02:00:5e:00:53:21,bridge=vmbr1"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config:\n%+v\nwant\n%+v", got, want)
	}
	empty, err := p.VMConfig(ctx, "pve-1", 9)
	if err != nil || empty.Disks == nil || empty.NICs == nil || len(empty.Disks)+len(empty.NICs) != 0 {
		t.Fatalf("empty config: %+v %v", empty, err)
	}
	if _, err := p.VMConfig(ctx, "pve-1", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("vmid 0: %v", err)
	}
}

func TestProxmoxVMIPsAndSnapshots(t *testing.T) {
	s, _ := pveServer(t, map[string]string{
		"/api2/json/nodes/pve-1/qemu/101/agent/network-get-interfaces": `{"data":{"result":[
		  {"name":"lo","hardware-address":"00:00:00:00:00:00","ip-addresses":[{"ip-address-type":"ipv4","prefix":8,"ip-address":"127.0.0.1"},{"ip-address-type":"ipv6","prefix":128,"ip-address":"::1"}]},
		  {"name":"bond0","hardware-address":"02:00:5e:00:53:8a"},
		  {"name":"eth0","ip-addresses":[{"ip-address-type":"ipv4","prefix":24,"ip-address":"192.0.2.20"},{"ip-address-type":"ipv6","prefix":64,"ip-address":"fe80::1"}]}]}}`,
		"/api2/json/nodes/pve-1/qemu/101/snapshot": `{"data":[
		  {"name":"pre-upgrade","description":"before 1.13","snaptime":1793300000,"vmstate":0},
		  {"name":"no-time","description":""},
		  {"name":"current","description":"You are here!","digest":"6b93","running":1}]}`,
	})
	p := NewProxmox(s.URL, "u@pve!t", secret.New("t", ""), nil)

	ips := p.VMIPs(ctx, "pve-1", 101)
	want := []model.IfaceAddr{{Iface: "eth0", Address: "192.0.2.20"}, {Iface: "eth0", Address: "fe80::1"}}
	if !reflect.DeepEqual(ips, want) {
		t.Fatalf("ips: %+v", ips)
	}
	// No guest agent (501 from the server) or a bad id: empty, never nil.
	for _, id := range []int{102, -1} {
		if got := p.VMIPs(ctx, "pve-1", id); got == nil || len(got) != 0 {
			t.Fatalf("vm %d ips = %v", id, got)
		}
	}

	snaps, err := p.Snapshots(ctx, "pve-1", 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].Name != "pre-upgrade" || snaps[0].Description != "before 1.13" ||
		snaps[0].Time == nil || snaps[0].Time.Unix() != 1793300000 || snaps[1].Time != nil {
		t.Fatalf("snapshots: %+v", snaps)
	}
	if _, err := p.Snapshots(ctx, "pve-1", 102); err == nil {
		t.Fatal("want upstream error")
	}
}

// A cluster answer without its "cluster" entry is standalone only for one node with node id 0;
// otherwise it is transient (name and quorum unknown). A node or cluster name that breaks the
// Proxmox rule is dropped from every answer.
func TestProxmoxClusterStatusShapesAndNames(t *testing.T) {
	s, _ := pveServer(t, map[string]string{
		"/api2/json/cluster/status": `{"data":[{"type":"node","name":"pve-1","nodeid":1,"online":1},{"type":"node","name":"pve-2","nodeid":2,"online":1},
		  {"type":"node","name":"bad/name","nodeid":3,"online":1}]}`,
		"/api2/json/cluster/resources?type=node": `{"data":[{"node":"pve-1","status":"online"},{"node":"x y","status":"online"},{"node":"-a","status":"online"}]}`,
		"/api2/json/cluster/resources?type=vm":   `{"data":[{"vmid":7,"node":"a/b","name":"seven","status":"running"}]}`,
	})
	p := NewProxmox(s.URL, "u@pve!t", secret.New("t", ""), nil)
	st, err := p.ClusterStatus(ctx)
	if err != nil || !st.Transient || st.Standalone || st.Quorate || len(st.Nodes) != 2 {
		t.Errorf("cluster without its entry = %+v, %v; want transient with the two valid nodes", st, err)
	}
	u, err := p.NodeUsage(ctx)
	if err != nil || len(u) != 1 || u[0].Node != "pve-1" {
		t.Errorf("usage = %+v, %v; want pve-1 alone", u, err)
	}
	vms, err := p.VMs(ctx)
	if err != nil || len(vms) != 1 || vms[0].Node != "" {
		t.Errorf("vms = %+v, %v; want the guest without its invalid node", vms, err)
	}
	if _, err := p.NodeStatus(ctx, "../etc"); !errors.Is(err, ErrInvalid) {
		t.Errorf("NodeStatus of an invalid name: %v", err)
	}
}

// A call that addresses a node refuses without sending a request when it is given none: the plugin
// passes the node a VM runs on, and an empty one is a bug, not a request for the node the URL reaches.
func TestProxmoxCallsWithoutANodeSendNothing(t *testing.T) {
	var hits int
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		io.WriteString(w, `{"data":[]}`)
	})
	p := NewProxmox(s.URL, "u@pve!t", secret.New("t", ""), nil)
	if _, err := p.NodeStatus(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("NodeStatus: %v", err)
	}
	if _, err := p.VMConfig(ctx, "", 101); !errors.Is(err, ErrInvalid) {
		t.Errorf("VMConfig: %v", err)
	}
	if _, err := p.Snapshots(ctx, "", 101); !errors.Is(err, ErrInvalid) {
		t.Errorf("Snapshots: %v", err)
	}
	if ips := p.VMIPs(ctx, "", 101); len(ips) != 0 {
		t.Errorf("VMIPs: %v", ips)
	}
	if hits != 0 {
		t.Fatalf("%d requests went out for calls without a node", hits)
	}
}

func TestProxmoxStorages(t *testing.T) {
	s, _ := pveServer(t, map[string]string{"/api2/json/cluster/resources?type=storage": `{"data":[
	  {"id":"storage/pve-2/tank","storage":"tank","node":"pve-2","shared":0,"maxdisk":1000,"disk":250},
	  {"id":"storage/pve-1/local","storage":"local","node":"pve-1","shared":"0","maxdisk":"4000","disk":"1000"},
	  {"id":"storage/pve-1/nas","storage":"nas","node":"pve-1","shared":1,"maxdisk":9000,"disk":3000},
	  {"id":"storage/pve-2/nas","storage":"nas","node":"pve-2","shared":1,"status":"unknown"},
	  {"storage":"","node":"pve-1","maxdisk":5},
	  {"storage":"evil","node":"../x","maxdisk":5}]}`})
	got, err := NewProxmox(s.URL, "u@pve!t", secret.New("t", ""), nil).Storages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []PVEStorage{
		{Name: "local", Node: "pve-1", UsedBytes: 1000, SizeBytes: 4000},
		{Name: "nas", Node: "pve-1", Shared: true, UsedBytes: 3000, SizeBytes: 9000},
		{Name: "tank", Node: "pve-2", UsedBytes: 250, SizeBytes: 1000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storages = %+v, want %+v (by node and name, sizeless and unnamed ones left out)", got, want)
	}
	// An empty answer is an empty list, never nil.
	s2, _ := pveServer(t, map[string]string{"/api2/json/cluster/resources?type=storage": `{"data":[]}`})
	if got, err := NewProxmox(s2.URL, "u@pve!t", secret.New("t", ""), nil).Storages(ctx); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty storages = %#v, %v", got, err)
	}
}

// The options are the plugin client's: its timeout and its redirect policy reach the requests.
func TestProxmoxHonoursTheClientOptions(t *testing.T) {
	block := make(chan struct{})
	slow := serve(t, func(w http.ResponseWriter, r *http.Request) { <-block })
	defer close(block)
	p := NewProxmox(slow.URL, "u@pve!t", secret.New("t", ""), nil, Timeout(30*time.Millisecond))
	start := time.Now()
	_, err := p.ClusterStatus(ctx)
	if err == nil || Category(err) != CategoryTimeout || time.Since(start) > 2*time.Second {
		t.Fatalf("a slow Proxmox: %v (%s) after %v", err, Category(err), time.Since(start))
	}

	var refused bool
	target := serve(t, func(w http.ResponseWriter, r *http.Request) { t.Error("the redirect was followed") })
	redirecting := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
	p = NewProxmox(redirecting.URL, "u@pve!t", secret.New("t", ""), nil,
		CheckRedirect(func(*http.Request, []*http.Request) error { refused = true; return errors.New("redirect refused") }))
	if _, err := p.ClusterStatus(ctx); err == nil || !refused {
		t.Fatalf("redirect: err = %v, policy consulted = %v", err, refused)
	}
}

// Both generations of Proxmox answer in the shapes the client reads: the cluster (one node offline),
// a standalone server, the nodes' usage and facts, the guests on both nodes (numbers that arrive as
// strings or booleans included), the config, the guest-agent addresses, the snapshots, every node's
// storages (one shared) and the tasks, from recorded responses.
func TestProxmoxDecodesPVE8AndPVE9(t *testing.T) {
	type expect struct {
		cluster  PVEClusterStatus
		usage    []PVENodeUsage
		host     PVEHost
		vms      []PVEVM
		cfg      model.VMConfig
		ips      []model.IfaceAddr
		snaps    []string
		storages []PVEStorage
	}
	disks := []model.KeySpec{
		{Key: "ide2", Spec: "local:iso/ci.iso,media=cdrom"},
		{Key: "scsi0", Spec: "local-zfs:vm-101-disk-0,iothread=1,size=64G"},
		{Key: "scsi1", Spec: "local-zfs:vm-101-disk-1,size=10G"},
	}
	nics := []model.KeySpec{{Key: "net0", Spec: "virtio=02:00:5e:00:53:01,bridge=vmbr0,firewall=1"}}
	cfg := model.VMConfig{Cores: 4, Sockets: 1, MemoryMiB: 8192, BalloonMiB: 4096, OSType: "l26",
		Agent: "enabled=1,fstrim_cloned_disks=1", Disks: disks, NICs: nics}
	k8s := []string{"k8s", "control-plane"}
	vms := []PVEVM{
		{VMID: 101, Node: "pve-1", Type: "qemu", Name: "k8s-cp-1", Status: "running", Tags: k8s, CPUs: 4, CPURatio: 0.0873, MemBytes: 4017299456, MaxMemBytes: 8589934592, UptimeSeconds: 86400, NetInBytes: 1250000, NetOutBytes: 730000},
		{VMID: 102, Node: "pve-2", Type: "qemu", Name: "db-1", Status: "stopped", Tags: []string{"lab"}, CPUs: 2, MaxMemBytes: 4294967296, Lock: "backup"},
		// pve-2 has been offline long enough for the cluster to forget its guests' details.
		{VMID: 104, Node: "pve-2", Type: "qemu", Status: "unknown", Tags: []string{}},
		{VMID: 9000, Node: "pve-1", Type: "qemu", Name: "k8s-template", Status: "stopped", Tags: []string{"template"}, CPUs: 2, MaxMemBytes: 2147483648, Template: true},
	}
	nodes := []PVEClusterNode{{Name: "pve-1", NodeID: 1, Online: true}, {Name: "pve-2", NodeID: 2}}
	for _, tc := range []struct {
		dir  string
		want expect
	}{
		{"pve8", expect{
			cluster: PVEClusterStatus{Name: "lab", Quorate: true, Nodes: nodes},
			usage: []PVENodeUsage{
				{Node: "pve-1", Status: "online", CPURatio: 0.0312, Threads: 8, Mem: 9123348480, MaxMem: 33564844032, Disk: 4415053824, MaxDisk: 100861726720, Uptime: 864203},
				{Node: "pve-2", Status: "offline"},
			},
			host: PVEHost{UptimeSeconds: 864203, PVEVersion: "8.4.1", Kernel: "6.8.12-9-pve", CPUModel: "Example CPU @ 2.20GHz", Sockets: 1, Cores: 4},
			vms:  vms,
			cfg:  cfg, ips: []model.IfaceAddr{{Iface: "eth0", Address: "192.0.2.21"}, {Iface: "eth0", Address: "fe80::1"}},
			snaps: []string{"pre-upgrade"},
			storages: []PVEStorage{
				{Name: "local", Node: "pve-1", Status: "available", UsedBytes: 4415053824, SizeBytes: 100861726720},
				{Name: "local-zfs", Node: "pve-1", Status: "available", UsedBytes: 651000000000, SizeBytes: 1829000000000},
				{Name: "nas", Node: "pve-1", Status: "available", Shared: true, UsedBytes: 2000000000000, SizeBytes: 8000000000000},
				{Name: "nas", Node: "pve-2", Status: "available", Shared: true, UsedBytes: 2000000000000, SizeBytes: 8000000000000},
			},
		}},
		{"pve9", expect{
			cluster: PVEClusterStatus{Name: "lab", Quorate: false, Nodes: nodes},
			usage: []PVENodeUsage{
				{Node: "pve-1", Status: "online", CPURatio: 0.0425, Threads: 16, Mem: 12884901888, MaxMem: 67108864000, Disk: 5415053824, MaxDisk: 100861726720, Uptime: 2422190},
				{Node: "pve-2", Status: "offline", Threads: 16, MaxMem: 67108864000},
			},
			host: PVEHost{UptimeSeconds: 2422190, PVEVersion: "9.0.5", Kernel: "6.14.8-2-pve", CPUModel: "Example CPU @ 2.10GHz", Sockets: 1, Cores: 8},
			vms:  vms,
			cfg:  cfg, ips: []model.IfaceAddr{{Iface: "eth0", Address: "192.0.2.21"}, {Iface: "eth0", Address: "fe80::1"}, {Iface: "tailscale0", Address: "198.51.100.7"}},
			snaps: []string{"pre-upgrade", "no-time"},
			storages: []PVEStorage{
				{Name: "local", Node: "pve-1", Status: "available", UsedBytes: 5415053824, SizeBytes: 100861726720},
				{Name: "local-zfs", Node: "pve-1", Status: "available", UsedBytes: 651000000000, SizeBytes: 1829000000000},
				{Name: "nas", Node: "pve-1", Status: "available", Shared: true, UsedBytes: 2000000000000, SizeBytes: 8000000000000},
				{Name: "nas", Node: "pve-2", Status: "available", Shared: true, UsedBytes: 2000000000000, SizeBytes: 8000000000000},
				{Name: "tank", Node: "pve-2", Status: "available", UsedBytes: 4100000000000, SizeBytes: 10900000000000},
			},
		}},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			read := func(name string) string {
				b, err := os.ReadFile("testdata/proxmox/" + tc.dir + "/" + name)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			s, _ := pveServer(t, map[string]string{
				"/api2/json/cluster/status":                                    read("cluster_status.json"),
				"/api2/json/cluster/resources?type=node":                       read("cluster_resources_node.json"),
				"/api2/json/cluster/resources?type=storage":                    read("cluster_resources_storage.json"),
				"/api2/json/cluster/resources?type=vm":                         read("cluster_resources.json"),
				"/api2/json/cluster/tasks":                                     read("cluster_tasks.json"),
				"/api2/json/nodes/pve-1/status":                                read("nodes_status.json"),
				"/api2/json/nodes/pve-1/qemu/101/config":                       read("qemu_config.json"),
				"/api2/json/nodes/pve-1/qemu/101/agent/network-get-interfaces": read("agent_network.json"),
				"/api2/json/nodes/pve-1/qemu/101/snapshot":                     read("snapshot.json"),
			})
			p := NewProxmox(s.URL, "ci@pve!dash", secret.New("s", ""), nil)

			if got, err := p.ClusterStatus(ctx); err != nil || !reflect.DeepEqual(got, tc.want.cluster) {
				t.Errorf("cluster = %+v, %v; want %+v", got, err, tc.want.cluster)
			}
			if got, err := p.NodeUsage(ctx); err != nil || !reflect.DeepEqual(got, tc.want.usage) {
				t.Errorf("usage = %+v, %v\nwant %+v", got, err, tc.want.usage)
			}
			if got, err := p.NodeStatus(ctx, "pve-1"); err != nil || got != tc.want.host {
				t.Errorf("host = %+v, %v; want %+v", got, err, tc.want.host)
			}
			if got, err := p.VMs(ctx); err != nil || !reflect.DeepEqual(got, tc.want.vms) {
				t.Errorf("vms = %+v, %v\nwant %+v", got, err, tc.want.vms)
			}
			if got, err := p.VMConfig(ctx, "pve-1", 101); err != nil || !reflect.DeepEqual(got, tc.want.cfg) {
				t.Errorf("config = %+v, %v\nwant %+v", got, err, tc.want.cfg)
			}
			if got := p.VMIPs(ctx, "pve-1", 101); !reflect.DeepEqual(got, tc.want.ips) {
				t.Errorf("ips = %+v, want %+v", got, tc.want.ips)
			}
			snaps, err := p.Snapshots(ctx, "pve-1", 101)
			var names []string
			for _, sn := range snaps {
				names = append(names, sn.Name)
			}
			if err != nil || !reflect.DeepEqual(names, tc.want.snaps) {
				t.Errorf("snapshots = %v, %v; want %v", names, err, tc.want.snaps)
			}
			if got, err := p.Storages(ctx); err != nil || !reflect.DeepEqual(got, tc.want.storages) {
				t.Errorf("storages = %+v, %v\nwant %+v", got, err, tc.want.storages)
			}
			tasks, err := p.Tasks(ctx)
			if err != nil || len(tasks) != 2 || tasks[0].Node != "pve-2" || tasks[0].Status != "running" ||
				tasks[1].Node != "pve-1" || tasks[1].Status != "OK" || tasks[1].EndTime == nil {
				t.Errorf("tasks = %+v, %v; want pve-2's running vzdump, then pve-1's finished qmstart", tasks, err)
			}

			// A standalone server has no cluster entry: a cluster of one, without a name, and quorate.
			alone, _ := pveServer(t, map[string]string{"/api2/json/cluster/status": read("cluster_status_standalone.json")})
			want := PVEClusterStatus{Standalone: true, Quorate: true, Nodes: []PVEClusterNode{{Name: "pve-1", NodeID: 0, Online: true}}}
			if got, err := NewProxmox(alone.URL, "ci@pve!dash", secret.New("s", ""), nil).ClusterStatus(ctx); err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("standalone = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}
