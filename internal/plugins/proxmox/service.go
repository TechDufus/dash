package proxmox

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// outcome is how the last poll of one source ended.
type outcome struct {
	polled bool      // a poll has ended, whether or not it succeeded
	cat    string    // the error category; "" when it succeeded
	at     time.Time // when it ended
}

// discovery is what node discovery knows while the node is unset.
type discovery struct {
	since  time.Time // when discovery began
	cat    string    // the category of the last transient failure
	cfgErr string    // a failure no retry can fix: none or several nodes
}

// runtime is one run of the plugin: what Start built and the pollers fill. A reload makes a new
// one, so nothing here outlives its configuration.
type runtime struct {
	h      plugin.Host
	cfg    pluginconf.Proxmox
	t      timing
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	transport http.RoundTripper
	opts      []upstream.Option
	nodeless  *upstream.Proxmox // Nodes only: it has no node to address

	nodeReady chan struct{} // closed when the node is known
	readyOnce sync.Once

	// pubMu serialises the build and the publish of proxmox.vms, so a slow poll cannot send an
	// older snapshot after a newer one.
	pubMu sync.Mutex

	mu       sync.RWMutex
	node     string
	client   *upstream.Proxmox // the node client; nil until the node is known
	disc     discovery
	vmsOut   outcome
	nodeOut  outcome
	storOut  outcome
	vms      []upstream.PVEVM
	tasks    []model.PVETask
	nodeData pvemodel.NodeData
	storages []pvemodel.PVEStorage
	// The last good samples of the three pve-exporter queries, kept separately: a failing query
	// leaves the values of the others (and its own old ones) as they were.
	promUsed, promSize map[string]float64
	promNames          map[string]string
	logged             map[string]string // source -> the category last logged for it

	fs plugin.FirstSeen

	// detail answers the VM detail; setNode points it at the node client unless a run (the demo
	// twin) brought its own.
	detail detailSource
	// build replaces buildVMs when set: the demo twin's payload comes from the fake world.
	build func() model.VMsData
}

// detailSource is what the VM detail route reads: the node client, or the twin's fixtures.
type detailSource interface {
	VMConfig(ctx context.Context, vmid int) (model.VMConfig, error)
	Snapshots(ctx context.Context, vmid int) ([]model.Snapshot, error)
	VMIPs(ctx context.Context, vmid int) []model.IfaceAddr
}

// setNode records the node, builds the node client and lets the pollers start.
func (r *runtime) setNode(name string) {
	r.mu.Lock()
	r.node = name
	r.client = upstream.NewProxmox(r.cfg.URL, name, r.cfg.TokenID, r.cfg.TokenSecret, r.transport, r.opts...)
	if r.detail == nil {
		r.detail = r.client
	}
	r.nodeData = pvemodel.NodeData{Ref: plugin.Ref(ID, "Node", name), Node: name}
	r.mu.Unlock()
	r.readyOnce.Do(func() { close(r.nodeReady) })
}

// detailer is the source of the VM detail, nil until the node is known.
func (r *runtime) detailer() detailSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.detail
}

// nodeName is the node dash reads, and whether it is known yet.
func (r *runtime) nodeName() (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.node, r.node != ""
}

// nodeClient is the node client, nil until the node is known.
func (r *runtime) nodeClient() *upstream.Proxmox {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client
}

// waitNode blocks until the node is known; it reports false when ctx ends first.
func (r *runtime) waitNode(ctx context.Context) bool {
	select {
	case <-r.nodeReady:
		return true
	case <-ctx.Done():
		return false
	}
}

// every runs poll once the node is known and then every d until the run ends.
func (r *runtime) every(d time.Duration, poll func(context.Context)) {
	if !r.waitNode(r.ctx) {
		return
	}
	poll(r.ctx)
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			poll(r.ctx)
		}
	}
}

// querier is the prometheus plugin's service, nil when it does not run.
func (r *runtime) querier() svc.Querier {
	v, ok := r.h.Service("prometheus")
	if !ok {
		return nil
	}
	q, _ := v.(svc.Querier)
	return q
}

// nodeIndex is the kubernetes plugin's node index, nil when it does not run.
func (r *runtime) nodeIndex() svc.NodeIndex {
	v, ok := r.h.Service("kubernetes")
	if !ok {
		return nil
	}
	idx, _ := v.(svc.NodeIndex)
	return idx
}

// indexSig identifies what a change of the node index looks like to the VMs: which service it is
// (a reload builds a new one), its revision and whether it has synced. Synced can turn true with
// no node event, so the revision alone would leave the first publish gate shut.
type indexSig struct {
	id     uintptr
	has    bool
	rev    uint64
	synced bool
}

