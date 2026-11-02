package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
)

// The fake cluster the tests read: neutral names, documentation addresses.
const (
	pveNodes   = `{"data":[{"node":"pve-1","status":"online"}]}`
	pveStatus  = `{"data":{"uptime":2422190,"kversion":"Linux 6.8.12-4-pve #1","pveversion":"pve-manager/9.0.5/b9984c6d","current-kernel":{"release":"6.8.12-4-pve"},"cpuinfo":{"model":"Example CPU @ 2.10GHz","cpus":8}}}`
	pveTasks   = `{"data":[{"upid":"UPID:pve-1:0001:qmstart","type":"qmstart","id":"101","user":"root@pam","status":"OK","starttime":1793390000,"endtime":1793390005}]}`
	pveStorage = `{"data":[{"storage":"tank","type":"zfspool","used":50,"total":100,"active":1},{"storage":"local","type":"dir","used":1000,"total":4000,"active":1},{"storage":"offline","type":"nfs","active":0}]}`
	pveConfig  = `{"data":{"name":"k8s-cp-1","ostype":"l26","cores":4,"sockets":1,"memory":"8192","balloon":0,"agent":"enabled=1","scsi0":"local-zfs:vm-101-disk-0,size=64G","net0":"virtio=02:00:5e:00:53:01,bridge=vmbr0"}}`
	pveIPs     = `{"data":{"result":[{"name":"lo","ip-addresses":[{"ip-address-type":"ipv4","prefix":8,"ip-address":"127.0.0.1"}]},{"name":"eth0","ip-addresses":[{"ip-address-type":"ipv4","prefix":24,"ip-address":"192.0.2.21"}]}]}}`
	pveSnaps   = `{"data":[{"name":"pre-upgrade","description":"before the upgrade","snaptime":1793300000},{"name":"current","description":"You are here!"}]}`
	pveVMs     = `{"data":[
	  {"id":"qemu/101","vmid":101,"name":"k8s-cp-1","status":"running","template":0,"maxcpu":4,"cpu":0.25,"mem":4294967296,"maxmem":8589934592,"uptime":3600,"netin":100,"netout":200,"tags":"k8s;control-plane"},
	  {"id":"qemu/102","vmid":102,"name":"db-1","status":"stopped","template":0,"maxcpu":2,"maxmem":4294967296,"tags":"lab;Critical"},
	  {"id":"qemu/103","vmid":103,"name":"scratch","status":"stopped","template":0,"maxcpu":1,"maxmem":1073741824,"tags":"lab"},
	  {"id":"qemu/9000","vmid":9000,"name":"k8s-template","status":"stopped","template":1,"maxcpu":1,"maxmem":1073741824,"tags":"template"}]}`

	testTokenID = "dash@pve!ci"
	// testToken is the canary: it must never reach a log line, a payload or a response.
	testToken = "0b1c2d3e-aaaa-bbbb-cccc-000000000042"
)

// fakePVE is a Proxmox API: it answers the endpoints the plugin reads, counts the requests of each
// path and records every Authorization header.
type fakePVE struct {
	*httptest.Server

	mu     sync.Mutex
	body   map[string]string // path -> answer; the defaults are set by newFakePVE
	status map[string]int    // path -> a forced status code
	echo   bool              // 500 answers echo the Authorization header, as a broken proxy might
	to     string            // when set, every request is redirected to this base URL
	hits   map[string]int
	auth   []string
	nodes  atomic.Int32 // requests to /nodes
}

func newFakePVE(t *testing.T, tls bool) *fakePVE {
	t.Helper()
	f := &fakePVE{hits: map[string]int{}, status: map[string]int{}, body: map[string]string{
		"/api2/json/nodes":                                             pveNodes,
		"/api2/json/nodes/pve-1/status":                                pveStatus,
		"/api2/json/cluster/resources":                                 pveVMs,
		"/api2/json/cluster/tasks":                                     pveTasks,
		"/api2/json/nodes/pve-1/storage":                               pveStorage,
		"/api2/json/nodes/pve-1/qemu/101/config":                       pveConfig,
		"/api2/json/nodes/pve-1/qemu/101/agent/network-get-interfaces": pveIPs,
		"/api2/json/nodes/pve-1/qemu/101/snapshot":                     pveSnaps,
	}}
	h := http.HandlerFunc(f.serve)
	if tls {
		f.Server = httptest.NewTLSServer(h)
	} else {
		f.Server = httptest.NewServer(h)
	}
	t.Cleanup(f.Close)
	return f
}

