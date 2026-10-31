package topics

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
)

const (
	labelProtected  = "homelab.io/protected"
	labelTenant     = "homelab.io/tenant"
	labelFluxName   = "kustomize.toolkit.fluxcd.io/name"
	labelPSAEnforce = "pod-security.kubernetes.io/enforce"
	maxEvents       = 100
)

type usageSnap struct {
	podCPU, podMem   map[string]float64 // "<ns>/<pod>"
	nodeCPU, nodeMem map[string]float64 // instance "<ip>:9100"
}

func (m *Manager) pollUsage(ctx context.Context) {
	res := m.queryAll(ctx, map[string]string{
		"podCPU": queries.PodCPU, "podMem": queries.PodMem, "nodeCPU": queries.NodeCPU, "nodeMem": queries.NodeMem,
	})
	m.setSource("cluster:victoriametrics", firstErr(res))
	m.mu.Lock()
	if res["podCPU"].err == nil {
		m.usage.podCPU = byLabels(res["podCPU"].samples, "namespace", "pod")
	}
	if res["podMem"].err == nil {
		m.usage.podMem = byLabels(res["podMem"].samples, "namespace", "pod")
	}
	if res["nodeCPU"].err == nil {
		m.usage.nodeCPU = byLabel(res["nodeCPU"].samples, "instance")
	}
	if res["nodeMem"].err == nil {
		m.usage.nodeMem = byLabel(res["nodeMem"].samples, "instance")
	}
	m.mu.Unlock()
	m.mark(model.TopicCluster)
}

func nodeReady(conds []corev1.NodeCondition) bool {
	for _, c := range conds {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func nodeIP(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

// NodeIP returns a Kubernetes node's InternalIP.
func (m *Manager) NodeIP(name string) (string, bool) {
	n, err := m.d.Cluster.Nodes.Get(name)
	if err != nil {
		return "", false
	}
	ip := nodeIP(n)
	return ip, ip != ""
}

// nsInfo is the classification of a namespace.
type nsInfo struct {
	group      model.Group
	protected  bool
	tenant     string
	privileged bool
}

func classifyNamespace(labels map[string]string) nsInfo {
	info := nsInfo{
		group:      model.GroupPlatform,
		protected:  labels[labelProtected] == "true",
		tenant:     labels[labelTenant],
		privileged: labels[labelPSAEnforce] == "privileged",
	}
	switch _, isTenant := labels[labelTenant]; {
	case isTenant:
		info.group = model.GroupTenants
	case strings.HasPrefix(labels[labelFluxName], "app-"):
		info.group = model.GroupApps
	}
	return info
}

func (m *Manager) namespaceInfos() map[string]nsInfo {
	return namespaceInfosOf(m.d.Cluster.ListNamespaces())
}

func namespaceInfosOf(list []*corev1.Namespace) map[string]nsInfo {
	out := make(map[string]nsInfo, len(list))
	for _, ns := range list {
		out[ns.Name] = classifyNamespace(ns.Labels)
	}
	return out
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

func (m *Manager) buildPod(p *corev1.Pod, usage usageSnap, res kube.Resolver, now time.Time) model.Pod {
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
		Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), Reason: podReason(p),
		ReadyContainers: ready, TotalContainers: len(p.Spec.Containers), Restarts: restarts, Node: p.Spec.NodeName,
		CreatedAt: p.CreationTimestamp.UTC(),
		Health:    PodHealth(string(p.Status.Phase), waitingReasons(p), p.CreationTimestamp.Time, now),
		Links:     m.d.Links.Headlamp("Pod", p.Namespace, p.Name),
	}
	key := p.Namespace + "/" + p.Name
	if v, ok := usage.podCPU[key]; ok {
		pod.CPUCores = new(v)
	}
	if v, ok := usage.podMem[key]; ok {
		pod.MemBytes = new(v)
	}
	if top, ok := res.Top(p); ok {
		pod.Owner = &model.OwnerRef{Kind: top.Kind, Name: top.Name}
	}
	return pod
}

func (m *Manager) buildCluster(now time.Time) model.ClusterData {
	m.mu.RLock()
	usage := m.usage
	m.mu.RUnlock()
	c := m.d.Cluster
	res := c.Resolver()
	nsList := c.ListNamespaces() // listed once: the classification and the namespace rows must agree
	infos := namespaceInfosOf(nsList)

	out := model.ClusterData{
		Nodes: []model.Node{}, Namespaces: []model.Namespace{}, Workloads: []model.Workload{}, Pods: []model.Pod{},
		Events: []model.Event{}, Certificates: []model.Cert{}, ExternalSecrets: []model.ExtSecret{},
	}

	podsPerNode := map[string]int{}
	nsPods := map[string][]model.Pod{}
	for _, p := range c.ListPods() {
		pod := m.buildPod(p, usage, res, now)
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
		inst := net.JoinHostPort(ip, strconv.Itoa(queries.NodeExporterPort))
		out.Nodes = append(out.Nodes, model.Node{
			Name: n.Name, IP: ip, Ready: nodeReady(n.Status.Conditions), KubeletVersion: n.Status.NodeInfo.KubeletVersion,
			CPURatio: usage.nodeCPU[inst], MemRatio: usage.nodeMem[inst], Pods: podsPerNode[n.Name],
			Links: m.d.Links.Headlamp("Node", "", n.Name),
		})
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Name < out.Nodes[j].Name })

	out.Workloads = m.buildWorkloads(infos)

	notReady := map[string]int{}
	for _, w := range out.Workloads {
		if w.Health != model.HealthOK {
			notReady[w.Namespace]++
		}
	}
	for _, ns := range nsList {
		info := infos[ns.Name]
		n := model.Namespace{
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
			n.Health = Worst(n.Health, p.Health)
		}
		for _, w := range out.Workloads {
			if w.Namespace == ns.Name {
				n.Health = Worst(n.Health, w.Health)
			}
		}
		out.Namespaces = append(out.Namespaces, n)
	}
	sort.Slice(out.Namespaces, func(i, j int) bool { return out.Namespaces[i].Name < out.Namespaces[j].Name })

	for _, e := range c.ListEvents() {
		last := e.LastTimestamp.Time
		if last.IsZero() {
			last = e.EventTime.Time
		}
		if last.IsZero() {
			last = e.CreationTimestamp.Time
		}
		count := int(e.Count)
		if e.Series != nil && int(e.Series.Count) > count {
			count = int(e.Series.Count)
		}
		out.Events = append(out.Events, model.Event{
			Namespace: e.Namespace, Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, Reason: e.Reason,
			Message: e.Message, Count: max(count, 1), LastSeen: last.UTC(),
		})
	}
	sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].LastSeen.After(out.Events[j].LastSeen) })
	if len(out.Events) > maxEvents {
		out.Events = out.Events[:maxEvents]
	}

	for _, u := range c.List(kube.GVRCertificates) {
		cert := model.Cert{Namespace: u.GetNamespace(), Name: u.GetName(), Ready: conditionTrue(u, "Ready"),
			Links: m.d.Links.Headlamp("Certificate", u.GetNamespace(), u.GetName())}
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
		_, msg := conditionOf(u, "Ready")
		out.ExternalSecrets = append(out.ExternalSecrets, model.ExtSecret{
			Namespace: u.GetNamespace(), Name: u.GetName(), Ready: conditionTrue(u, "Ready"), Message: msg,
		})
	}
	sort.Slice(out.ExternalSecrets, func(i, j int) bool {
		return out.ExternalSecrets[i].Namespace+"/"+out.ExternalSecrets[i].Name < out.ExternalSecrets[j].Namespace+"/"+out.ExternalSecrets[j].Name
	})
	return out
}

