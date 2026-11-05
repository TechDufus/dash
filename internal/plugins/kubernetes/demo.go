package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// demoEvery is how often the twin's numbers move on, like the rest of demo mode.
const demoEvery = 5 * time.Second

// twin is the plugin's demo twin (design 1.9): the same manifest, the cluster, storage and apps
// of the fake world, a restart that does nothing, and no connection. It is the plugin itself run
// on fixtures: the payloads are decorated with their refs as the live builders do, and the
// attention items, KPIs, topology, charts and pod detail are the real code's.
type twin struct {
	Plugin
}

func newTwin() plugin.Plugin { return &twin{} }

// twinSource is what the demo runtime reads instead of a cluster: the fake world and the node
// addresses of its last snapshot.
type twinSource struct {
	w *world.World

	mu  sync.RWMutex
	ips map[string]string
	cl  model.ClusterData
}

func (s *twinSource) setCluster(cl model.ClusterData) {
	ips := make(map[string]string, len(cl.Nodes))
	for _, n := range cl.Nodes {
		ips[n.Name] = n.IP
	}
	s.mu.Lock()
	s.ips, s.cl = ips, cl
	s.mu.Unlock()
}

func (s *twinSource) nodeIP(name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ip, ok := s.ips[name]
	return ip, ok
}

// twinLinks is the world's link source: the links the running link providers give an object.
type twinLinks struct{ h plugin.Host }

func (l twinLinks) Headlamp(kind, namespace, name string) []model.Link {
	return linksOf(l.h.Links(), kind, namespace, name)
}

// Start publishes the world's payloads now and then every demoEvery.
func (t *twin) Start(ctx context.Context, h plugin.Host, _ any) error {
	h = t.Wrap(h)
	rctx, cancel := context.WithCancel(ctx)
	r := &runtime{
		h: h, cfg: pluginconf.DefaultKubernetes(), t: defaultTiming(), ctx: rctx, cancel: cancel,
		started: h.Now(), logged: map[string]string{},
		twin: &twinSource{w: world.New(h.Now, h.Now(), twinLinks{h})},
	}
	t.rt.Store(r)
	t.refresh(r)
	r.spawn("demo", func() { r.every(demoEvery, func() { t.refresh(r) }) })
	return nil
}

// refresh moves the fake state on and publishes the three topics.
func (t *twin) refresh(r *runtime) {
	now := r.h.Now()
	cl := decorateCluster(r.twin.w.Cluster())
	st := decorateStorage(r.twin.w.Storage())
	ap := decorateApps(r.twin.w.Apps())
	r.twin.setCluster(cl)
	r.mu.Lock()
	r.cluster, r.storageD = &cl, &st
	r.mu.Unlock()
	okSrc := func(name string) model.SourceStatus {
		return model.SourceStatus{Name: name, OK: true, CheckedAt: now.UTC()}
	}
	r.h.Publish(topicCluster, cl, []model.SourceStatus{okSrc(sourceKubernetes), okSrc(sourcePrometheus)})
	r.h.Publish(topicStorage, st, []model.SourceStatus{okSrc(sourceKubernetes), okSrc(sourcePrometheus)})
	r.h.Publish(topicApps, ap, []model.SourceStatus{okSrc(sourceKubernetes), okSrc("gatus")})
}

// Act is the fake restart: it applies the rules of the real one that need no cluster (the typed
// name for a protected namespace) and changes nothing.
func (t *twin) Act(_ context.Context, action string, _ auth.Principal, input any) (any, error) {
	in, ok := input.(*restartInput)
	if action != "restart" || !ok {
		return nil, &plugin.ActionError{Status: 404, Code: model.ErrCodeNotFound, Message: "no such action"}
	}
	r := t.rt.Load()
	if r == nil {
		return nil, errNotRunning
	}
	r.twin.mu.RLock()
	defer r.twin.mu.RUnlock()
	for _, ns := range r.twin.cl.Namespaces {
		if ns.Name == in.Namespace && ns.Protected && in.Confirm != in.Name {
			return nil, &plugin.ActionError{Status: 400, Code: model.ErrCodeInvalidInput, Message: "type the workload name to confirm"}
		}
	}
	return nil, nil
}

