package kubernetes

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/internal/cond"
	"github.com/TechDufus/dash/internal/plugins/internal/health"
)

const (
	labelFluxName   = "kustomize.toolkit.fluxcd.io/name"
	labelPSAEnforce = "pod-security.kubernetes.io/enforce"
	maxEvents       = 100
)

// nsInfo is the classification of a namespace.
type nsInfo struct {
	group      model.Group
	protected  bool
	tenant     string
	privileged bool
}

func classifyNamespace(labels map[string]string, conv pluginconf.KubernetesConventions) nsInfo {
	info := nsInfo{
		group:      model.GroupPlatform,
		protected:  conv.ProtectedLabel != "" && labels[conv.ProtectedLabel] == "true",
		privileged: labels[labelPSAEnforce] == "privileged",
	}
	if conv.TenantLabel != "" {
		info.tenant = labels[conv.TenantLabel]
	}
	switch _, isTenant := labels[conv.TenantLabel]; {
	case conv.TenantLabel != "" && isTenant:
		info.group = model.GroupTenants
	case conv.AppsFluxPrefix != "" && strings.HasPrefix(labels[labelFluxName], conv.AppsFluxPrefix):
		info.group = model.GroupApps
	}
	return info
}

func namespaceInfosOf(list []*corev1.Namespace, conv pluginconf.KubernetesConventions) map[string]nsInfo {
	out := make(map[string]nsInfo, len(list))
	for _, ns := range list {
		out[ns.Name] = classifyNamespace(ns.Labels, conv)
	}
	return out
}

// clusterInput is everything buildCluster and the pod detail read.
type clusterInput struct {
	c     *kube.Cluster
	conv  pluginconf.KubernetesConventions
	usage usageSnap
	links plugin.LinkResolver
	now   time.Time
}

// worst is the most severe of the healths (crit, then warn, then unknown); no argument is ok.
func worst(hs ...model.Health) model.Health {
	w := model.HealthOK
	for _, h := range hs {
		if healthRank(h) > healthRank(w) {
			w = h
		}
	}
	return w
}

func healthRank(h model.Health) int {
	switch h {
	case model.HealthCrit:
		return 3
	case model.HealthWarn:
		return 2
	case model.HealthUnknown:
		return 1
	}
	return 0
}

// waitingReasons lists the waiting reasons of a pod's containers (init containers included).
func waitingReasons(p *corev1.Pod) []string {
	var out []string
	for _, group := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range group {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				out = append(out, cs.State.Waiting.Reason)
			}
		}
	}
	return out
}

// podReason is why a pod is not (fully) running: the first waiting reason, the reason a container
// ended in a pod that is neither running nor done, the pod's own reason, or Completed.
func podReason(p *corev1.Pod) string {
	if w := waitingReasons(p); len(w) > 0 {
		return w[0]
	}
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.Reason != "" && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodRunning {
			return t.Reason
		}
	}
	if p.Status.Reason != "" {
		return p.Status.Reason
	}
	if p.Status.Phase == corev1.PodSucceeded {
		return "Completed"
	}
	return ""
}

// buildPod is one pod row: containers ready, restarts, the top of the owner chain and the usage
// the last poll found.
func buildPod(p *corev1.Pod, in clusterInput, res kube.Resolver) model.Pod {
	var ready, restarts int
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += int(cs.RestartCount)
	}
	for _, cs := range p.Status.InitContainerStatuses {
		restarts += int(cs.RestartCount)
	}
	pod := model.Pod{
		Ref:       plugin.Ref(ID, "Pod", p.Namespace, p.Name),
		Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), Reason: podReason(p),
		ReadyContainers: ready, TotalContainers: len(p.Spec.Containers), Restarts: restarts, Node: p.Spec.NodeName,
		CreatedAt: p.CreationTimestamp.UTC(),
		Health:    health.PodHealth(string(p.Status.Phase), waitingReasons(p), p.CreationTimestamp.Time, in.now),
		Links:     linksOf(in.links, "Pod", p.Namespace, p.Name),
	}
	key := p.Namespace + "/" + p.Name
	if v, ok := in.usage.podCPU[key]; ok {
		pod.CPUCores = new(v)
	}
	if v, ok := in.usage.podMem[key]; ok {
		pod.MemBytes = new(v)
	}
	if top, ok := res.Top(p); ok {
		pod.Owner = &model.OwnerRef{Kind: top.Kind, Name: top.Name}
	}
	return pod
}

