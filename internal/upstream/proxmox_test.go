package upstream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

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
		"/api2/json/nodes/pve-730/status": `{"data":{"uptime":2422190,"kversion":"Linux 7.0.2-6-pve #1 SMP","pveversion":"pve-manager/9.2.2/b9984c6d90a4bd80","current-kernel":{"release":"7.0.2-6-pve"},"cpuinfo":{"model":"Intel(R) Xeon(R) CPU E5-2683 v4 @ 2.10GHz","cpus":64}}}`,
		"/api2/json/cluster/resources?type=vm": `{"data":[
		  {"uptime":2421124,"type":"qemu","id":"qemu/2020","maxdisk":68719476736,"netout":475752853144,"status":"running","node":"pve-730","template":0,"netin":628308715878,"name":"talos-cp-1","maxmem":25769803776,"maxcpu":8,"tags":"homelab;talos","cpu":0.265756734687807,"mem":21573279744,"vmid":2020},
		  {"id":"qemu/9000","vmid":"9000","name":"tmpl","status":"stopped","template":"1","maxcpu":"2","maxmem":"2147483648","mem":0,"lock":"clone"}]}`,
	})
	tok := secret.New("secret-value", "")
	p := NewProxmox(s.URL, "pve-730", "csi@pve!homelab", tok, nil)
	if !p.Enabled() || NewProxmox(s.URL, "n", "", secret.Value{}, nil).Enabled() || NewProxmox(s.URL, "n", "csi@pve!homelab", secret.Value{}, nil).Enabled() {
		t.Fatal("Enabled")
	}
	h, err := p.NodeStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h != (PVEHost{UptimeSeconds: 2422190, PVEVersion: "9.2.2", Kernel: "7.0.2-6-pve", CPUModel: "Intel(R) Xeon(R) CPU E5-2683 v4 @ 2.10GHz"}) {
		t.Fatalf("host: %+v", h)
	}
	vms, err := p.VMs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []PVEVM{
		{VMID: 2020, Name: "talos-cp-1", Status: "running", Tags: []string{"homelab", "talos"}, CPUs: 8, CPURatio: 0.265756734687807,
			MemBytes: 21573279744, MaxMemBytes: 25769803776, UptimeSeconds: 2421124, NetInBytes: 628308715878, NetOutBytes: 475752853144},
		{VMID: 9000, Name: "tmpl", Status: "stopped", Tags: []string{}, CPUs: 2, MaxMemBytes: 2147483648, Template: true, Lock: "clone"},
	}
	if !reflect.DeepEqual(vms, want) {
		t.Fatalf("vms:\n%+v\nwant\n%+v", vms, want)
	}
	for _, a := range *auth {
		if a != "PVEAPIToken=csi@pve!homelab=secret-value" {
			t.Fatalf("Authorization = %q", a)
		}
	}
	// A rotated secret (hot reload of a file ref) is on the very next request.
	tok.Update("rotated")
	*auth = nil
	if _, err := p.NodeStatus(ctx); err != nil {
		t.Fatal(err)
	}
	if len(*auth) != 1 || (*auth)[0] != "PVEAPIToken=csi@pve!homelab=rotated" {
		t.Fatalf("Authorization after rotation = %q", *auth)
	}

	if _, err := NewProxmox(s.URL, "pve-730", "", secret.Value{}, nil).VMs(ctx); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}