func (r *runtime) indexSig() indexSig {
	idx := r.nodeIndex()
	if idx == nil {
		return indexSig{}
	}
	s := indexSig{has: true, rev: idx.Revision(), synced: idx.Synced()}
	if v := reflect.ValueOf(idx); v.Kind() == reflect.Pointer {
		s.id = v.Pointer()
	}
	return s
}

// watchIndex republishes the VMs when the node index changes: a node's readiness reaches the VM
// as fast as it did when the Manager rebuilt on every node event.
func (r *runtime) watchIndex() {
	last := r.indexSig()
	t := time.NewTicker(r.t.watch)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			if cur := r.indexSig(); cur != last {
				last = cur
				r.publishVMs()
			}
		}
	}
}

// note logs a failed poll once per change of category, with the detail scrubbed: viewers see the
// category only, because the detail names the URL and the host.
func (r *runtime) note(source string, err error) {
	cat := upstream.Category(err)
	r.mu.Lock()
	prev := r.logged[source]
	r.logged[source] = cat
	r.mu.Unlock()
	switch {
	case cat == prev:
	case err != nil:
		r.h.Logger().Warn("upstream source failed", "source", source, "category", cat, "error", r.h.ScrubLog(err.Error()))
	default:
		r.h.Logger().Info("upstream source recovered", "source", source)
	}
}

// src is the SourceStatus of an outcome.
func src(name string, o outcome) model.SourceStatus {
	return model.SourceStatus{Name: name, OK: o.cat == "", Error: o.cat, CheckedAt: o.at}
}

// sourcesLocked is the sources the plugin reports: Proxmox (the VM list, the node and, from the
// API, the storages), the metrics source of the storages when they come from there, and the
// note that verification is off. The caller holds r.mu.
func (r *runtime) sourcesLocked() []model.SourceStatus {
	out := []model.SourceStatus{}
	pve := []outcome{r.vmsOut, r.nodeOut}
	if r.cfg.StorageSource() == pluginconf.ProxmoxStorageAPI {
		pve = append(pve, r.storOut)
	}
	merged := outcome{}
	for _, o := range pve {
		if !o.polled {
			continue
		}
		merged.polled = true
		if merged.cat == "" {
			merged.cat = o.cat
		}
		if o.at.After(merged.at) {
			merged.at = o.at
		}
	}
	if merged.polled {
		out = append(out, src(sourceName, merged))
	}
	if r.cfg.StorageSource() == pluginconf.ProxmoxStoragePrometheus && r.storOut.polled {
		out = append(out, src(sourcePrometheus, r.storOut))
	}
	if r.cfg.TLS.InsecureSkipVerify {
		out = append(out, model.SourceStatus{Name: sourceInsecure, OK: true})
	}
	return out
}

// status implements Plugin.Status.
func (r *runtime) status() plugin.Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	srcs := r.sourcesLocked()
	if r.node == "" {
		switch {
		case r.disc.cfgErr != "":
			return plugin.Status{State: plugin.StateError, Message: r.disc.cfgErr, Sources: srcs}
		case r.disc.cat != "" && r.h.Now().Sub(r.disc.since) >= r.t.grace:
			return plugin.Status{State: plugin.StateError, Message: "node discovery failed: " + r.disc.cat + "; set plugins.proxmox.node to skip it", Sources: srcs}
		}
		return plugin.Status{State: plugin.StateStarting, Message: "waiting for the Proxmox node name", Sources: srcs}
	}
	if !r.vmsOut.polled {
		return plugin.Status{State: plugin.StateStarting, Message: "waiting for the first poll", Sources: srcs}
	}
	var failing []string
	polled := 0
	for _, p := range []struct {
		name string
		o    outcome
	}{{"vms", r.vmsOut}, {"node", r.nodeOut}, {"storage", r.storOut}} {
		if !p.o.polled {
			continue
		}
		polled++
		if p.o.cat != "" {
			failing = append(failing, p.name+": "+p.o.cat)
		}
	}
	switch {
	case len(failing) == 0:
		return plugin.Status{State: plugin.StateHealthy, Sources: srcs}
	case polled == 3 && len(failing) == 3:
		return plugin.Status{State: plugin.StateError, Message: strings.Join(failing, "; "), Sources: srcs}
	}
	return plugin.Status{State: plugin.StateDegraded, Message: strings.Join(failing, "; "), Sources: srcs}
}

// errNotRunning is what a request that races Stop or a reload gets.
var errNotRunning = &plugin.ActionError{
	Status: http.StatusServiceUnavailable, Code: model.ErrCodeUpstreamUnavailable, Message: "proxmox is not available",
}
