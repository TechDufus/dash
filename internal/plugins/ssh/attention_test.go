package ssh

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// The rule table: each state's item, and the items of a host that is up; the health is the worst
// of them.
func TestAttentionAndHealth(t *testing.T) {
	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	host := func(name string, state sshmodel.HostState, info *sshmodel.Info) sshmodel.Host {
		h := sshmodel.Host{Ref: plugin.Ref(ID, "Host", name), Name: name, State: state, Since: since, Info: info}
		h.Health = string(healthOf(h))
		return h
	}
	full := &sshmodel.Info{
		Filesystems: []sshmodel.Filesystem{
			{Mount: "/", UsedBytes: 50, TotalBytes: 100},
			{Mount: "/data", UsedBytes: 90, TotalBytes: 100},
			{Mount: "/var", UsedBytes: 96, TotalBytes: 100},
			{Mount: "/empty", UsedBytes: 0, TotalBytes: 0},
		},
		Pools:    []sshmodel.Pool{{Name: "tank", Health: "ONLINE"}, {Name: "cold", Health: "DEGRADED"}},
		Services: &sshmodel.Services{Running: 3, Failed: []string{"a.service", "b.service"}},
	}
	hosts := []sshmodel.Host{
		host("up", sshmodel.StateUp, full),
		host("away", sshmodel.StateUnreachable, full), // its stale inventory raises nothing
		host("refused", sshmodel.StateUnauthorized, nil),
		host("moved", sshmodel.StateKeyChanged, nil),
		host("new", sshmodel.StateUnverified, nil),
		host("bsd", sshmodel.StateUnsupported, nil),
		host("fine", sshmodel.StateUp, &sshmodel.Info{Filesystems: []sshmodel.Filesystem{{Mount: "/", UsedBytes: 1, TotalBytes: 2}}}),
	}
	type row struct {
		id    string
		sev   model.Severity
		since time.Time
	}
	var got []row
	for _, it := range attentionOf(hosts) {
		got = append(got, row{it.ID, it.Severity, it.Since})
		if it.Plugin != "ssh" || it.Source != "ssh" || it.Ref == "" || it.Links == nil {
			t.Errorf("item %+v", it)
		}
	}
	want := []row{
		{"ssh:fs:up//data", model.SeverityWarn, time.Time{}},
		{"ssh:fs:up//var", model.SeverityCrit, time.Time{}},
		{"ssh:units:up", model.SeverityWarn, time.Time{}},
		{"ssh:pool:up/cold", model.SeverityCrit, time.Time{}},
		{"ssh:host:away", model.SeverityWarn, since},
		{"ssh:host:refused", model.SeverityWarn, since},
		{"ssh:host:moved", model.SeverityCrit, since},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("items =\n%+v\nwant\n%+v", got, want)
	}
	for name, h := range map[string]model.Health{"up": model.HealthCrit, "away": model.HealthWarn, "refused": model.HealthWarn, "moved": model.HealthCrit, "new": model.HealthUnknown, "bsd": model.HealthUnknown, "fine": model.HealthOK} {
		for _, x := range hosts {
			if x.Name == name && x.Health != string(h) {
				t.Errorf("%s health = %s, want %s", name, x.Health, h)
			}
		}
	}
	if r := attentionOf(hosts)[0].Route; r != "/hosts/up" {
		t.Errorf("route = %s", r)
	}
	if r := hostRoute("a b"); r != "/hosts/a%20b" {
		t.Errorf("escaped route = %s", r)
	}
}

// The ring keeps an hour at most, oldest first, and a clock that steps back does not break the
// order.
func TestHistoryRing(t *testing.T) {
	h := newHistory()
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for i := range 2 * historyCap {
		h.add("A", sshmodel.Sample{T: base.Add(time.Duration(i) * time.Second).Unix()})
	}
	now := base.Add(time.Duration(2*historyCap) * time.Second)
	s := h.samples("a", now)
	if len(s) != historyCap || s[len(s)-1].T != now.Unix()-1 {
		t.Fatalf("%d samples, last %d", len(s), s[len(s)-1].T)
	}
	for i := 1; i < len(s); i++ {
		if s[i].T <= s[i-1].T {
			t.Fatal("not oldest first")
		}
	}
	if old := h.samples("a", now.Add(2*time.Hour)); len(old) != 0 {
		t.Errorf("%d samples older than an hour", len(old))
	}
	h.add("b", sshmodel.Sample{T: 100})
	h.add("b", sshmodel.Sample{T: 200})
	h.add("b", sshmodel.Sample{T: 150}) // the clock stepped back
	if got := h.samples("b", time.Unix(200, 0)); len(got) != 2 || got[0].T != 100 || got[1].T != 150 {
		t.Errorf("after a step back: %+v", got)
	}
	h.keep([]string{"B"})
	if len(h.samples("a", now)) != 0 || len(h.samples("b", time.Unix(200, 0))) != 2 {
		t.Error("keep")
	}
}

// The twin publishes the world's three hosts with a made-up identity, raises the VM's failed unit,
// answers the history route from the clock, and is the same at the same instant.
func TestDemoTwin(t *testing.T) {
	clock := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	run := func() (*twin, *demoHost) {
		tw := (&Plugin{}).Demo().(*twin)
		h := &demoHost{now: clock}
		if err := tw.Start(context.Background(), h, nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tw.Stop(context.Background()) })
		return tw, h
	}
	tw, h := run()
	d := h.payload()
	if len(d.Hosts) != 3 || d.Identity == nil || d.IntervalSeconds != 5 {
		t.Fatalf("payload = %+v", d)
	}
	for _, x := range d.Hosts {
		if x.State != sshmodel.StateUp || x.Info == nil || x.Stats == nil || x.Message == "" || x.Health == "" {
			t.Errorf("host %+v", x)
		}
	}
	if d.Hosts[0].Info.Pools[0].Name != "tank" || d.Hosts[0].Info.Containers == nil || d.Hosts[1].Stats.CPUTempC == nil || d.Hosts[2].Stats.CPUTempC != nil {
		t.Errorf("the hosts are not the NAS, the Pi and the VM: %+v", d.Hosts)
	}
	items := tw.Attention(clock)
	if len(items) != 1 || items[0].ID != "ssh:units:vm-1" {
		t.Errorf("attention = %+v", items)
	}
	for _, x := range d.Hosts {
		hist, ok := historyOf(t, tw, x.Name)
		if !ok || len(hist.Samples) != 720 || hist.Samples[len(hist.Samples)-1].T != clock.Unix() {
			t.Errorf("%s history: %d samples, ok %v", x.Name, len(hist.Samples), ok)
		}
	}
	if _, ok := historyOf(t, tw, "nope"); ok {
		t.Error("an unknown demo host has a history")
	}
	if st := tw.Status(); st.State != plugin.StateHealthy {
		t.Errorf("status = %+v", st)
	}
	_, h2 := run()
	if !reflect.DeepEqual(h.payload(), h2.payload()) {
		t.Error("two twins at the same instant publish different payloads")
	}
	if hits := tw.Search("nas", []string{"Host"}, 5); len(hits) != 1 {
		t.Errorf("search = %+v", hits)
	}
}

// demoHost is the twin's host: a fixed clock and the last payload.
type demoHost struct {
	stubHost
	now  time.Time
	last sshmodel.HostsData
}

func (h *demoHost) Now() time.Time { return h.now }
func (h *demoHost) Publish(_ string, data any, _ []model.SourceStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = data.(sshmodel.HostsData)
}

func (h *demoHost) payload() sshmodel.HostsData {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}
