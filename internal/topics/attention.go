package topics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// AttentionInput is everything the rule table reads. Every field is the typed data of a topic
// (the health fields in it were set with the functions in health.go).
type AttentionInput struct {
	Flux    model.FluxData
	Cluster model.ClusterData
	Storage model.StorageData
	// Resolver resolves the links of the rules' objects (Grafana, Headlamp, the Proxmox UI and
	// shell); nil means none.
	Resolver plugin.LinkResolver
	Now      time.Time
}

// link is the links of one object into the tools whose plugins run: never nil.
func (in AttentionInput) link(kind, namespace, name string) []model.Link {
	if in.Resolver == nil {
		return []model.Link{}
	}
	return in.Resolver.Resolve(plugin.LinkRequest{Kind: kind, Namespace: namespace, Name: name})
}

// Tracker remembers when an attention item was first seen, for items without a natural time.
type Tracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker { return &Tracker{seen: map[string]time.Time{}} }

// legacyRules are the rule groups that still run in the Manager, each attributed to the plugin it
// moves into (its G3 leaf), in the order the table has always been applied.
var legacyRules = []struct {
	plugin plugin.ID
	run    func(AttentionInput, addFn)
}{
	{"flux", attentionFlux},
	{"kubernetes", attentionCluster},
	{"kubernetes", attentionStorage},
}

// legacyAttention applies the rule table and returns the items in rule order, each stamped with
// the plugin whose rule raised it. tr gives an item without a time of its own the time it was
// first seen.
func legacyAttention(in AttentionInput, tr *Tracker) []model.AttentionItem {
	if tr == nil {
		tr = NewTracker()
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	live := map[string]bool{}
	since := func(id string, natural time.Time) time.Time {
		live[id] = true
		if !natural.IsZero() {
			return natural
		}
		if t, ok := tr.seen[id]; ok {
			return t
		}
		tr.seen[id] = in.Now
		return in.Now
	}
	items := []model.AttentionItem{}
	var from plugin.ID
	add := func(id string, sev model.Health, source, title, detail string, natural time.Time, route string, ls []model.Link) {
		if sev != model.HealthWarn && sev != model.HealthCrit {
			return
		}
		if ls == nil {
			ls = []model.Link{}
		}
		items = append(items, model.AttentionItem{
			ID: id, Severity: model.Severity(sev), Source: source, Plugin: string(from), Title: title, Detail: detail,
			Since: since(id, natural), Route: route, Links: ls,
		})
	}
	for _, r := range legacyRules {
		from = r.plugin
		r.run(in, add)
	}

	for id := range tr.seen {
		if !live[id] {
			delete(tr.seen, id)
		}
	}
	return items
}

// sortAttention orders items critical first, then newest first, then by ID.
func sortAttention(items []model.AttentionItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Severity != items[j].Severity {
			return items[i].Severity == model.SeverityCrit
		}
		if !items[i].Since.Equal(items[j].Since) {
			return items[i].Since.After(items[j].Since)
		}
		return items[i].ID < items[j].ID
	})
}

// Attention applies the rule table of every legacy source and returns the items, critical first,
// then newest first. It is what the overview shows when the legacy sources are all there is.
func Attention(in AttentionInput, tr *Tracker) []model.AttentionItem {
	return compose([]contribution{{attention: legacyAttention(in, tr)}}, legacyOverview{}).Attention
}

// OverallStatus is the worst severity of the items.
func OverallStatus(items []model.AttentionItem) model.Health {
	s := model.HealthOK
	for _, it := range items {
		if it.Severity == model.SeverityCrit {
			return model.HealthCrit
		}
		if it.Severity == model.SeverityWarn {
			s = model.HealthWarn
		}
	}
	return s
}

type addFn func(id string, sev model.Health, source, title, detail string, natural time.Time, route string, ls []model.Link)