func (f *fakePVE) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	body, ok := f.body[r.URL.Path]
	code := f.status[r.URL.Path]
	echo, to := f.echo, f.to
	f.mu.Unlock()
	if r.URL.Path == "/api2/json/nodes" {
		f.nodes.Add(1)
	}
	switch {
	case to != "":
		http.Redirect(w, r, to+r.URL.Path, http.StatusFound)
	case code != 0:
		w.WriteHeader(code)
		if echo && code == http.StatusInternalServerError {
			_, _ = io.WriteString(w, `{"message":"boom for `+r.Header.Get("Authorization")+` at `+r.Host+`"}`)
		}
	case !ok:
		http.Error(w, `{"data":null,"message":"no such endpoint"}`, http.StatusNotImplemented)
	default:
		_, _ = io.WriteString(w, body)
	}
}

// echoErrors makes a 500 answer echo the Authorization header and the Host, as a broken proxy might.
func (f *fakePVE) echoErrors() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.echo = true
}

// redirectTo sends every request to base instead.
func (f *fakePVE) redirectTo(base string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.to = base
}

func (f *fakePVE) set(path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body["/api2/json"+path] = body
}

func (f *fakePVE) fail(path string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status["/api2/json"+path] = code
}

func (f *fakePVE) heal(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.status, "/api2/json"+path)
}

func (f *fakePVE) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits["/api2/json"+path]
}

// pathsWith lists the paths that were requested and contain sub.
func (f *fakePVE) pathsWith(sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for p := range f.hits {
		if strings.Contains(p, sub) {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakePVE) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.hits {
		n += c
	}
	return n
}

func (f *fakePVE) authHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...)
}

// fakeIndex is the kubernetes plugin's svc.NodeIndex.
type fakeIndex struct {
	mu     sync.Mutex
	nodes  map[string]bool // name -> ready
	synced bool
	rev    uint64
}

func (x *fakeIndex) K8sNode(vm string) (string, bool, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.synced {
		return "", false, false
	}
	ready, ok := x.nodes[vm]
	return vm, ready, ok
}
func (x *fakeIndex) NodeIP(string) (string, bool) { return "", false }
func (x *fakeIndex) Synced() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.synced
}
func (x *fakeIndex) Revision() uint64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.rev
}

// setSynced flips the sync flag without touching the revision.
func (x *fakeIndex) setSynced(v bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.synced = v
}

// setNode changes a node and bumps the revision, as a node event does.
func (x *fakeIndex) setNode(name string, ready bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.nodes[name] = ready
	x.rev++
}

var _ svc.NodeIndex = (*fakeIndex)(nil)

// fakeProm is the prometheus plugin's svc.Querier: each expression answers with its own samples.
type fakeProm struct {
	mu      sync.Mutex
	samples map[string][]svc.Sample
	err     map[string]error
	calls   []string
}

func (q *fakeProm) Instant(_ context.Context, expr string) ([]svc.Sample, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls = append(q.calls, expr)
	if err := q.err[expr]; err != nil {
		return nil, err
	}
	return q.samples[expr], nil
}

func (q *fakeProm) Range(context.Context, string, time.Time, time.Time, time.Duration) ([]model.SeriesLine, error) {
	return nil, nil
}

func (q *fakeProm) setErr(expr string, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err == nil {
		q.err = map[string]error{}
	}
	q.err[expr] = err
}

// set is what expr answers from now on.
func (q *fakeProm) set(expr string, samples ...svc.Sample) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.samples[expr] = samples
}

// sample builds one pve-exporter sample.
func sample(v float64, kv ...string) svc.Sample {
	l := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		l[kv[i]] = kv[i+1]
	}
	return svc.Sample{Labels: l, Value: v}
}

// env is the plugin on a real supervisor and hub, against a fake Proxmox, a fake prometheus and a
// fake kubernetes node index.
type env struct {
	pve  *fakePVE
	hub  *hub.Hub
	sup  *plugin.Supervisor
	set  *plugintest.Settings
	mu   sync.Mutex
	p    *Plugin // the latest one the factory made: the one that runs
	cfg  *pluginconf.Proxmox
	idx  *fakeIndex
	prom *fakeProm
	logs *syncBuffer
}

// syncBuffer is a bytes.Buffer that is safe to write from the plugin's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type envOpts struct {
	cfg      func(*pluginconf.Proxmox) // adjusts the configuration
	pve      *fakePVE                  // use this upstream
	noKube   bool                      // no kubernetes plugin
	noProm   bool                      // no prometheus plugin
	unsynced bool                      // the node index has not synced yet
	timing   func(*timing)
	demo     bool
	scrub    func(string) string
	links    func(plugin.LinkRequest) []model.Link // a headlamp-like provider
	prefill  func(*env)                            // fills the fakes before the plugin starts: a poll never sees half of it
}

// testToken is a secret.Value holding testToken; Update rotates it.
func tokenValue() secret.Value { return secret.New(testToken, "") }