// buildCluster is the kubernetes.cluster payload.
func buildCluster(in clusterInput) model.ClusterData {
	c := in.c
	res := c.Resolver()
	nsList := c.ListNamespaces() // listed once: the classification and the namespace rows must agree
	infos := namespaceInfosOf(nsList, in.conv)

	out := model.ClusterData{
		Nodes: []model.Node{}, Namespaces: []model.Namespace{}, Workloads: []model.Workload{}, Pods: []model.Pod{},
		Events: []model.Event{}, Certificates: []model.Cert{}, ExternalSecrets: []model.ExtSecret{},
	}

	podsPerNode := map[string]int{}
	nsPods := map[string][]model.Pod{}
	for _, p := range c.ListPods() {
		pod := buildPod(p, in, res)
		out.Pods = append(out.Pods, pod)
		nsPods[p.Namespace] = append(nsPods[p.Namespace], pod)
		if p.Spec.NodeName != "" && (p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending) {
			podsPerNode[p.Spec.NodeName]++
		}
	}
	sort.Slice(out.Pods, func(i, j int) bool {
		if out.Pods[i].Namespace != out.Pods[j].Namespace {
			return out.Pods[i].Namespace < out.Pods[j].Namespace
		}
		return out.Pods[i].Name < out.Pods[j].Name
	})

	for _, n := range c.ListNodes() {
		ip := nodeIP(n)
		inst := kube.InstanceOf(ip)
		out.Nodes = append(out.Nodes, model.Node{
			Ref:  plugin.Ref(ID, "Node", n.Name),
			Name: n.Name, IP: ip, Ready: nodeReady(n.Status.Conditions), KubeletVersion: n.Status.NodeInfo.KubeletVersion,
			CPURatio: in.usage.nodeCPU[inst], MemRatio: in.usage.nodeMem[inst], Pods: podsPerNode[n.Name],
			Links: linksOf(in.links, "Node", "", n.Name),
		})
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Name < out.Nodes[j].Name })

	out.Workloads = buildWorkloads(in, infos)

	notReady := map[string]int{}
	workloadHealth := map[string]model.Health{}
	for _, w := range out.Workloads {
		if w.Health != model.HealthOK {
			notReady[w.Namespace]++
		}
		workloadHealth[w.Namespace] = worst(workloadHealth[w.Namespace], w.Health)
	}
	for _, ns := range nsList {
		info := infos[ns.Name]
		n := model.Namespace{
			Ref:  plugin.Ref(ID, "Namespace", ns.Name),
			Name: ns.Name, Group: info.group, Protected: info.protected, Tenant: info.tenant, Privileged: info.privileged,
			WorkloadsNotReady: notReady[ns.Name], Health: model.HealthOK,
		}
		for _, p := range nsPods[ns.Name] {
			n.Restarts += p.Restarts
			if p.Phase != string(corev1.PodSucceeded) {
				n.PodsTotal++
			}
			if p.Phase == string(corev1.PodRunning) {
				n.PodsRunning++
			}
			n.Health = worst(n.Health, p.Health)
		}
		n.Health = worst(n.Health, workloadHealth[ns.Name])
		out.Namespaces = append(out.Namespaces, n)
	}
	sort.Slice(out.Namespaces, func(i, j int) bool { return out.Namespaces[i].Name < out.Namespaces[j].Name })

	for _, e := range c.ListEvents() {
		count := int(e.Count)
		if e.Series != nil && int(e.Series.Count) > count {
			count = int(e.Series.Count)
		}
		out.Events = append(out.Events, eventOf(e, count))
	}
	sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].LastSeen.After(out.Events[j].LastSeen) })
	if len(out.Events) > maxEvents {
		out.Events = out.Events[:maxEvents]
	}

	for _, u := range c.List(kube.GVRCertificates) {
		cert := model.Cert{
			Ref:       plugin.Ref(ID, "Certificate", u.GetNamespace(), u.GetName()),
			Namespace: u.GetNamespace(), Name: u.GetName(), Ready: cond.True(u, "Ready"),
			Links: linksOf(in.links, "Certificate", u.GetNamespace(), u.GetName()),
		}
		if s, ok, _ := unstructured.NestedString(u.Object, "status", "notAfter"); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				cert.NotAfter = new(t.UTC())
			}
		}
		out.Certificates = append(out.Certificates, cert)
	}
	sort.Slice(out.Certificates, func(i, j int) bool {
		return out.Certificates[i].Namespace+"/"+out.Certificates[i].Name < out.Certificates[j].Namespace+"/"+out.Certificates[j].Name
	})

	for _, u := range c.List(kube.GVRExternalSecrets) {
		_, msg := cond.Of(u, "Ready")
		out.ExternalSecrets = append(out.ExternalSecrets, model.ExtSecret{
			Ref:       plugin.Ref(ID, "ExternalSecret", u.GetNamespace(), u.GetName()),
			Namespace: u.GetNamespace(), Name: u.GetName(), Ready: cond.True(u, "Ready"), Message: msg,
		})
	}
	sort.Slice(out.ExternalSecrets, func(i, j int) bool {
		return out.ExternalSecrets[i].Namespace+"/"+out.ExternalSecrets[i].Name < out.ExternalSecrets[j].Namespace+"/"+out.ExternalSecrets[j].Name
	})
	return out
}

