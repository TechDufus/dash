package world

import (
	"fmt"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

type nsSpec struct {
	name      string
	group     model.Group
	protected bool
	tenant    string
	apps      []appSpec
}

type appSpec struct {
	name, image string
	kind        model.WorkloadKind
	replicas    int
}

var namespaces = []nsSpec{
	{"kube-system", model.GroupPlatform, true, "", []appSpec{
		{"cilium-operator", "quay.io/cilium/operator-generic:v1.18.2", model.WorkloadKindDeployment, 1},
		{"coredns", "registry.k8s.io/coredns/coredns:v1.12.3", model.WorkloadKindDeployment, 2},
		{"cilium", "quay.io/cilium/cilium:v1.18.2", model.WorkloadKindDaemonSet, 3},
	}},
	{FluxNamespace, model.GroupPlatform, true, "", []appSpec{
		{"source-controller", "ghcr.io/fluxcd/source-controller:v1.7.1", model.WorkloadKindDeployment, 1},
		{"kustomize-controller", "ghcr.io/fluxcd/kustomize-controller:v1.7.0", model.WorkloadKindDeployment, 1},
		{"helm-controller", "ghcr.io/fluxcd/helm-controller:v1.4.1", model.WorkloadKindDeployment, 1},
	}},
	{"cert-manager", model.GroupPlatform, true, "", []appSpec{
		{"cert-manager", "quay.io/jetstack/cert-manager-controller:v1.19.1", model.WorkloadKindDeployment, 1},
	}},
	{"external-secrets", model.GroupPlatform, true, "", []appSpec{
		{"external-secrets", "ghcr.io/external-secrets/external-secrets:v0.20.3", model.WorkloadKindDeployment, 1},
	}},
	{"monitoring", model.GroupPlatform, false, "", []appSpec{
		{"vmsingle", "victoriametrics/victoria-metrics:v1.127.0", model.WorkloadKindDeployment, 1},
		{"vmalertmanager", "prom/alertmanager:v0.28.1", model.WorkloadKindStatefulSet, 1},
		{"grafana", "grafana/grafana:12.2.0", model.WorkloadKindDeployment, 1},
		{"node-exporter", "quay.io/prometheus/node-exporter:v1.9.1", model.WorkloadKindDaemonSet, 3},
	}},
	{"cnpg-system", model.GroupPlatform, false, "", []appSpec{
		{"cnpg-controller-manager", "ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0", model.WorkloadKindDeployment, 1},
	}},
	{"dash", model.GroupApps, false, "", []appSpec{
		{"dash", "registry.example.com/dash/dash:0.4.0", model.WorkloadKindDeployment, 1},
	}},
	{"gatus", model.GroupApps, false, "", []appSpec{
		{"gatus", "ghcr.io/twin/gatus:v5.26.0", model.WorkloadKindDeployment, 1},
	}},
	{"headlamp", model.GroupApps, false, "", []appSpec{
		{"headlamp", "ghcr.io/headlamp-k8s/headlamp:v0.36.0", model.WorkloadKindDeployment, 1},
	}},
	{"homepage", model.GroupApps, false, "", []appSpec{
		{"homepage", "ghcr.io/gethomepage/homepage:v1.5.0", model.WorkloadKindDeployment, 1},
	}},
	{"registry", model.GroupApps, false, "", []appSpec{
		{"zot", "ghcr.io/project-zot/zot-linux-amd64:v2.1.8", model.WorkloadKindStatefulSet, 1},
	}},
	{"acme", model.GroupTenants, false, "acme", []appSpec{
		{"web", "registry.example.com/acme/web:2.3.1", model.WorkloadKindDeployment, 2},
		{"api", "registry.example.com/acme/api:2.3.1", model.WorkloadKindDeployment, 2},
		{"worker", "registry.example.com/acme/worker:2.3.1", model.WorkloadKindDeployment, 1},
	}},
}

var nodeNames = []string{"k8s-cp-1", "k8s-cp-2", "k8s-cp-3"}

// crashing is the one unhealthy pod, so the attention list has something to show.
const crashingNS, crashingOwner = "acme", "worker"

// podName is the generated name of replica r of app a (index ai in its namespace).
func podName(a appSpec, ai, r int) string {
	suffix := fmt.Sprintf("%05x", (len(a.name)*7919+r*104729+ai*31)%0xfffff)
	switch a.kind {
	case model.WorkloadKindStatefulSet:
		return fmt.Sprintf("%s-%d", a.name, r)
	case model.WorkloadKindDeployment:
		return a.name + "-7d9c5b8f4-" + suffix[:5]
	}
	return a.name + "-" + suffix
}

// crashPodName is the name of the one crash-looping pod.
func crashPodName() string {
	for _, ns := range namespaces {
		if ns.name != crashingNS {
			continue
		}
		for ai, a := range ns.apps {
			if a.name == crashingOwner {
				return podName(a, ai, 0)
			}
		}
	}
	return ""
}

// Cluster is the Kubernetes payload: workloads, pods, nodes, events, certificates and
// external secrets.
func (w *World) Cluster() model.ClusterData { return w.cluster(w.clock()) }

func (w *World) cluster(now time.Time) model.ClusterData {
	var c model.ClusterData
	podsByNode := map[string]int{}
	for _, ns := range namespaces {
		nsm := model.Namespace{Name: ns.name, Group: ns.group, Protected: ns.protected, Tenant: ns.tenant,
			Privileged: ns.name == "kube-system" || ns.name == "monitoring", Health: model.HealthOK}
		for ai, a := range ns.apps {
			desired, ready := a.replicas, a.replicas
			h := model.HealthOK
			if ns.name == crashingNS && a.name == crashingOwner {
				ready, h = 0, model.HealthCrit
				nsm.WorkloadsNotReady++
				nsm.Health = model.HealthCrit
			}
			c.Workloads = append(c.Workloads, model.Workload{
				Kind: a.kind, Namespace: ns.name, Name: a.name, Ready: ready, Desired: desired,
				Images: []string{a.image}, Health: h, Restartable: true, Protected: ns.protected,
				Links: w.links.Headlamp(string(a.kind), ns.name, a.name),
			})
			for r := range desired {
				node := nodeNames[(ai+r+len(ns.name))%len(nodeNames)]
				if a.kind == model.WorkloadKindDaemonSet {
					node = nodeNames[r%len(nodeNames)]
				}
				name := podName(a, ai, r)
				key := ns.name + "/" + name
				cpu := Around(key+"cpu", now, 0.03+0.01*float64(len(a.name)%5), 0.015)
				mem := Around(key+"mem", now, float64(48+len(a.name)*9)*(1<<20), 6*(1<<20))
				p := model.Pod{
					Namespace: ns.name, Name: name, Phase: "Running", ReadyContainers: 1, TotalContainers: 1,
					Node: node, CreatedAt: ago(now, time.Duration(20+ai*13+r)*time.Hour),
					CPUCores: new(cpu), MemBytes: new(mem),
					Owner:  &model.OwnerRef{Kind: string(a.kind), Name: a.name},
					Health: model.HealthOK, Links: w.links.Headlamp("Pod", ns.name, name),
				}
				if h == model.HealthCrit {
					p.ReadyContainers, p.Restarts, p.Reason, p.Health = 0, 14, "CrashLoopBackOff", model.HealthCrit
					p.CreatedAt = ago(now, 47*time.Minute)
				}
				c.Pods = append(c.Pods, p)
				nsm.PodsTotal++
				if p.Health == model.HealthOK {
					nsm.PodsRunning++
				}
				nsm.Restarts += p.Restarts
				podsByNode[node]++
			}
		}
		c.Namespaces = append(c.Namespaces, nsm)
	}
	for i, n := range nodeNames {
		c.Nodes = append(c.Nodes, model.Node{
			Name: n, IP: fmt.Sprintf("192.0.2.%d", 11+i), Ready: true, KubeletVersion: "v1.34.1",
			CPURatio: Around(n+"cpu", now, 0.24+0.05*float64(i), 0.08),
			MemRatio: Around(n+"mem", now, 0.58+0.04*float64(i), 0.04),
			Pods:     podsByNode[n], Links: w.links.Headlamp("Node", "", n),
		})
	}
	crashPod := crashPodName()
	c.Events = []model.Event{
		{Namespace: crashingNS, Object: "Pod/" + crashPod, Reason: "BackOff", Message: "Back-off restarting failed container app", Count: 14, LastSeen: ago(now, 40*time.Second)},
		{Namespace: "acme", Object: "Deployment/web", Reason: "ScalingReplicaSet", Message: "Scaled up replica set web-7d9c5b8f4 from 1 to 2", Count: 1, LastSeen: ago(now, 18*time.Minute)},
		{Namespace: "monitoring", Object: "Pod/vmsingle-7d9c5b8f4-1a2b3", Reason: "Pulled", Message: "Container image already present on machine", Count: 1, LastSeen: ago(now, 3*time.Hour)},
		{Namespace: "cert-manager", Object: "Certificate/wildcard-example", Reason: "Issued", Message: "The certificate has been successfully issued", Count: 1, LastSeen: ago(now, 6*time.Hour)},
	}
	for _, cert := range []struct {
		ns, name string
		days     int
	}{{"cert-manager", "wildcard-example", 61}, {"dash", "dash-tls", 74}, {"acme", "acme-web-tls", 12}} {
		c.Certificates = append(c.Certificates, model.Cert{
			Namespace: cert.ns, Name: cert.name, Ready: true, NotAfter: agoP(now, -time.Duration(cert.days)*24*time.Hour),
			Links: w.links.Headlamp("Certificate", cert.ns, cert.name),
		})
	}
	c.ExternalSecrets = []model.ExtSecret{
		{Namespace: "dash", Name: "dash-oauth", Ready: true, Message: "secret synced"},
		{Namespace: "monitoring", Name: "grafana-admin", Ready: true, Message: "secret synced"},
		{Namespace: "acme", Name: "acme-db", Ready: true, Message: "secret synced"},
	}
	return c
}
