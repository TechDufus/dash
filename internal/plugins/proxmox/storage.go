package proxmox

import (
	"context"
	"errors"
	"slices"
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

// The pve-exporter series storage.source: prometheus reads.
const (
	exprStorageUsed = `pve_disk_usage_bytes{id=~"storage/.+"}`
	exprStorageSize = `pve_disk_size_bytes{id=~"storage/.+"}`
	exprStorageInfo = `pve_storage_info`
)

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
	data, out := pvemodel.PVEStorageData{Storages: append([]pvemodel.PVEStorage{}, r.storages...)}, r.storOut
	r.mu.Unlock()
	r.note("storage", err)
	r.h.Publish(topicStorage, data, []model.SourceStatus{src(name, out)})
}

func (r *runtime) storageFromAPI(ctx context.Context) error {
	client := r.nodeClient()
	if client == nil {
		return upstream.ErrDisabled
	}
	list, err := client.Storages(ctx)
	if err != nil {
		return err
	}
	out := make([]pvemodel.PVEStorage, len(list))
	for i, s := range list {
		out[i] = pvemodel.PVEStorage{Ref: plugin.Ref(ID, "Storage", s.Name), Name: s.Name, UsedBytes: s.UsedBytes, SizeBytes: s.SizeBytes}
	}
	r.mu.Lock()
	r.storages = out
	r.mu.Unlock()
	return nil
}

// storageFromPrometheus runs the three queries together. A query that fails leaves its last
// values in place and the poll reports the first error (in query order); the others still update,
// as the Manager's did.
func (r *runtime) storageFromPrometheus(ctx context.Context) error {
	q := r.querier()
	if q == nil {
		return upstream.ErrDisabled
	}
	exprs := [3]string{exprStorageUsed, exprStorageSize, exprStorageInfo}
	var (
		res  [3][]svc.Sample
		errs [3]error
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
		r.promNames = map[string]string{}
		for _, s := range res[2] {
			r.promNames[s.Labels["id"]] = s.Labels["storage"]
		}
	}
	r.storages = prometheusStorages(r.node, r.promUsed, r.promSize, r.promNames)
	return errors.Join(errs[:]...)
}

// prometheusStorages assembles the storages from the exporter's series, which are keyed by the
// "storage/<node>/<name>" id: the name comes from pve_storage_info, or is the id when there is
// none. The list is by name.
//
// An exporter that reports several nodes carries every node's storages. When any id belongs to
// this plugin's node, only those are kept, as the API source lists only this node's; when none
// does (the exporter names its nodes differently), all of them stay. A ref is the name, except
// for a name that more than one remaining storage has: those use their id, so refs never repeat.
func prometheusStorages(node string, used, size map[string]float64, names map[string]string) []pvemodel.PVEStorage {
	ids := make([]string, 0, len(size))
	mine := false
	for id := range size {
		ids = append(ids, id)
		mine = mine || (node != "" && strings.HasPrefix(id, "storage/"+node+"/"))
	}
	if mine {
		ids = slices.DeleteFunc(ids, func(id string) bool { return !strings.HasPrefix(id, "storage/"+node+"/") })
	}
	nameOf := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	count := map[string]int{}
	for _, id := range ids {
		count[nameOf(id)]++
	}
	sort.Slice(ids, func(i, j int) bool {
		if a, b := nameOf(ids[i]), nameOf(ids[j]); a != b {
			return a < b
		}
		return ids[i] < ids[j]
	})
	out := make([]pvemodel.PVEStorage, 0, len(ids))
	for _, id := range ids {
		name := nameOf(id)
		key := name
		if count[name] > 1 {
			key = id
		}
		out = append(out, pvemodel.PVEStorage{
			Ref: plugin.Ref(ID, "Storage", key), Name: name, UsedBytes: int64(used[id]), SizeBytes: int64(size[id]),
		})
	}
	return out
}

// byLabel indexes sample values by one label (later samples of the same label win).
func byLabel(s []svc.Sample, label string) map[string]float64 {
	out := make(map[string]float64, len(s))
	for _, x := range s {
		out[x.Labels[label]] = x.Value
	}
	return out
}
