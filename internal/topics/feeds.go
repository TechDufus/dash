package topics

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
)

func (m *Manager) pollStatus(ctx context.Context) {
	eps, err := m.d.Gatus.Statuses(ctx)
	m.setSource("status:gatus", err)
	if err == nil {
		m.mu.Lock()
		m.status = eps
		m.mu.Unlock()
	}
	m.mark(model.TopicStatus, model.TopicApps, model.TopicVMs)
}

func (m *Manager) buildStatus() model.StatusData {
	m.mu.RLock()
	eps := append([]model.Endpoint{}, m.status...)
	m.mu.RUnlock()
	sort.SliceStable(eps, func(i, j int) bool {
		if eps[i].Group != eps[j].Group {
			return eps[i].Group < eps[j].Group
		}
		return eps[i].Name < eps[j].Name
	})
	for i := range eps {
		if eps[i].Results == nil {
			eps[i].Results = []model.CheckResult{}
		}
	}
	return model.StatusData{Endpoints: eps}
}

var iconRE = regexp.MustCompile(`^[a-z0-9-]+\.(png|svg)$`)

const iconBase = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/"

// iconURL maps a gethomepage.dev/icon annotation to a dashboard-icons CDN URL; other values are dropped.
func iconURL(icon string) string {
	if !iconRE.MatchString(icon) {
		return ""
	}
	return iconBase + icon[strings.LastIndexByte(icon, '.')+1:] + "/" + icon
}

// routeAttachesToHome reports whether an HTTPRoute is parented on gateway/home.
func routeAttachesToHome(u *unstructured.Unstructured) bool {
	refs, _, _ := unstructured.NestedSlice(u.Object, "spec", "parentRefs")
	for _, r := range refs {
		rm, ok := r.(map[string]any)
		if ok && rm["name"] == "home" && rm["namespace"] == "gateway" {
			return true
		}
	}
	return false
}

func (m *Manager) buildApps() model.AppsData {
	m.mu.RLock()
	eps := m.status
	m.mu.RUnlock()
	health := map[string]bool{}
	for _, e := range eps {
		host := strings.ToLower(e.URL)
		if u, err := url.Parse(e.URL); err == nil && u.Hostname() != "" {
			host = strings.ToLower(u.Hostname())
		}
		if host != "" {
			health[host] = e.Healthy
		}
	}
	infos := m.namespaceInfos()

	out := model.AppsData{Apps: []model.App{}}
	for _, u := range m.d.Cluster.List(kube.GVRHTTPRoutes) {
		if u.GetNamespace() == "dash" || !routeAttachesToHome(u) {
			continue
		}
		hosts, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "hostnames")
		ann := u.GetAnnotations()
		for _, h := range hosts {
			name := ann["gethomepage.dev/name"]
			if name == "" {
				name = strings.SplitN(h, ".", 2)[0]
			}
			group := model.GroupApps
			switch {
			case infos[u.GetNamespace()].group == model.GroupTenants:
				group = model.GroupTenants
			case ann["gethomepage.dev/group"] == "Platform":
				group = model.GroupPlatform
			}
			app := model.App{
				Name: name, Description: ann["gethomepage.dev/description"], IconURL: iconURL(ann["gethomepage.dev/icon"]),
				Href: "https://" + h, Group: group, Namespace: u.GetNamespace(),
			}
			if ok, has := health[strings.ToLower(h)]; has {
				app.Healthy = new(ok)
			}
			out.Apps = append(out.Apps, app)
		}
	}
	sort.Slice(out.Apps, func(i, j int) bool {
		if out.Apps[i].Group != out.Apps[j].Group {
			return out.Apps[i].Group < out.Apps[j].Group
		}
		return strings.ToLower(out.Apps[i].Name) < strings.ToLower(out.Apps[j].Name)
	})
	return out
}
