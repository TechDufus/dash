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
	client    *upstream.Proxmox // reads the whole cluster; nil in the demo twin

	// pubMu serialises the build and the publish of proxmox.vms, so a slow poll cannot send an
	// older snapshot after a newer one.
	pubMu sync.Mutex

	mu         sync.RWMutex
	vmsOut     outcome
	clusterOut outcome
	statusOut  outcome // /cluster/status alone: its failure does not fail the cluster poll
	storOut    outcome
	vms        []upstream.PVEVM
	tasks      []model.PVETask
	// memo is what each guest last showed while its node was up: a node offline for a while makes
	// the cluster forget its guests' name, template flag and memory size.
	memo map[int]vmMemo
	// info is the last answer of /cluster/status that said the cluster's name and quorum.
	info clusterInfo
	// cluster is the last good proxmox.cluster payload; its Ref is empty until a cluster poll has
	// succeeded.
	cluster pvemodel.PVEClusterData
	// facts are what each node last said about itself: a node that is down, or that fails to
	// answer once, keeps them.
	facts    map[string]upstream.PVEHost
	storages []pvemodel.PVEStorage
	// The last good samples of the pve-exporter queries, kept separately: a failing query leaves
	// the values of the others (and its own old ones) as they were.
	promUsed, promSize, promShared map[string]float64
	promInfo                       map[string]promStorage
	logged                         map[string]string // source -> the category last logged for it

	// detail answers the VM detail: the client, or the twin's fixtures.
	detail detailSource
	// build replaces buildVMs when set: the demo twin's payload comes from the fake world.
	build func() model.VMsData
}

// detailSource is what the VM detail route reads, on the node the VM runs on: the client, or the
// twin's fixtures.
type detailSource interface {
	VMConfig(ctx context.Context, node string, vmid int) (model.VMConfig, error)
	Snapshots(ctx context.Context, node string, vmid int) ([]model.Snapshot, error)
	VMIPs(ctx context.Context, node string, vmid int) []model.IfaceAddr
}

// nodeName is the node that is the Hardware page's host: plugins.proxmox.node when it is set (and,
// once the cluster is known, one of its nodes), else the only node of a one-node cluster. ok is
// false until that is known, for a cluster of several nodes without the setting, and for a setting
// that names no node of the cluster.
func (r *runtime) nodeName() (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cfg.Node != "" {
		if r.cluster.Ref != "" && !r.hasNodeLocked(r.cfg.Node) {
			return "", false
		}
		return r.cfg.Node, true
	}
	if len(r.cluster.Nodes) == 1 {
		return r.cluster.Nodes[0].Node, true
	}
	return "", false
}

// strayNodeLocked is the message for a plugins.proxmox.node that names no node of the cluster the
// last good poll found; empty when it names one, or nothing is known yet. The caller holds r.mu.
func (r *runtime) strayNodeLocked() string {
	if r.cfg.Node == "" || r.cluster.Ref == "" || r.hasNodeLocked(r.cfg.Node) {
		return ""
	}
	names := make([]string, len(r.cluster.Nodes))
	for i, n := range r.cluster.Nodes {
		names[i] = n.Node
	}
	of := "the Proxmox server"
	if r.cluster.Name != "" {
		of = "cluster " + r.cluster.Name
	}
	return "plugins.proxmox.node names " + r.cfg.Node + ", which is not a node of " + of + " (" + strings.Join(names, ", ") + ")"
}

func (r *runtime) hasNodeLocked(node string) bool {
	for _, n := range r.cluster.Nodes {
		if n.Node == node {
			return true
		}
	}
	return false
}

// clusterNodes are the names of the cluster's nodes, sorted, once a cluster poll has succeeded.
func (r *runtime) clusterNodes() ([]string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cluster.Ref == "" {
		return nil, false
	}
	out := make([]string, len(r.cluster.Nodes))
	for i, n := range r.cluster.Nodes {
		out[i] = n.Node
	}
	return out, true
}

// isNode reports whether the cluster's last good poll names node.
func (r *runtime) isNode(node string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hasNodeLocked(node)
}

// every runs poll at once and then every d until the run ends.
func (r *runtime) every(d time.Duration, poll func(context.Context)) {
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

// storageLoop is every(r.t.storage, r.pollStorage) for the storages: with the Prometheus source
// it also polls at once when the querier appears after a poll that found none (the plugin starts
// later, or its credential is filled late), looked for every t.watch, so the source does not
// read "prometheus not_configured" until the next interval.
func (r *runtime) storageLoop() {
	t := time.NewTicker(r.t.storage)
	defer t.Stop()
	watch := time.NewTicker(r.t.watch)
	defer watch.Stop()
	for {
		had := r.querier() != nil
		r.pollStorage(r.ctx)
		for again := false; !again; {
			select {
			case <-r.ctx.Done():
				return
			case <-t.C:
				again = true
			case <-watch.C:
				again = !had && r.cfg.StorageSource() == pluginconf.ProxmoxStoragePrometheus && r.querier() != nil
			}
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

// sourcesLocked is the sources the plugin reports: Proxmox (the VM list, the cluster and, from the
// API, the storages), the metrics source of the storages when they come from there, and the
// note that verification is off. The caller holds r.mu.
func (r *runtime) sourcesLocked() []model.SourceStatus {
	out := []model.SourceStatus{}
	pve := []outcome{r.vmsOut, r.clusterOut}
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
	if r.statusOut.polled && r.statusOut.cat != "" {
		out = append(out, src(sourceClusterStatus, r.statusOut))
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
	if !r.vmsOut.polled {
		return plugin.Status{State: plugin.StateStarting, Message: "waiting for the first poll", Sources: srcs}
	}
	var failing []string
	polled := 0
	for _, p := range []struct {
		name string
		o    outcome
	}{{"vms", r.vmsOut}, {"cluster", r.clusterOut}, {"storage", r.storOut}} {
		if !p.o.polled {
			continue
		}
		polled++
		if p.o.cat != "" {
			failing = append(failing, p.name+": "+p.o.cat)
		}
	}
	if polled == 3 && len(failing) == 3 {
		return plugin.Status{State: plugin.StateError, Message: strings.Join(failing, "; "), Sources: srcs}
	}
	// Neither of these fails a poll: the cluster is read all the same.
	if r.statusOut.polled && r.statusOut.cat != "" && r.clusterOut.cat == "" {
		failing = append(failing, sourceClusterStatus+": "+r.statusOut.cat)
	}
	if msg := r.strayNodeLocked(); msg != "" {
		failing = append(failing, msg)
	}
	if len(failing) == 0 {
		return plugin.Status{State: plugin.StateHealthy, Sources: srcs}
	}
	return plugin.Status{State: plugin.StateDegraded, Message: strings.Join(failing, "; "), Sources: srcs}
}

// errNotRunning is what a request that races Stop or a reload gets.
var errNotRunning = &plugin.ActionError{
	Status: http.StatusServiceUnavailable, Code: model.ErrCodeUpstreamUnavailable, Message: "proxmox is not available",
}
