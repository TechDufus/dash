package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

var ctx = context.Background()

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestPromInstant(t *testing.T) {
	var gotForm string
	var gotMethod, gotPath string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotForm, gotMethod, gotPath = string(b), r.Method, r.URL.Path
		io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"__name__":"up","instance":"10.0.20.20:2381","job":"etcd","node":"talos-cp-1"},"value":[1793396394,"1"]},
			{"metric":{},"value":[1793396394.5,"0.25"]},
			{"metric":{"job":"nan"},"value":[1793396394,"NaN"]},
			{"metric":{"job":"inf"},"value":[1793396394,"+Inf"]}]}}`)
	})
	got, err := NewProm(s.URL, nil).Instant(ctx, `up{job="etcd"}`)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/query" || gotForm != "query=up%7Bjob%3D%22etcd%22%7D" {
		t.Fatalf("request: %s %s %q", gotMethod, gotPath, gotForm)
	}
	want := []Sample{
		{Labels: map[string]string{"__name__": "up", "instance": "10.0.20.20:2381", "job": "etcd", "node": "talos-cp-1"}, Value: 1},
		{Labels: map[string]string{}, Value: 0.25},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestPromErrors(t *testing.T) {
	body, code := "", 200
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		io.WriteString(w, body)
	})
	p := NewProm(s.URL, nil)
	for name, tc := range map[string]struct {
		code int
		body string
	}{
		"promql error":   {200, `{"status":"error","errorType":"bad_data","error":"parse error"}`},
		"http error":     {422, `{"status":"error"}`},
		"wrong type":     {200, `{"status":"success","data":{"resultType":"matrix","result":[]}}`},
		"garbage":        {200, `<html>`},
		"empty document": {200, ``},
	} {
		t.Run(name, func(t *testing.T) {
			code, body = tc.code, tc.body
			if _, err := p.Instant(ctx, "x"); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := NewProm("", nil).Instant(ctx, "x"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestPromRange(t *testing.T) {
	var form string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form = string(b)
		io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"instance":"a"},"values":[[1793396000,"1.5"],[1793396060,"NaN"],[1793396120,"2"]]},
			{"metric":{"instance":"b"},"values":[[1793396000,"+Inf"]]}]}}`)
	})
	start := time.Unix(1793396000, 0)
	got, err := NewProm(s.URL, nil).Range(ctx, "up", start, start.Add(time.Hour), 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if form != "end=1793399600&query=up&start=1793396000&step=60" {
		t.Fatalf("form = %q", form)
	}
	want := []model.SeriesLine{
		{Labels: map[string]string{"instance": "a"}, Points: [][2]float64{{1793396000, 1.5}, {1793396120, 2}}},
		{Labels: map[string]string{"instance": "b"}, Points: [][2]float64{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

const amAlertsJSON = `[
 {"annotations":{"description":"always firing","summary":"Watchdog"},"endsAt":"2026-10-30T21:41:17.226Z","fingerprint":"e2306103db8679b0","receivers":[{"name":"healthchecks"}],"startsAt":"2026-10-02T21:15:20.000Z","status":{"inhibitedBy":[],"mutedBy":[],"silencedBy":[],"state":"active"},"updatedAt":"2026-10-30T21:39:57.228Z","generatorURL":"http:/explore","labels":{"alertgroup":"general.rules","alertname":"Watchdog","severity":"none"}},
 {"annotations":{},"fingerprint":"1","startsAt":"2026-10-02T21:15:20.000Z","status":{"inhibitedBy":[],"silencedBy":[],"state":"active"},"labels":{"alertname":"InfoInhibitor","severity":"none"}},
 {"annotations":{"description":"DIMM B8 ECC","summary":"IPMI SEL"},"fingerprint":"abc123","startsAt":"2026-10-30T03:14:30.000Z","status":{"inhibitedBy":null,"silencedBy":["5b1c2d3e-aaaa-bbbb-cccc-0123456789ab"],"state":"suppressed"},"labels":{"alertname":"ProxmoxIpmiSelCriticalEntry","severity":"warning","instance":"pve-730"}}
]`

func TestAlertmanagerAlerts(t *testing.T) {
	var rawQuery string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		if r.URL.Path != "/api/v2/alerts" {
			t.Errorf("path %s", r.URL.Path)
		}
		io.WriteString(w, amAlertsJSON)
	})
	got, err := NewAlertmanager(s.URL, nil).Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rawQuery != "active=true&inhibited=true&silenced=true" {
		t.Fatalf("query = %q", rawQuery)
	}
	if len(got) != 1 {
		t.Fatalf("Watchdog/InfoInhibitor not dropped: %+v", got)
	}
	a := got[0]
	if a.Fingerprint != "abc123" || a.Name != "ProxmoxIpmiSelCriticalEntry" || a.Severity != "warning" ||
		a.State != "suppressed" || a.Summary != "IPMI SEL" || a.Description != "DIMM B8 ECC" ||
		a.Labels["instance"] != "pve-730" || !a.StartsAt.Equal(time.Date(2026, 10, 30, 3, 14, 30, 0, time.UTC)) ||
		len(a.SilencedBy) != 1 || a.InhibitedBy == nil || len(a.InhibitedBy) != 0 {
		t.Fatalf("alert: %+v", a)
	}

	// An empty answer is an empty non-nil slice.
	s2 := serve(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `[]`) })
	empty, err := NewAlertmanager(s2.URL, nil).Alerts(ctx)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %v, %v", empty, err)
	}
}

