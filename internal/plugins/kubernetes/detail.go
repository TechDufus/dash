package kubernetes

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

// Routes implements plugin.Router: GET /api/v1/plugins/kubernetes/pods/{ns}/{name}. The route
// names no namespace for the core's read-deny check: the pod sheet stays readable in a namespace
// the restart action refuses.
func (p *Plugin) Routes(r plugin.Routes) {
	r.Get("/pods/{ns}/{name}", p.podDetail, plugin.RouteSpec{
		Summary:  "The detail of one pod: containers, conditions, owner chain and events",
		Scope:    "read",
		Response: model.PodDetail{},
	})
}

var (
	errInvalidPod  = &plugin.ActionError{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Message: "invalid namespace or pod name"}
	errPodNotFound = &plugin.ActionError{Status: http.StatusNotFound, Code: model.ErrCodeNotFound, Message: "pod not found"}
)

// podDetail answers one pod. Both names are checked before anything is read; an unknown pod is a
// 404, and any other failure is the core's category-only 502.
func (p *Plugin) podDetail(w http.ResponseWriter, req *http.Request) {
	ns, name := req.PathValue("ns"), req.PathValue("name")
	if !queries.ValidDNS(ns) || !queries.ValidDNS(name) {
		plugin.Fail(w, req, errInvalidPod)
		return
	}
	r := p.rt.Load()
	if r == nil {
		plugin.Fail(w, req, errNotRunning)
		return
	}
	if r.twin != nil { // the demo twin answers from the fake world
		d, ok := r.twin.podDetail(ns, name)
		if !ok {
			plugin.Fail(w, req, errPodNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(d)
		return
	}
	d, err := podDetailOf(req.Context(), r.clusterInput(), ns, name)
	switch {
	case apierrors.IsNotFound(err):
		plugin.Fail(w, req, errPodNotFound)
		return
	case err != nil:
		plugin.Fail(w, req, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(d)
}

// podDetailOf describes one pod. Its events come live from the API, Normal ones included; when
// the API cannot list them the list is empty and the rest still answers.
func podDetailOf(ctx context.Context, in clusterInput, ns, name string) (model.PodDetail, error) {
	p, err := in.c.Pods.Pods(ns).Get(name)
	if err != nil {
		return model.PodDetail{}, err
	}
	res := in.c.Resolver()
	d := model.PodDetail{
		Pod: buildPod(p, in, res), IPs: []string{}, Conditions: []model.Condition{},
		Containers: []model.Container{}, OwnerChain: []model.OwnerRef{}, Events: []model.Event{},
	}
	for _, ip := range p.Status.PodIPs {
		d.IPs = append(d.IPs, ip.IP)
	}
	if len(d.IPs) == 0 && p.Status.PodIP != "" {
		d.IPs = append(d.IPs, p.Status.PodIP)
	}
	for _, c := range p.Status.Conditions {
		d.Conditions = append(d.Conditions, model.Condition{Type: string(c.Type), Status: string(c.Status)})
	}
	d.Containers = podContainers(p)
	for _, o := range res.Chain(p) {
		d.OwnerChain = append(d.OwnerChain, model.OwnerRef{Kind: o.Kind, Name: o.Name})
	}

	list, err := in.c.Client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.kind=Pod,involvedObject.name=" + name,
	})
	if err == nil {
		for i := range list.Items {
			d.Events = append(d.Events, eventOf(&list.Items[i], int(list.Items[i].Count)))
		}
		sort.Slice(d.Events, func(i, j int) bool { return d.Events[i].LastSeen.After(d.Events[j].LastSeen) })
	}
	return d, nil
}
