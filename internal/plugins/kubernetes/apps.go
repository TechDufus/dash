package kubernetes

import (
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// The gethomepage.dev annotations an HTTPRoute carries to describe its app.
const (
	homepagePrefix        = "gethomepage.dev/"
	annotationName        = homepagePrefix + "name"
	annotationGroup       = homepagePrefix + "group"
	annotationDescription = homepagePrefix + "description"
	annotationIcon        = homepagePrefix + "icon"
	homepageGroupPlatform = "Platform"
)

// The routes that make an app: parented on the gateway home of the namespace gateway, anywhere
// but in dash's own namespace.
const (
	appsGatewayName      = "home"
	appsGatewayNamespace = "gateway"
	appsSkippedNamespace = "dash"
	appsSourceHTTPRoute  = "httproute"
)

const iconBase = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/"

var iconRE = regexp.MustCompile(`^[a-z0-9-]+\.(png|svg)$`)

// iconURL maps a gethomepage.dev/icon annotation to a dashboard-icons CDN URL; other values are dropped.
func iconURL(icon string) string {
	if !iconRE.MatchString(icon) {
		return ""
	}
	return iconBase + icon[strings.LastIndexByte(icon, '.')+1:] + "/" + icon
}

// routeAttachesToHome reports whether an HTTPRoute has the gateway named home in the namespace
// gateway among its parents.
func routeAttachesToHome(u *unstructured.Unstructured) bool {
	refs, _, _ := unstructured.NestedSlice(u.Object, "spec", "parentRefs")
	for _, r := range refs {
		rm, ok := r.(map[string]any)
		if ok && rm["name"] == appsGatewayName && rm["namespace"] == appsGatewayNamespace {
			return true
		}
	}
	return false
}

// appsInput is everything buildApps reads.
type appsInput struct {
	c     *kube.Cluster
	cfg   pluginconf.Kubernetes
	ep    svc.EndpointHealth
	infos map[string]nsInfo
}

// buildApps is the kubernetes.apps payload: one app per hostname of every HTTPRoute parented on
// the home gateway, outside the dash namespace, sorted by group and name.
func buildApps(in appsInput) model.AppsData {
	out := model.AppsData{Apps: []model.App{}}
	for _, u := range in.c.List(kube.GVRHTTPRoutes) {
		if u.GetNamespace() == appsSkippedNamespace || !routeAttachesToHome(u) {
			continue
		}
		hosts, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "hostnames")
		ann := u.GetAnnotations()
		for _, h := range hosts {
			name := ann[annotationName]
			if name == "" {
				name = strings.SplitN(h, ".", 2)[0]
			}
			group := model.GroupApps
			switch {
			case in.infos[u.GetNamespace()].group == model.GroupTenants:
				group = model.GroupTenants
			case ann[annotationGroup] == homepageGroupPlatform:
				group = model.GroupPlatform
			}
			app := model.App{
				Name: name, Description: ann[annotationDescription], IconURL: iconURL(ann[annotationIcon]),
				Ref:  plugin.Ref(ID, "App", u.GetNamespace(), appsSourceHTTPRoute, u.GetName(), h),
				Href: "https://" + h, Group: group, Namespace: u.GetNamespace(),
			}
			if state, known := in.ep.ByHost(h); known {
				app.Healthy = new(state == model.HealthOK)
			}
			out.Apps = append(out.Apps, app)
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