// twinPodDetail answers the pod detail from the world's pods.
func (s *twinSource) podDetail(ns, name string) (model.PodDetail, bool) {
	s.mu.RLock()
	cl := s.cl
	s.mu.RUnlock()
	for _, p := range cl.Pods {
		if p.Namespace != ns || p.Name != name {
			continue
		}
		var chain []model.OwnerRef
		if p.Owner != nil {
			if p.Owner.Kind == string(model.WorkloadKindDeployment) {
				chain = append(chain, model.OwnerRef{Kind: "ReplicaSet", Name: p.Owner.Name + "-7d9c5b8f4"})
			}
			chain = append(chain, *p.Owner)
		}
		ready := p.ReadyContainers == p.TotalContainers
		state := "running"
		if !ready {
			state = "waiting: " + p.Reason
		}
		events := []model.Event{}
		for _, e := range cl.Events {
			if e.Namespace == ns && strings.HasSuffix(e.Object, "/"+name) {
				events = append(events, e)
			}
		}
		return model.PodDetail{
			Pod: p,
			IPs: []string{fmt.Sprintf("198.51.100.%d", 10+len(name)%200)},
			Conditions: []model.Condition{
				{Type: "PodScheduled", Status: "True"},
				{Type: "Initialized", Status: "True"},
				{Type: "ContainersReady", Status: boolStatus(ready)},
				{Type: "Ready", Status: boolStatus(ready)},
			},
			Containers: []model.Container{{Name: "app", Image: imageOf(cl.Workloads, p), State: state, Ready: ready, Restarts: p.Restarts}},
			OwnerChain: chain,
			Events:     events,
		}, true
	}
	return model.PodDetail{}, false
}

func boolStatus(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func imageOf(ws []model.Workload, p model.Pod) string {
	for _, w := range ws {
		if p.Owner != nil && w.Namespace == p.Namespace && w.Name == p.Owner.Name && len(w.Images) > 0 {
			return w.Images[0]
		}
	}
	return "ghcr.io/example/app:1.0.0"
}

// decorateCluster gives the world's objects the refs the live builders give.
func decorateCluster(c model.ClusterData) model.ClusterData {
	for i := range c.Nodes {
		c.Nodes[i].Ref = plugin.Ref(ID, "Node", c.Nodes[i].Name)
	}
	for i := range c.Namespaces {
		c.Namespaces[i].Ref = plugin.Ref(ID, "Namespace", c.Namespaces[i].Name)
	}
	for i, w := range c.Workloads {
		c.Workloads[i].Ref = plugin.Ref(ID, "Workload", string(w.Kind), w.Namespace, w.Name)
	}
	for i, p := range c.Pods {
		c.Pods[i].Ref = plugin.Ref(ID, "Pod", p.Namespace, p.Name)
	}
	for i, x := range c.Certificates {
		c.Certificates[i].Ref = plugin.Ref(ID, "Certificate", x.Namespace, x.Name)
	}
	for i, x := range c.ExternalSecrets {
		c.ExternalSecrets[i].Ref = plugin.Ref(ID, "ExternalSecret", x.Namespace, x.Name)
	}
	return c
}

// decorateStorage gives the world's volumes and backups their refs.
func decorateStorage(s model.StorageData) model.StorageData {
	for i, p := range s.PVCs {
		s.PVCs[i].Ref = plugin.Ref(ID, "PVC", p.Namespace, p.Name)
	}
	for i, j := range s.BackupJobs {
		s.BackupJobs[i].Ref = plugin.Ref(ID, "BackupJob", "backup", j.Name)
	}
	if s.BackupJobs == nil {
		s.BackupJobs = []model.BackupJob{}
	}
	return s
}

// decorateApps gives the world's apps their refs: the world has no route behind an app, so the
// route is named after the app.
func decorateApps(a model.AppsData) model.AppsData {
	for i, app := range a.Apps {
		host := strings.TrimPrefix(app.Href, "https://")
		route := strings.ToLower(strings.ReplaceAll(app.Name, " ", "-"))
		a.Apps[i].Ref = plugin.Ref(ID, "App", app.Namespace, "httproute", route, host)
	}
	return a
}
