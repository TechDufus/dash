package kubernetes

import (
	"fmt"
	"math"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugins/internal/health"
)

// attentionOf is the attention items of the cluster and storage payloads, in rule order: nodes,
// pods, workloads, certificates, PVCs. Only warnings and critical items are listed. An
// item has a Since of its own only where the cluster gives one (a pod's creation); the others
// are left zero for the caller to stamp with the time they were first seen.
func attentionOf(cl model.ClusterData, st model.StorageData, now time.Time) []model.AttentionItem {
	items := []model.AttentionItem{}
	add := func(id, ref string, sev model.Health, source, title, detail string, since time.Time, route string, links []model.Link) {
		if sev != model.HealthWarn && sev != model.HealthCrit {
			return
		}
		if links == nil {
			links = []model.Link{}
		}
		items = append(items, model.AttentionItem{
			ID: id, Severity: model.Severity(sev), Source: source, Plugin: string(ID), Title: title, Detail: detail,
			Since: since, Route: route, Links: links, Ref: ref,
		})
	}

	for _, n := range cl.Nodes {
		if !n.Ready {
			add("node:"+n.Name, plugin.Ref(ID, "Node", n.Name), model.HealthCrit, "kubernetes", "Node "+n.Name+" not ready", n.IP,
				time.Time{}, "/cluster", n.Links)
		}
	}
	for _, p := range cl.Pods {
		if p.Health != model.HealthWarn && p.Health != model.HealthCrit {
			continue
		}
		detail := p.Reason
		if p.Health == model.HealthWarn && detail == "" {
			detail = "Pending for over 5 minutes"
		}
		add("pod:"+p.Namespace+"/"+p.Name, plugin.Ref(ID, "Pod", p.Namespace, p.Name), p.Health, "kubernetes",
			fmt.Sprintf("Pod %s/%s %s", p.Namespace, p.Name, podProblem(p)), detail, p.CreatedAt,
			"/cluster/pods/"+p.Namespace+"/"+p.Name, p.Links)
	}
	for _, w := range cl.Workloads {
		add("workload:"+string(w.Kind)+"/"+w.Namespace+"/"+w.Name, plugin.Ref(ID, "Workload", string(w.Kind), w.Namespace, w.Name),
			w.Health, "kubernetes", fmt.Sprintf("%s %s/%s not ready", w.Kind, w.Namespace, w.Name),
			fmt.Sprintf("%d of %d ready", w.Ready, w.Desired), time.Time{}, "/cluster", w.Links)
	}
	for _, c := range cl.Certificates {
		if c.NotAfter == nil {
			continue
		}
		add("cert:"+c.Namespace+"/"+c.Name, plugin.Ref(ID, "Certificate", c.Namespace, c.Name), health.CertHealth(*c.NotAfter, now),
			"cert-manager", fmt.Sprintf("Certificate %s/%s expires soon", c.Namespace, c.Name),
			"expires "+c.NotAfter.UTC().Format("2006-01-02")+" ("+fmt.Sprintf("%.0f", math.Floor(c.NotAfter.Sub(now).Hours()/24))+" days)",
			time.Time{}, "/cluster", c.Links)
	}
	for _, p := range st.PVCs {
		if p.UsedRatio == nil {
			continue
		}
		add("pvc:"+p.Namespace+"/"+p.Name, plugin.Ref(ID, "PVC", p.Namespace, p.Name), health.RatioHealth(*p.UsedRatio), "storage",
			fmt.Sprintf("PVC %s/%s is %.0f%% full", p.Namespace, p.Name, *p.UsedRatio*100), "", time.Time{}, "/storage", p.Links)
	}
	return items
}

func podProblem(p model.Pod) string {
	if p.Health == model.HealthCrit {
		return "is failing"
	}
	return "is stuck pending"
}
