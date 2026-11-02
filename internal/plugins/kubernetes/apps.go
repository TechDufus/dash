package kubernetes

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// The annotations an object carries to describe its app, under plugins.kubernetes.apps.
// annotationPrefix: <prefix>/name, /group, /description and /icon.
const (
	annotationName        = "name"
	annotationGroup       = "group"
	annotationDescription = "description"
	annotationIcon        = "icon"
	homepageGroupPlatform = "Platform"
)

// The sources an app can come from, as its ref names them.
const (
	appsSourceHTTPRoute = "httproute"
	appsSourceIngress   = "ingress"
)

// maxIconToken is the longest icon annotation dash passes on, in bytes.
const maxIconToken = 256

// iconNotAName matches the icon tokens of the homepage dashboards that are not dashboard-icons
// names (mdi-monitor-dashboard, si-github, sh-grafana): asking a CDN for one is a 404.
var iconNotAName = regexp.MustCompile(`^(mdi|si|sh)-`)

// iconToken is the raw icon token of an annotation value: trimmed, empty when it is longer than
// maxIconToken or is one of the prefixed tokens that name no dashboard icon. The web UI resolves
// the token against ui.iconBaseURL; the server never builds an icon URL.
func iconToken(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > maxIconToken || iconNotAName.MatchString(v) {
		return ""
	}
	return v
}

// appsInput is everything buildApps reads.
type appsInput struct {
	c     *kube.Cluster
	cfg   pluginconf.Kubernetes
	ep    svc.EndpointHealth
	infos map[string]nsInfo
}

// gatewayAccepts reports whether a route is an app by the gateway rule: no gateways listed
// accepts every route; otherwise one of the route's parent references must name a listed gateway,
// by name and by namespace, where a reference without a namespace means the route's own (the
// Gateway API default).
func gatewayAccepts(gateways []pluginconf.AppsGateway, u *unstructured.Unstructured) bool {
	if len(gateways) == 0 {
		return true
	}
	refs, _, _ := unstructured.NestedSlice(u.Object, "spec", "parentRefs")
	for _, r := range refs {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := rm["name"].(string)
		ns, _ := rm["namespace"].(string)
		if ns == "" {
			ns = u.GetNamespace()
		}
		for _, g := range gateways {
			if g.Name == name && g.Namespace == ns {
				return true
			}
		}
	}
	return false
}

// buildApps is the kubernetes.apps payload: one app per hostname of every accepted HTTPRoute and,
// with apps.ingress, of every Ingress rule, outside the excluded namespaces, sorted by group and
// name.
func buildApps(in appsInput) model.AppsData {
	a := in.cfg.Apps
	prefix := a.Prefix() + "/"
	out := model.AppsData{Apps: []model.App{}}
	add := func(u *unstructured.Unstructured, source string, hosts []string) {
		ann := u.GetAnnotations()
		seen := map[string]bool{}
		for _, h := range hosts {
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			name := ann[prefix+annotationName]
			if name == "" {
				name = strings.SplitN(h, ".", 2)[0]
			}
			group := model.GroupApps
			switch {
			case in.infos[u.GetNamespace()].group == model.GroupTenants:
				group = model.GroupTenants
			case ann[prefix+annotationGroup] == homepageGroupPlatform:
				group = model.GroupPlatform
			}
			app := model.App{
				Name: name, Description: ann[prefix+annotationDescription], Icon: iconToken(ann[prefix+annotationIcon]),
				Ref:  plugin.Ref(ID, "App", u.GetNamespace(), source, u.GetName(), h),
				Href: "https://" + h, Group: group, Namespace: u.GetNamespace(),
			}
			if state, known := in.ep.ByHost(h); known {
				app.Healthy = new(state == model.HealthOK)
			}
			out.Apps = append(out.Apps, app)
		}
	}
	for _, u := range in.c.List(kube.GVRHTTPRoutes) {
		if slices.Contains(a.ExcludeNamespaces, u.GetNamespace()) || !gatewayAccepts(a.Gateways, u) {
			continue
		}
		hosts, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "hostnames")
		add(u, appsSourceHTTPRoute, hosts)
	}
	if a.Ingress {
		for _, u := range in.c.List(kube.GVRIngresses) {
			if slices.Contains(a.ExcludeNamespaces, u.GetNamespace()) {
				continue
			}
			rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
			var hosts []string
			for _, r := range rules {
				if rm, ok := r.(map[string]any); ok {
					if h, _ := rm["host"].(string); h != "" {
						hosts = append(hosts, h)
					}
				}
			}
			add(u, appsSourceIngress, hosts)
		}
	}
	// Apps of one group and name keep a fixed order (the ref), so a rebuild never reshuffles them.
	sort.Slice(out.Apps, func(i, j int) bool {
		a, b := out.Apps[i], out.Apps[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		return a.Ref < b.Ref
	})
	return out
}