func (m *Manager) buildWorkloads(infos map[string]nsInfo) []model.Workload {
	c := m.d.Cluster
	var out []model.Workload
	add := func(kind model.WorkloadKind, ns, name string, ready, desired int, spec corev1.PodSpec, restartable bool) {
		images := make([]string, 0, len(spec.Containers))
		for _, ct := range spec.Containers {
			images = append(images, ct.Image)
		}
		h := model.HealthOK
		if restartable {
			h = WorkloadHealth(ready, desired)
		}
		out = append(out, model.Workload{
			Kind: kind, Namespace: ns, Name: name, Ready: ready, Desired: desired, Images: images, Health: h,
			Restartable: restartable, Protected: infos[ns].protected, Links: m.d.Links.Headlamp(string(kind), ns, name),
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
		add(model.WorkloadKindCronJob, j.Namespace, j.Name, int(len(j.Status.Active)), 0, j.Spec.JobTemplate.Spec.Template.Spec, false)
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
	if out == nil {
		return []model.Workload{}
	}
	return out
}

// PodDetail describes one pod; events come live from the API (Normal ones included).
func (m *Manager) PodDetail(ctx context.Context, ns, name string) (model.PodDetail, error) {
	p, err := m.d.Cluster.Pods.Pods(ns).Get(name)
	if err != nil {
		return model.PodDetail{}, ErrNotFound
	}
	m.mu.RLock()
	usage := m.usage
	m.mu.RUnlock()
	res := m.d.Cluster.Resolver()
	d := model.PodDetail{
		Pod: m.buildPod(p, usage, res, m.now()), IPs: []string{}, Conditions: []model.Condition{},
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

	list, err := m.d.Cluster.Client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.kind=Pod,involvedObject.name=" + name,
	})
	if err == nil {
		for _, e := range list.Items {
			last := e.LastTimestamp.Time
			if last.IsZero() {
				last = e.EventTime.Time
			}
			if last.IsZero() {
				last = e.CreationTimestamp.Time
			}
			d.Events = append(d.Events, model.Event{
				Namespace: e.Namespace, Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, Reason: e.Reason,
				Message: e.Message, Count: max(int(e.Count), 1), LastSeen: last.UTC(),
			})
		}
		sort.Slice(d.Events, func(i, j int) bool { return d.Events[i].LastSeen.After(d.Events[j].LastSeen) })
	}
	return d, nil
}

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

// conditionOf returns the status and message of a named status.condition of an unstructured object.
func conditionOf(u *unstructured.Unstructured, typ string) (status, message string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != typ {
			continue
		}
		status, _ = cm["status"].(string)
		message, _ = cm["message"].(string)
		return status, message
	}
	return "", ""
}

func conditionTrue(u *unstructured.Unstructured, typ string) bool {
	s, _ := conditionOf(u, typ)
	return s == "True"
}

// conditionTime returns the lastTransitionTime of a status condition.
func conditionTime(u *unstructured.Unstructured, typ string) *time.Time {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != typ {
			continue
		}
		if s, _ := cm["lastTransitionTime"].(string); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return new(t.UTC())
			}
		}
	}
	return nil
}
