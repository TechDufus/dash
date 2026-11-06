package kubernetes

import (
	"context"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

// maxRelated bounds the related refs of one object (the pods of a node, say).
const maxRelated = 50

var errUnknownObject = plugin.NotFound("unknown object")

// podRef splits kubernetes:Pod/<ns>/<name>.
func podRef(ref string) (ns, name string, ok bool) {
	rest, cut := strings.CutPrefix(ref, string(ID)+":Pod/")
	if !cut {
		return "", "", false
	}
	ns, name, ok = strings.Cut(rest, "/")
	return ns, name, ok && queries.ValidDNS(ns) && queries.ValidDNS(name)
}

// detailOf is the pod detail, from the cluster or, in demo mode, from the fake world.
func (p *Plugin) detailOf(ctx context.Context, ns, name string) (model.PodDetail, error) {
	r := p.rt.Load()
	if r == nil {
		return model.PodDetail{}, errNotRunning
	}
	if r.twin != nil {
		d, ok := r.twin.podDetail(ns, name)
		if !ok {
			return model.PodDetail{}, errUnknownObject
		}
		return d, nil
	}
	d, err := podDetailOf(ctx, r.clusterInput(), ns, name)
	if apierrors.IsNotFound(err) {
		return model.PodDetail{}, errUnknownObject
	}
	return d, err
}

// Get implements plugin.Objects: a pod is its detail with its newest events (at most
// model.ContextMaxEvents); any other object is what the topics carry.
func (p *Plugin) Get(ctx context.Context, ref string) (any, error) {
	if ns, name, ok := podRef(ref); ok {
		d, err := p.detailOf(ctx, ns, name)
		if err != nil {
			return nil, err
		}
		d.Events = plugin.NewestEvents(d.Events)
		return d, nil
	}
	return p.ObjectStore.Get(ctx, ref)
}

// Context implements plugin.Objects. A pod adds its conditions, events, node, workload,
// namespace, CPU and memory series and its logs; a node its series and pods; a workload its pods,
// the summaries and events of the unhealthy ones next to its own and the logs of the worst; a
// namespace its workloads.
func (p *Plugin) Context(ctx context.Context, ref string, o model.ContextOptions) (model.ContextPart, error) {
	part, obj, err := p.Part(ref)
	if err != nil {
		return part, err
	}
	var cl model.ClusterData
	if v, ok := p.Latest(topicCluster); ok {
		cl, _ = v.(model.ClusterData)
	}
	related := func(refs ...string) {
		for _, r := range refs {
			if r != "" && len(part.Related) < maxRelated {
				part.Related = append(part.Related, r)
			}
		}
	}
	switch v := obj.(type) {
	case model.Pod:
		if d, err := p.detailOf(ctx, v.Namespace, v.Name); err == nil {
			part.Conditions = append(part.Conditions, d.Conditions...)
			for _, c := range d.Containers {
				val := c.State + ", restarts " + strconv.Itoa(c.Restarts)
				if c.LastTerminationReason != "" {
					val += ", last termination " + c.LastTerminationReason
				}
				part.Facts = append(part.Facts, model.ContextFact{Name: "container." + c.Name, Value: val})
			}
			if o.Wants(model.ContextIncludeEvents) {
				part.Events = plugin.NewestEvents(d.Events)
			}
		}
		if o.Wants(model.ContextIncludeRelated) {
			if v.Node != "" {
				related(refOf(cl.Nodes, func(n model.Node) bool { return n.Name == v.Node }, func(n model.Node) string { return n.Ref }))
			}
			if v.Owner != nil {
				related(refOf(cl.Workloads, func(w model.Workload) bool {
					return string(w.Kind) == v.Owner.Kind && w.Namespace == v.Namespace && w.Name == v.Owner.Name
				}, func(w model.Workload) string { return w.Ref }))
			}
			related(refOf(cl.Namespaces, func(n model.Namespace) bool { return n.Name == v.Namespace }, func(n model.Namespace) string { return n.Ref }))
		}
		args := map[string]string{"namespace": v.Namespace, "pod": v.Name}
		part.Series = []model.SeriesRef{{Name: "pod.cpu", Args: args}, {Name: "pod.mem", Args: args}}
		part.Logs = &model.LogsRef{Namespace: v.Namespace, Pod: v.Name}
	case model.Node:
		args := map[string]string{"node": v.Name}
		part.Series = []model.SeriesRef{{Name: "node.cpu", Args: args}, {Name: "node.mem", Args: args}}
		if o.Wants(model.ContextIncludeEvents) {
			part.Events = plugin.NewestEvents(eventsFor(cl.Events, "", "Node/"+v.Name))
		}
		if o.Wants(model.ContextIncludeRelated) {
			for _, pod := range cl.Pods {
				if pod.Node == v.Name {
					related(pod.Ref)
				}
			}
		}
	case model.Workload:
		var events []model.Event
		if o.Wants(model.ContextIncludeEvents) {
			events = eventsFor(cl.Events, v.Namespace, string(v.Kind)+"/"+v.Name)
		}
		var worst *model.Pod
		for _, pod := range cl.Pods {
			if pod.Owner == nil || pod.Owner.Kind != string(v.Kind) || pod.Namespace != v.Namespace || pod.Owner.Name != v.Name {
				continue
			}
			if o.Wants(model.ContextIncludeRelated) {
				related(pod.Ref)
			}
			if pod.Health == model.HealthOK {
				continue
			}
			part.Facts = append(part.Facts, model.ContextFact{Name: "pod." + pod.Name, Value: podSummary(pod)})
			if o.Wants(model.ContextIncludeEvents) {
				events = append(events, eventsFor(cl.Events, pod.Namespace, "Pod/"+pod.Name)...)
			}
			if worst == nil || worse(pod, *worst) {
				worst = &pod
			}
		}
		if o.Wants(model.ContextIncludeEvents) {
			part.Events = plugin.NewestEvents(events)
		}
		if worst != nil {
			part.Logs = &model.LogsRef{Namespace: worst.Namespace, Pod: worst.Name}
		}
	case model.Namespace:
		if o.Wants(model.ContextIncludeRelated) {
			for _, w := range cl.Workloads {
				if w.Namespace == v.Name {
					related(w.Ref)
				}
			}
		}
	}
	return part, nil
}

// podSummary is a pod's state in one line, for the bundle of its workload.
func podSummary(p model.Pod) string {
	s := "health " + string(p.Health) + ", phase " + p.Phase
	if p.Reason != "" {
		s += " (" + p.Reason + ")"
	}
	return s + ", ready " + strconv.Itoa(p.ReadyContainers) + "/" + strconv.Itoa(p.TotalContainers) +
		", restarts " + strconv.Itoa(p.Restarts)
}

// worse reports whether a is the better pod to read a workload's logs from than b: one that has
// run a container (started), so has logs, before a Pending pod that never started; then the
// worse health; then the most restarts.
func worse(a, b model.Pod) bool {
	if sa, sb := started(a), started(b); sa != sb {
		return sa
	}
	if ra, rb := healthRank(a.Health), healthRank(b.Health); ra != rb {
		return ra > rb
	}
	return a.Restarts > b.Restarts
}

// started reports whether a pod has run a container: it restarted, has a ready container, or
// left Pending for Running, Succeeded or Failed.
func started(p model.Pod) bool {
	switch p.Phase {
	case "Running", "Succeeded", "Failed":
		return true
	}
	return p.Restarts > 0 || p.ReadyContainers > 0
}

// eventsFor are the events of one object ("Kind/name") in namespace ns ("" for any).
func eventsFor(evs []model.Event, ns, object string) []model.Event {
	out := []model.Event{}
	for _, e := range evs {
		if e.Object == object && (ns == "" || e.Namespace == ns) {
			out = append(out, e)
		}
	}
	return out
}

func refOf[T any](xs []T, match func(T) bool, ref func(T) string) string {
	for _, x := range xs {
		if match(x) {
			return ref(x)
		}
	}
	return ""
}

var _ plugin.Objects = (*Plugin)(nil)
