package world

import (
	"strings"

	"github.com/TechDufus/dash/internal/model"
)

// Apps is the application list: six apps, all healthy. Icons are raw dashboard-icons tokens; the
// SPA resolves them against ui.iconBaseURL.
func (*World) Apps() model.AppsData {
	var d model.AppsData
	yes := true
	for _, a := range []struct{ name, desc, icon, ns string }{
		{"Grafana", "Dashboards and metrics", "grafana", "monitoring"},
		{"Headlamp", "Kubernetes UI", "headlamp", "headlamp"},
		{"Homepage", "Start page", "homepage", "homepage"},
		{"Gatus", "Service status", "gatus", "gatus"},
		{"Zot", "Container registry", "zot-registry", "registry"},
		{"Acme web", "Tenant storefront", "nginx", "acme"},
	} {
		g := model.GroupApps
		if a.ns == "monitoring" {
			g = model.GroupPlatform
		}
		if a.ns == "acme" {
			g = model.GroupTenants
		}
		d.Apps = append(d.Apps, model.App{Name: a.name, Description: a.desc, Icon: a.icon,
			Href: "https://" + strings.ToLower(strings.ReplaceAll(a.name, " ", "-")) + ".example.com", Group: g, Namespace: a.ns, Healthy: &yes})
	}
	return d
}