func TestAlertmanagerSilences(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[
		 {"id":"11111111-2222","status":{"state":"active"},"matchers":[{"name":"alertname","value":"Foo","isRegex":false,"isEqual":true},{"name":"ns","value":"a|b","isRegex":true,"isEqual":false}],"createdBy":"dash","comment":"c","startsAt":"2026-10-30T20:00:00Z","endsAt":"2026-10-30T22:00:00Z"},
		 {"id":"33333333-4444","status":{"state":"pending"},"matchers":[],"createdBy":"x","comment":"","startsAt":"2026-10-31T20:00:00Z","endsAt":"2026-10-31T22:00:00Z"},
		 {"id":"55555555-6666","status":{"state":"expired"},"matchers":[],"startsAt":"2026-10-01T20:00:00Z","endsAt":"2026-10-01T22:00:00Z"}]`)
	})
	got, err := NewAlertmanager(s.URL, nil).Silences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "11111111-2222" || got[1].State != "pending" {
		t.Fatalf("got %+v", got)
	}
	wantM := []model.Matcher{{Name: "alertname", Value: "Foo", IsEqual: true}, {Name: "ns", Value: "a|b", IsRegex: true}}
	if !reflect.DeepEqual(got[0].Matchers, wantM) || got[1].Matchers == nil {
		t.Fatalf("matchers: %+v / %+v", got[0].Matchers, got[1].Matchers)
	}
}

func TestAlertmanagerCreateSilence(t *testing.T) {
	var body map[string]any
	var method, path, ctype string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"silenceID":"abcdef12-3456"}`)
	})
	start := time.Date(2026, 10, 30, 22, 0, 0, 0, time.FixedZone("CDT", -5*3600))
	id, err := NewAlertmanager(s.URL, nil).CreateSilence(ctx, SilenceSpec{
		Matchers:  []model.SilenceMatcher{{Name: "alertname", Value: "Foo"}, {Name: "namespace", Value: "gatus"}},
		StartsAt:  start,
		EndsAt:    start.Add(4 * time.Hour),
		CreatedBy: "techdufus",
		Comment:   "maintenance",
	})
	if err != nil || id != "abcdef12-3456" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if method != http.MethodPost || path != "/api/v2/silences" || ctype != "application/json" {
		t.Fatalf("request: %s %s %s", method, path, ctype)
	}
	want := map[string]any{
		"matchers": []any{
			map[string]any{"name": "alertname", "value": "Foo", "isRegex": false, "isEqual": true},
			map[string]any{"name": "namespace", "value": "gatus", "isRegex": false, "isEqual": true},
		},
		"startsAt":  "2026-10-31T03:00:00Z",
		"endsAt":    "2026-10-31T07:00:00Z",
		"createdBy": "techdufus",
		"comment":   "maintenance",
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v\nwant %v", body, want)
	}
	if _, err := NewAlertmanager(s.URL, nil).CreateSilence(ctx, SilenceSpec{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no matchers: %v", err)
	}
}