func TestProxmoxTasks(t *testing.T) {
	var tasks []string
	tasks = append(tasks, `{"upid":"UPID:run","type":"qmstart","id":"2020","user":"root@pam","starttime":1793390000,"node":"pve-730"}`)
	for i := range 25 {
		tasks = append(tasks, `{"upid":"UPID:`+string(rune('a'+i))+`","type":"qmconfig","id":"2020","user":"csi@pve","status":"OK","starttime":`+
			itoa(1793380000+i)+`,"endtime":`+itoa(1793380001+i)+`}`)
	}
	body := `{"data":[` + strings.Join(tasks, ",") + `]}`
	s, _ := pveServer(t, map[string]string{"/api2/json/cluster/tasks": body})
	got, err := NewProxmox(s.URL, "pve-730", "u@pve!t", secret.New("t", ""), nil).Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Fatalf("len = %d", len(got))
	}
	run := got[0]
	if run.UPID != "UPID:run" || run.Status != "running" || run.EndTime != nil || run.StartTime.Unix() != 1793390000 {
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
		"/api2/json/nodes/pve-730/qemu/2020/config": `{"data":{"name":"talos-cp-1","ostype":"l26","onboot":1,"cores":8,"sockets":1,"memory":"24576","balloon":24576,
		  "agent":"enabled=1,fstrim_cloned_disks=0,type=virtio","ide2":"fast-zfs:vm-2020-cloudinit,media=cdrom","scsi0":"fast-zfs:vm-2020-disk-0,size=64G",
		  "scsi10":"fast-zfs:vm-9999-pvc-z,size=1G","scsi2":"fast-zfs:vm-9999-pvc-a,size=1G","efidisk0":"fast-zfs:vm-2020-disk-1,size=1M",
		  "net1":"virtio=02:0a:00:14:00:21,bridge=vmbr1","net0":"virtio=02:0a:00:14:00:20,bridge=vmbr0,firewall=0","netfoo":"x","scsihw":"virtio-scsi-single","smbios1":"uuid=x"}}`,
		"/api2/json/nodes/pve-730/qemu/9/config": `{"data":{}}`,
	})
	p := NewProxmox(s.URL, "pve-730", "u@pve!t", secret.New("t", ""), nil)
	got, err := p.VMConfig(ctx, 2020)
	if err != nil {
		t.Fatal(err)
	}
	want := model.VMConfig{
		Cores: 8, Sockets: 1, MemoryMiB: 24576, BalloonMiB: 24576, OSType: "l26",
		Agent: "enabled=1,fstrim_cloned_disks=0,type=virtio",
		Disks: []model.KeySpec{
			{Key: "efidisk0", Spec: "fast-zfs:vm-2020-disk-1,size=1M"},
			{Key: "ide2", Spec: "fast-zfs:vm-2020-cloudinit,media=cdrom"},
			{Key: "scsi0", Spec: "fast-zfs:vm-2020-disk-0,size=64G"},
			{Key: "scsi2", Spec: "fast-zfs:vm-9999-pvc-a,size=1G"},
			{Key: "scsi10", Spec: "fast-zfs:vm-9999-pvc-z,size=1G"},
		},
		NICs: []model.KeySpec{
			{Key: "net0", Spec: "virtio=02:0a:00:14:00:20,bridge=vmbr0,firewall=0"},
			{Key: "net1", Spec: "virtio=02:0a:00:14:00:21,bridge=vmbr1"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config:\n%+v\nwant\n%+v", got, want)
	}
	empty, err := p.VMConfig(ctx, 9)
	if err != nil || empty.Disks == nil || empty.NICs == nil || len(empty.Disks)+len(empty.NICs) != 0 {
		t.Fatalf("empty config: %+v %v", empty, err)
	}
	if _, err := p.VMConfig(ctx, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("vmid 0: %v", err)
	}
}

func TestProxmoxVMIPsAndSnapshots(t *testing.T) {
	s, _ := pveServer(t, map[string]string{
		"/api2/json/nodes/pve-730/qemu/2020/agent/network-get-interfaces": `{"data":{"result":[
		  {"name":"lo","hardware-address":"00:00:00:00:00:00","ip-addresses":[{"ip-address-type":"ipv4","prefix":8,"ip-address":"127.0.0.1"},{"ip-address-type":"ipv6","prefix":128,"ip-address":"::1"}]},
		  {"name":"bond0","hardware-address":"da:65:d3:b5:d7:8a"},
		  {"name":"eth0","ip-addresses":[{"ip-address-type":"ipv4","prefix":24,"ip-address":"10.0.20.20"},{"ip-address-type":"ipv6","prefix":64,"ip-address":"fe80::1"}]}]}}`,
		"/api2/json/nodes/pve-730/qemu/2020/snapshot": `{"data":[
		  {"name":"pre-upgrade","description":"before 1.13","snaptime":1793300000,"vmstate":0},
		  {"name":"no-time","description":""},
		  {"name":"current","description":"You are here!","digest":"6b93","running":1}]}`,
	})
	p := NewProxmox(s.URL, "pve-730", "u@pve!t", secret.New("t", ""), nil)

	ips := p.VMIPs(ctx, 2020)
	want := []model.IfaceAddr{{Iface: "eth0", Address: "10.0.20.20"}, {Iface: "eth0", Address: "fe80::1"}}
	if !reflect.DeepEqual(ips, want) {
		t.Fatalf("ips: %+v", ips)
	}
	// No guest agent (501 from the server) or a bad id: empty, never nil.
	for _, id := range []int{2021, -1} {
		if got := p.VMIPs(ctx, id); got == nil || len(got) != 0 {
			t.Fatalf("vm %d ips = %v", id, got)
		}
	}

	snaps, err := p.Snapshots(ctx, 2020)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].Name != "pre-upgrade" || snaps[0].Description != "before 1.13" ||
		snaps[0].Time == nil || snaps[0].Time.Unix() != 1793300000 || snaps[1].Time != nil {
		t.Fatalf("snapshots: %+v", snaps)
	}
	if _, err := p.Snapshots(ctx, 2021); err == nil {
		t.Fatal("want upstream error")
	}
}