func attentionFlux(in AttentionInput, add addFn) {
	all := func(l []model.FluxObj) {
		for _, o := range l {
			id := fmt.Sprintf("flux:%s/%s/%s", o.Kind, o.Namespace, o.Name)
			if !o.Ready && !o.Suspended {
				add(id, model.HealthWarn, "flux", fmt.Sprintf("%s %s/%s not ready", o.Kind, o.Namespace, o.Name),
					firstLine(o.Message), deref(o.LastTransition), "/gitops", in.link(string(o.Kind), o.Namespace, o.Name))
			}
			if o.Kind == model.FluxKindGitRepository && o.Namespace == "flux-system" && o.Name == "flux-system" &&
				(o.Verified == nil || !*o.Verified) {
				add("flux:source-unverified", model.HealthCrit, "flux", "flux-system source signature not verified",
					"GitRepository flux-system/flux-system SourceVerified is not True", deref(o.LastTransition), "/gitops",
					in.link(string(o.Kind), o.Namespace, o.Name))
			}
		}
	}
	all(in.Flux.Sources)
	all(in.Flux.Kustomizations)
	all(in.Flux.HelmReleases)
}

func attentionCluster(in AttentionInput, add addFn) {
	for _, n := range in.Cluster.Nodes {
		if !n.Ready {
			add("node:"+n.Name, model.HealthCrit, "kubernetes", "Node "+n.Name+" not ready", n.IP, time.Time{}, "/cluster", n.Links)
		}
	}
	for _, p := range in.Cluster.Pods {
		if p.Health != model.HealthWarn && p.Health != model.HealthCrit {
			continue
		}
		detail := p.Reason
		if p.Health == model.HealthWarn && detail == "" {
			detail = "Pending for over 5 minutes"
		}
		add("pod:"+p.Namespace+"/"+p.Name, p.Health, "kubernetes", fmt.Sprintf("Pod %s/%s %s", p.Namespace, p.Name, podProblem(p)),
			detail, p.CreatedAt, "/cluster/pods/"+p.Namespace+"/"+p.Name, p.Links)
	}
	for _, w := range in.Cluster.Workloads {
		add("workload:"+string(w.Kind)+"/"+w.Namespace+"/"+w.Name, w.Health, "kubernetes",
			fmt.Sprintf("%s %s/%s not ready", w.Kind, w.Namespace, w.Name),
			fmt.Sprintf("%d of %d ready", w.Ready, w.Desired), time.Time{}, "/cluster", w.Links)
	}
	for _, c := range in.Cluster.Certificates {
		if c.NotAfter == nil {
			continue
		}
		h := CertHealth(*c.NotAfter, in.Now)
		add("cert:"+c.Namespace+"/"+c.Name, h, "cert-manager", fmt.Sprintf("Certificate %s/%s expires soon", c.Namespace, c.Name),
			"expires "+c.NotAfter.UTC().Format("2006-01-02")+" ("+fmt.Sprintf("%.0f", math.Floor(c.NotAfter.Sub(in.Now).Hours()/24))+" days)",
			time.Time{}, "/cluster", c.Links)
	}
}

func podProblem(p model.Pod) string {
	if p.Health == model.HealthCrit {
		return "is failing"
	}
	return "is stuck pending"
}

func attentionStorage(in AttentionInput, add addFn) {
	for _, p := range in.Storage.PVCs {
		if p.UsedRatio == nil {
			continue
		}
		add("pvc:"+p.Namespace+"/"+p.Name, RatioHealth(*p.UsedRatio), "storage",
			fmt.Sprintf("PVC %s/%s is %.0f%% full", p.Namespace, p.Name, *p.UsedRatio*100), "", time.Time{}, "/storage", p.Links)
	}
	for _, b := range in.Storage.Buckets {
		if b.QuotaBytes <= 0 {
			continue
		}
		r := float64(b.UsedBytes) / float64(b.QuotaBytes)
		add("bucket:"+b.Name, RatioHealth(r), "storage", fmt.Sprintf("Bucket %s is %.0f%% of its quota", b.Name, r*100),
			"", time.Time{}, "/storage", nil)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
