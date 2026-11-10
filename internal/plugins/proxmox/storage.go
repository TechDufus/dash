package proxmox

import (
	"cmp"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// The pve-exporter series storage.source: prometheus reads. pve_storage_shared is newer than the
// others: an exporter without it answers an empty result, and every storage then counts as the
// node's own.
const (
	exprStorageUsed   = `pve_disk_usage_bytes{id=~"storage/.+"}`
	exprStorageSize   = `pve_disk_size_bytes{id=~"storage/.+"}`
	exprStorageInfo   = `pve_storage_info`
	exprStorageShared = `pve_storage_shared{id=~"storage/.+"}`
)

// promStorage is what pve_storage_info says about one storage id.
type promStorage struct{ name, node string }

// pollStorage reads the storages from the configured source and publishes them. A failed poll
// publishes the last good list with the failing source.
func (r *runtime) pollStorage(ctx context.Context) {
	var (
		name string
		err  error
	)
	if r.cfg.StorageSource() == pluginconf.ProxmoxStoragePrometheus {
		name, err = sourcePrometheus, r.storageFromPrometheus(ctx)
	} else {
		name, err = sourceName, r.storageFromAPI(ctx)
	}
	if ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	r.storOut = outcome{polled: true, cat: upstream.Category(err), at: r.h.Now()}
	data, out := storageData(r.storages), r.storOut
	r.mu.Unlock()
	r.note("storage", err)
	r.h.Publish(topicStorage, data, []model.SourceStatus{src(name, out)})
}

// storageData is the proxmox.storage payload of a list: its totals count each storage once, which
// the list already holds a shared storage as.
func storageData(list []pvemodel.PVEStorage) pvemodel.PVEStorageData {
	d := pvemodel.PVEStorageData{Storages: append([]pvemodel.PVEStorage{}, list...)}
	for _, s := range list {
		d.UsedBytes += s.UsedBytes
		d.SizeBytes += s.SizeBytes
	}
	return d
}

// storageEntry is one storage of one node as a source reports it; key identifies it when it has
// no node to tell it from another of the same name. trusted is an entry whose node is online and
// reaches the storage: of a shared storage's entries, its usage is the one to show.
type storageEntry struct {
	name, node, key      string
	shared, trusted      bool
	usedBytes, sizeBytes int64
}

// storageList turns every node's storages (those that report a size) into the proxmox.storage
// list. A shared storage (NFS, Ceph...) appears once per node with the same usage: it is listed
// once, without a node, so neither the list nor the totals count it twice; its usage is that of a
// trusted entry first, then of the largest, then of the first node by name, so it does not depend
// on the order of the answer. The shared ones come first by name, then every node's own by node
// and name.
func storageList(entries []storageEntry) []pvemodel.PVEStorage {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.shared != b.shared {
			return a.shared
		}
		if a.shared && a.name != b.name {
			return a.name < b.name
		}
		if a.shared {
			if a.trusted != b.trusted {
				return a.trusted
			}
			if a.sizeBytes != b.sizeBytes {
				return a.sizeBytes > b.sizeBytes
			}
		}
		if a.node != b.node {
			return a.node < b.node
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.key < b.key
	})
	out := make([]pvemodel.PVEStorage, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		var ref string
		switch {
		case e.shared:
			ref = plugin.Ref(ID, "Storage", e.name)
			e.node = ""
		case e.node != "":
			ref = plugin.Ref(ID, "Storage", e.node, e.name)
		default:
			ref = plugin.Ref(ID, "Storage", cmp.Or(e.key, e.name))
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, pvemodel.PVEStorage{
			Ref: ref, Name: e.name, Node: e.node, Shared: e.shared, UsedBytes: e.usedBytes, SizeBytes: e.sizeBytes,
		})
	}
	return out
}

func (r *runtime) storageFromAPI(ctx context.Context) error {
	list, err := r.client.Storages(ctx)
	if err != nil {
		return err
	}
	offline := r.offlineNodes()
	entries := make([]storageEntry, len(list))
	for i, s := range list {
		entries[i] = storageEntry{
			name: s.Name, node: s.Node, shared: s.Shared, trusted: !offline[s.Node] && s.Status == "available",
			usedBytes: s.UsedBytes, sizeBytes: s.SizeBytes,
		}
	}
	out := storageList(entries)
	r.mu.Lock()
	r.storages = out
	r.mu.Unlock()
	return nil
}

// storageFromPrometheus runs the queries together. A query that fails leaves its last values in
// place and the poll reports the first error (in query order); the others still update, as the
// Manager's did.
func (r *runtime) storageFromPrometheus(ctx context.Context) error {
	q := r.querier()
	if q == nil {
		return upstream.ErrDisabled
	}
	exprs := [4]string{exprStorageUsed, exprStorageSize, exprStorageInfo, exprStorageShared}
	var (
		res  [4][]svc.Sample
		errs [4]error
		wg   sync.WaitGroup
	)
	for i, e := range exprs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = q.Instant(ctx, e)
		}()
	}
	wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	if errs[0] == nil {
		r.promUsed = byLabel(res[0], "id")
	}
	if errs[1] == nil {
		r.promSize = byLabel(res[1], "id")
	}
	if errs[2] == nil {
		r.promInfo = map[string]promStorage{}
		for _, s := range res[2] {
			r.promInfo[s.Labels["id"]] = promStorage{name: s.Labels["storage"], node: s.Labels["node"]}
		}
	}
	if errs[3] == nil {
		r.promShared = byLabel(res[3], "id")
	}
	r.storages = prometheusStorages(r.promUsed, r.promSize, r.promShared, r.promInfo, notOnline(r.cluster.Nodes))
	return errors.Join(errs[:]...)
}

// prometheusStorages assembles every node's storages from the exporter's series, which are keyed
// by the "storage/<node>/<name>" id: one exporter covers the whole cluster. The name and the node
// come from pve_storage_info, else from the id; an id of another shape is its own name. A storage
// is shared when pve_storage_shared says so; an exporter without that series lists every storage
// once per node, a shared one included. A storage without a size is left out, as the API source
// leaves it out.
func prometheusStorages(used, size, shared map[string]float64, info map[string]promStorage, offline map[string]bool) []pvemodel.PVEStorage {
	entries := make([]storageEntry, 0, len(size))
	for id := range size {
		if size[id] <= 0 {
			continue
		}
		e := storageEntry{key: id, shared: shared[id] != 0, usedBytes: int64(used[id]), sizeBytes: int64(size[id])}
		if parts := strings.SplitN(id, "/", 3); len(parts) == 3 && parts[0] == "storage" {
			e.node, e.name = parts[1], parts[2]
		}
		i := info[id]
		e.name = cmp.Or(i.name, e.name, id)
		e.node = cmp.Or(i.node, e.node)
		if e.node != "" && !upstream.ValidNodeName(e.node) {
			continue
		}
		e.trusted = !offline[e.node]
		entries = append(entries, e)
	}
	return storageList(entries)
}

// byLabel indexes sample values by one label (later samples of the same label win).
func byLabel(s []svc.Sample, label string) map[string]float64 {
	out := make(map[string]float64, len(s))
	for _, x := range s {
		out[x.Labels[label]] = x.Value
	}
	return out
}