func TestAlertmanagerExpireSilence(t *testing.T) {
	var method, path string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	am := NewAlertmanager(s.URL, nil)
	if err := am.ExpireSilence(ctx, "5b1c2d3e-aaaa-bbbb-cccc-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/api/v2/silence/5b1c2d3e-aaaa-bbbb-cccc-0123456789ab" {
		t.Fatalf("request: %s %s", method, path)
	}
	for _, id := range []string{"", "short", "../../x", "abc/def-1234", "zzzzzzzz"} {
		method = ""
		if err := am.ExpireSilence(ctx, id); !errors.Is(err, ErrInvalid) || method != "" {
			t.Errorf("id %q: err=%v, request sent=%q", id, err, method)
		}
	}

	missing := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "silence not found", http.StatusNotFound)
	})
	err := NewAlertmanager(missing.URL, nil).ExpireSilence(ctx, "5b1c2d3e-aaaa")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 || se.Body != "silence not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestGatusStatuses(t *testing.T) {
	var rawQuery string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		io.WriteString(w, `[
		 {"name":"Grafana","group":"apps","key":"apps_grafana","results":[
		   {"status":200,"hostname":"grafana.home.techdufus.com","duration":10172175,"conditionResults":[{"condition":"[STATUS] == 200","success":true}],"success":true,"timestamp":"2026-10-30T21:10:15.668367726Z"},
		   {"status":500,"hostname":"grafana.home.techdufus.com","duration":2500000,"success":false,"timestamp":"2026-10-30T21:40:15.668367726Z"}]},
		 {"name":"New","group":"apps","key":"apps_new","results":[]}]`)
	})
	got, err := NewGatus(s.URL, nil).Statuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rawQuery != "page=1&pageSize=30" {
		t.Fatalf("query = %q", rawQuery)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	g := got[0]
	if g.Group != "apps" || g.Name != "Grafana" || g.Key != "apps_grafana" || g.URL != "grafana.home.techdufus.com" ||
		g.Healthy || len(g.Results) != 2 || !g.Results[0].Success || g.Results[0].DurationMs != 10.172175 ||
		!g.Results[0].Timestamp.Before(g.Results[1].Timestamp) {
		t.Fatalf("endpoint: %+v", g)
	}
	if got[1].Results == nil || got[1].Healthy || got[1].URL != "" {
		t.Fatalf("empty endpoint: %+v", got[1])
	}
}

func TestGatusPaging(t *testing.T) {
	pages := 0
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		if r.URL.Query().Get("page") == "1" {
			list := make([]gatusStatus, gatusPageSize)
			json.NewEncoder(w).Encode(list)
			return
		}
		io.WriteString(w, `[{"name":"last","group":"g","key":"g_last","results":[]}]`)
	})
	got, err := NewGatus(s.URL, nil).Statuses(ctx)
	if err != nil || len(got) != gatusPageSize+1 || pages != 2 {
		t.Fatalf("len=%d pages=%d err=%v", len(got), pages, err)
	}
}

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
	p := NewProxmox(s.URL, "pve-730", "csi@pve!homelab=secret-value", nil)
	if !p.Enabled() || NewProxmox(s.URL, "n", "", nil).Enabled() {
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

	if _, err := NewProxmox(s.URL, "pve-730", "", nil).VMs(ctx); !errors.Is(err, ErrDisabled) {
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
	got, err := NewProxmox(s.URL, "pve-730", "t", nil).Tasks(ctx)
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
	p := NewProxmox(s.URL, "pve-730", "t", nil)
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
	p := NewProxmox(s.URL, "pve-730", "t", nil)

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