// defaultConfig is the configuration the tests start from: the fake's URL, the token, one node
// named explicitly unless a test clears it.
func defaultConfig(url string) pluginconf.Proxmox {
	c := pluginconf.DefaultProxmox()
	c.URL, c.Node, c.TokenID, c.TokenSecret = url, "pve-1", testTokenID, tokenValue()
	return c
}

func fastTiming() timing {
	return timing{
		vms: 20 * time.Millisecond, node: 20 * time.Millisecond, storage: 20 * time.Millisecond,
		watch:   10 * time.Millisecond,
		backoff: 5 * time.Millisecond, backoffMax: 20 * time.Millisecond, grace: time.Minute, steady: 20 * time.Millisecond,
	}
}

// newEnv starts the plugin (or its twin) and the fakes it uses.
func newEnv(t *testing.T, o envOpts) *env {
	t.Helper()
	e := &env{pve: o.pve, logs: &syncBuffer{}, idx: &fakeIndex{nodes: map[string]bool{"k8s-cp-1": true}, synced: !o.unsynced}, prom: &fakeProm{samples: map[string][]svc.Sample{}}}
	if e.pve == nil {
		e.pve = newFakePVE(t, false)
	}
	cfg := defaultConfig(e.pve.URL)
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	e.cfg = &cfg
	tm := fastTiming()
	if o.timing != nil {
		o.timing(&tm)
	}
	factory := func() plugin.Plugin {
		p := &Plugin{t: tm}
		e.mu.Lock()
		e.p = p
		e.mu.Unlock()
		return p
	}

	e.hub = hub.New()
	e.set = &plugintest.Settings{}
	e.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &cfg})
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: e.hub, Settings: e.set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		Log: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Demo: o.demo,
		ScrubLog: o.scrub,
	})
	e.sup = sup
	if !o.noProm {
		prom := plugintest.New("prometheus")
		prom.ServiceVal = svc.Querier(e.prom)
		if err := sup.Add(prom.Factory()); err != nil {
			t.Fatal(err)
		}
	}
	if !o.noKube {
		kube := plugintest.New("kubernetes")
		kube.ServiceVal = svc.NodeIndex(e.idx)
		if err := sup.Add(kube.Factory()); err != nil {
			t.Fatal(err)
		}
	}
	if o.links != nil {
		hl := plugintest.New("headlamp")
		hl.LinksFn = o.links
		if err := sup.Add(hl.Factory()); err != nil {
			t.Fatal(err)
		}
	}
	if err := sup.Add(factory); err != nil {
		t.Fatal(err)
	}
	e.hub.SetOrder(sup.TopicOrder)
	if o.prefill != nil {
		o.prefill(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })
	return e
}

// payload reads the latest envelope of a topic into out and returns its sources; it reports false
// when the topic has not been published.
func (e *env) payload(t *testing.T, topic string, out any) ([]model.SourceStatus, bool) {
	t.Helper()
	ev, found := e.hub.Latest("", model.Topic(topic))
	if !found {
		return nil, false
	}
	var env model.Envelope
	if err := json.Unmarshal(ev.Data, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		t.Fatal(err)
	}
	return env.Sources, true
}

// waitFor polls cond until it holds, or fails.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// vms waits for a proxmox.vms payload that satisfies ok.
func (e *env) vms(t *testing.T, what string, ok func(model.VMsData) bool) (model.VMsData, []model.SourceStatus) {
	t.Helper()
	var d model.VMsData
	var srcs []model.SourceStatus
	waitFor(t, what, func() bool {
		var cur model.VMsData
		s, found := e.payload(t, "proxmox.vms", &cur)
		if found && ok(cur) {
			d, srcs = cur, s
			return true
		}
		return false
	})
	return d, srcs
}

func anyVMs(model.VMsData) bool { return true }

// info is the plugin's row.
func (e *env) info(t *testing.T) model.PluginInfo {
	t.Helper()
	for _, pi := range e.sup.Infos() {
		if pi.ID == string(ID) {
			return pi
		}
	}
	t.Fatal("the plugin has no row")
	return model.PluginInfo{}
}

// state waits for the row to reach a state.
func (e *env) state(t *testing.T, want plugin.State) model.PluginInfo {
	t.Helper()
	var pi model.PluginInfo
	waitFor(t, "the row to be "+string(want), func() bool {
		pi = e.info(t)
		return pi.State == want
	})
	return pi
}

// get serves a GET through the supervisor's router, as the API does with no guard.
func (e *env) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	h, ok := e.sup.Router(ID, plugin.RouteGuard{})
	if !ok {
		t.Fatal("the plugin has no routes")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/proxmox"+path, nil))
	return w
}

func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// upstreamErr is a plain error a fake querier returns.
func upstreamErr(msg string) error { return errors.New(msg) }