// eventOf is one event row; the time is the last time it happened, from whichever field the
// cluster filled.
func eventOf(e *corev1.Event, count int) model.Event {
	last := e.LastTimestamp.Time
	if last.IsZero() {
		last = e.EventTime.Time
	}
	if last.IsZero() {
		last = e.CreationTimestamp.Time
	}
	return model.Event{
		Namespace: e.Namespace, Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, Reason: e.Reason,
		Message: e.Message, Count: max(count, 1), LastSeen: last.UTC(),
	}
}

// buildWorkloads lists the cluster's controllers; only Deployments, StatefulSets and DaemonSets
// are restartable and carry a health.
func buildWorkloads(in clusterInput, infos map[string]nsInfo) []model.Workload {
	c := in.c
	out := []model.Workload{}
	add := func(kind model.WorkloadKind, ns, name string, ready, desired int, spec corev1.PodSpec, restartable bool) {
		images := make([]string, 0, len(spec.Containers))
		for _, ct := range spec.Containers {
			images = append(images, ct.Image)
		}
		h := model.HealthOK
		if restartable {
			h = health.WorkloadHealth(ready, desired)
		}
		out = append(out, model.Workload{
			Ref:  plugin.Ref(ID, "Workload", string(kind), ns, name),
			Kind: kind, Namespace: ns, Name: name, Ready: ready, Desired: desired, Images: images, Health: h,
			Restartable: restartable, Protected: infos[ns].protected, Links: linksOf(in.links, string(kind), ns, name),
		})
	}
	for _, d := range c.ListDeployments() {
		desired := 1
		if d.Spec.Replicas != nil {
			desired = int(*d.Spec.Replicas)
		}
		add(model.WorkloadKindDeployment, d.Namespace, d.Name, int(d.Status.ReadyReplicas), desired, d.Spec.Template.Spec, true)
	}
	for _, s := range c.ListStatefulSets() {
		desired := 1
		if s.Spec.Replicas != nil {
			desired = int(*s.Spec.Replicas)
		}
		add(model.WorkloadKindStatefulSet, s.Namespace, s.Name, int(s.Status.ReadyReplicas), desired, s.Spec.Template.Spec, true)
	}
	for _, d := range c.ListDaemonSets() {
		add(model.WorkloadKindDaemonSet, d.Namespace, d.Name, int(d.Status.NumberReady), int(d.Status.DesiredNumberScheduled), d.Spec.Template.Spec, true)
	}
	for _, j := range c.ListJobs() {
		desired := 1
		if j.Spec.Completions != nil {
			desired = int(*j.Spec.Completions)
		}
		add(model.WorkloadKindJob, j.Namespace, j.Name, int(j.Status.Succeeded), desired, j.Spec.Template.Spec, false)
	}
	for _, j := range c.ListCronJobs() {
		add(model.WorkloadKindCronJob, j.Namespace, j.Name, len(j.Status.Active), 0, j.Spec.JobTemplate.Spec.Template.Spec, false)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return out
}

// podContainers is the containers of a pod spec with the state their status reports.
func podContainers(p *corev1.Pod) []model.Container {
	status := map[string]corev1.ContainerStatus{}
	for _, cs := range p.Status.ContainerStatuses {
		status[cs.Name] = cs
	}
	out := []model.Container{}
	for _, c := range p.Spec.Containers {
		cs := status[c.Name]
		ct := model.Container{Name: c.Name, Image: c.Image, Ready: cs.Ready, Restarts: int(cs.RestartCount), State: "waiting"}
		switch {
		case cs.State.Running != nil:
			ct.State = "running"
		case cs.State.Terminated != nil:
			ct.State = "terminated"
			if r := cs.State.Terminated.Reason; r != "" {
				ct.State += ": " + r
			}
		case cs.State.Waiting != nil:
			if r := cs.State.Waiting.Reason; r != "" {
				ct.State = "waiting: " + r
			}
		}
		if lt := cs.LastTerminationState.Terminated; lt != nil {
			ct.LastTerminationReason = lt.Reason
			if lt.ExitCode != 0 && lt.Reason != "" {
				ct.LastTerminationReason = fmt.Sprintf("%s (exit %d)", lt.Reason, lt.ExitCode)
			}
		}
		out = append(out, ct)
	}
	return out
}
