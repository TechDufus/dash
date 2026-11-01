package topics

import (
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
)

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
	ep := m.endpoints()
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
			if state, known := ep.ByHost(h); known {
				app.Healthy = new(state == model.HealthOK)
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
