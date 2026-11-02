package kubernetes

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// appsParent is one entry of an HTTPRoute's spec.parentRefs.
type appsParent struct{ name, namespace string }

// appsHomeGateway is the parent that makes a route an app.
var appsHomeGateway = appsParent{"home", "gateway"}

// appsRoute is an HTTPRoute with the given parents, hostnames and annotations.
func appsRoute(ns, name string, parents []appsParent, hosts []string, ann map[string]string) *unstructured.Unstructured {
	u := obj(kube.GVRHTTPRoutes, ns, name)
	refs := make([]any, 0, len(parents))
	for _, p := range parents {
		ref := map[string]any{"name": p.name}
		if p.namespace != "" {
			ref["namespace"] = p.namespace
		}
		refs = append(refs, ref)
	}
	hostnames := make([]any, 0, len(hosts))
	for _, h := range hosts {
		hostnames = append(hostnames, h)
	}
	_ = unstructured.SetNestedSlice(u.Object, refs, "spec", "parentRefs")
	_ = unstructured.SetNestedSlice(u.Object, hostnames, "spec", "hostnames")
	if ann != nil {
		u.SetAnnotations(ann)
	}
	return u
}

// appsHomelab is the configuration that reproduces the list the code had before the settings
// existed: one gateway, and dash's own namespace left out.
func appsHomelab() pluginconf.Kubernetes {
	cfg := pluginconf.DefaultKubernetes()
	cfg.Apps.Gateways = []pluginconf.AppsGateway{{Namespace: "gateway", Name: "home"}}
	cfg.Apps.ExcludeNamespaces = []string{"dash"}
	return cfg
}

func appsBuild(t *testing.T, ep *fakeGatus, infos map[string]nsInfo, objs ...k8sruntime.Object) model.AppsData {
	t.Helper()
	return appsBuildWith(t, appsHomelab(), nil, ep, infos, objs...)
}

// appsBuildWith builds with a configuration; crds are the served resources (nil: the default four).
func appsBuildWith(t *testing.T, cfg pluginconf.Kubernetes, crds []schema.GroupVersionResource, ep *fakeGatus, infos map[string]nsInfo, objs ...k8sruntime.Object) model.AppsData {
	t.Helper()
	c := testCluster(t, crds, objs...)
	return buildApps(appsInput{c: c, cfg: cfg, ep: ep, infos: infos})
}

func appByHref(t *testing.T, d model.AppsData, href string) model.App {
	t.Helper()
	for _, a := range d.Apps {
		if a.Href == href {
			return a
		}
	}
	t.Fatalf("no app with href %s in %+v", href, d.Apps)
	return model.App{}
}

func TestAppsReproduceTheHomelabList(t *testing.T) {
	home := []appsParent{appsHomeGateway}
	ep := &fakeGatus{}
	ep.set("grafana.example.com", model.HealthOK)
	ep.set("plex.example.com", model.HealthCrit)
	ep.set("old.example.com", model.HealthUnknown)
	infos := map[string]nsInfo{
		"acme":       {group: model.GroupTenants},
		"media":      {group: model.GroupApps},
		"monitoring": {group: model.GroupPlatform},
	}
	d := appsBuild(t, ep, infos,
		appsRoute("monitoring", "grafana", home, []string{"grafana.example.com"}, map[string]string{
			"gethomepage.dev/name": "Grafana", "gethomepage.dev/group": "Platform",
			"gethomepage.dev/description": "Dashboards", "gethomepage.dev/icon": "grafana.png",
		}),
		// Two hostnames, no annotations: the name is the first label of each host.
		appsRoute("media", "plex", home, []string{"plex.example.com", "plex-alt.example.com"}, nil),
		// A route with several parents counts when one of them is the home gateway.
		appsRoute("media", "old", []appsParent{{"other", "gateway"}, appsHomeGateway}, []string{"old.example.com"}, map[string]string{
			"gethomepage.dev/name": "Archive", "gethomepage.dev/icon": "mdi-monitor-dashboard",
		}),
		// A tenant namespace wins over the Platform annotation.
		appsRoute("acme", "portal", home, []string{"portal.acme.example.com"}, map[string]string{"gethomepage.dev/group": "Platform", "gethomepage.dev/icon": "acme.svg"}),
		// Not apps: another gateway, the right name in another namespace, a parent without a
		// namespace, no parent at all, no hostnames, and dash's own namespace.
		appsRoute("media", "other-gw", []appsParent{{"public", "gateway"}}, []string{"other.example.com"}, nil),
		appsRoute("media", "wrong-ns", []appsParent{{"home", "media"}}, []string{"wrong.example.com"}, nil),
		appsRoute("media", "no-ns", []appsParent{{"home", ""}}, []string{"nons.example.com"}, nil),
		appsRoute("media", "orphan", nil, []string{"orphan.example.com"}, nil),
		appsRoute("media", "hostless", home, nil, nil),
		appsRoute("dash", "dash", home, []string{"dash.example.com"}, nil),
	)

	type row struct {
		name, desc, icon, href string
		group                  model.Group
		ns                     string
		healthy                *bool
	}
	want := []row{
		// Apps, then Platform, then Tenants; by lower-cased name inside a group.
		{"Archive", "", "", "https://old.example.com", model.GroupApps, "media", new(false)},
		{"plex", "", "", "https://plex.example.com", model.GroupApps, "media", new(false)},
		{"plex-alt", "", "", "https://plex-alt.example.com", model.GroupApps, "media", nil},
		{"Grafana", "Dashboards", "grafana.png", "https://grafana.example.com", model.GroupPlatform, "monitoring", new(true)},
		{"portal", "", "acme.svg", "https://portal.acme.example.com", model.GroupTenants, "acme", nil},
	}
	if len(d.Apps) != len(want) {
		t.Fatalf("apps = %+v, want %d", d.Apps, len(want))
	}
	for i, w := range want {
		a := d.Apps[i]
		got := row{a.Name, a.Description, a.Icon, a.Href, a.Group, a.Namespace, a.Healthy}
		if got.name != w.name || got.desc != w.desc || got.icon != w.icon || got.href != w.href || got.group != w.group || got.ns != w.ns || !appsSameBool(got.healthy, w.healthy) {
			t.Errorf("app %d = %+v (healthy %v), want %+v (healthy %v)", i, got, appsHealthValue(got.healthy), w, appsHealthValue(w.healthy))
		}
	}
	plugintest.AssertRefs(t, nil, ID, d, model.App{})
	if got := appByHref(t, d, "https://plex-alt.example.com").Ref; got != "kubernetes:App/media/httproute/plex/plex-alt.example.com" {
		t.Errorf("ref = %q", got)
	}
}

func appsSameBool(a, b *bool) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func appsHealthValue(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// A host the endpoint source does not know has no verdict; one it knows as anything but ok is
// unhealthy.
func TestAppsHealthFollowsTheEndpointSource(t *testing.T) {
	ep := &fakeGatus{}
	ep.set("up.example.com", model.HealthOK)
	ep.set("warn.example.com", model.HealthWarn)
	ep.set("down.example.com", model.HealthCrit)
	hosts := []string{"up.example.com", "warn.example.com", "down.example.com", "unlisted.example.com"}
	d := appsBuild(t, ep, nil, appsRoute("media", "r", []appsParent{appsHomeGateway}, hosts, nil))
	for host, want := range map[string]*bool{
		"up.example.com": new(true), "warn.example.com": new(false), "down.example.com": new(false), "unlisted.example.com": nil,
	} {
		if got := appByHref(t, d, "https://"+host).Healthy; !appsSameBool(got, want) {
			t.Errorf("%s: healthy = %v, want %v", host, appsHealthValue(got), appsHealthValue(want))
		}
	}
	// The unknown verdict stays out of the JSON.
	raw, err := json.Marshal(appByHref(t, d, "https://unlisted.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["healthy"]; has {
		t.Errorf("json = %s, want no healthy key", raw)
	}
}

func TestAppsGroupRules(t *testing.T) {
	home := []appsParent{appsHomeGateway}
	infos := map[string]nsInfo{"acme": {group: model.GroupTenants}, "kube-system": {group: model.GroupPlatform}}
	tests := []struct {
		name string
		ns   string
		ann  map[string]string
		want model.Group
	}{
		{"no annotation", "media", nil, model.GroupApps},
		{"platform annotation", "media", map[string]string{"gethomepage.dev/group": "Platform"}, model.GroupPlatform},
		{"another group name", "media", map[string]string{"gethomepage.dev/group": "Media"}, model.GroupApps},
		{"lower case platform is not Platform", "media", map[string]string{"gethomepage.dev/group": "platform"}, model.GroupApps},
		{"tenant namespace", "acme", nil, model.GroupTenants},
		{"tenant beats the annotation", "acme", map[string]string{"gethomepage.dev/group": "Platform"}, model.GroupTenants},
		{"a platform namespace alone is not Platform", "kube-system", nil, model.GroupApps},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := appsBuild(t, &fakeGatus{}, infos, appsRoute(tc.ns, "r", home, []string{"x.example.com"}, tc.ann))
			if len(d.Apps) != 1 || d.Apps[0].Group != tc.want {
				t.Fatalf("apps = %+v, want one in %s", d.Apps, tc.want)
			}
		})
	}
}

// The icon is the annotation as written (trimmed, bounded); the prefixed tokens of the homepage
// dashboards, which name no dashboard icon, become empty. The web UI resolves the token.
func TestIconToken(t *testing.T) {
	long := strings.Repeat("a", 257)
	tests := map[string]string{
		"grafana.png":                   "grafana.png",
		"grafana.svg":                   "grafana.svg",
		"grafana":                       "grafana",
		"a.b.png":                       "a.b.png",
		"home-assistant.png":            "home-assistant.png",
		"  zot.svg \n":                  "zot.svg",
		"https://cdn.example.com/a.png": "https://cdn.example.com/a.png",
		"/static/icons/a.png":           "/static/icons/a.png",
		"mdi-monitor-dashboard":         "",
		"si-github":                     "",
		"sh-grafana":                    "",
		"mdi-":                          "",
		"mdix":                          "mdix",
		"simple":                        "simple",
		strings.Repeat("a", 256):        strings.Repeat("a", 256),
		long:                            "",
		"":                              "",
		"   ":                           "",
	}
	for in, want := range tests {
		if got := iconToken(in); got != want {
			t.Errorf("iconToken(%q) = %q, want %q", in, got, want)
		}
	}
}

// The gateway rule: none listed accepts every route; a listed one must be named by a parent
// reference, by name and namespace, a reference without a namespace meaning the route's own.
func TestAppsGatewayFilter(t *testing.T) {
	gw := func(ns, name string) pluginconf.AppsGateway { return pluginconf.AppsGateway{Namespace: ns, Name: name} }
	routes := []k8sruntime.Object{
		appsRoute("media", "explicit", []appsParent{{"home", "gateway"}}, []string{"explicit.example.com"}, nil),
		appsRoute("gateway", "implicit", []appsParent{{"home", ""}}, []string{"implicit.example.com"}, nil),
		appsRoute("media", "implicit-elsewhere", []appsParent{{"home", ""}}, []string{"elsewhere.example.com"}, nil),
		appsRoute("media", "public", []appsParent{{"public", "gateway"}}, []string{"public.example.com"}, nil),
		appsRoute("media", "both", []appsParent{{"public", "gateway"}, {"home", "gateway"}}, []string{"both.example.com"}, nil),
		appsRoute("media", "orphan", nil, []string{"orphan.example.com"}, nil),
	}
	for _, tc := range []struct {
		name     string
		gateways []pluginconf.AppsGateway
		want     []string
	}{
		{"none listed accepts every route", nil, []string{"both", "elsewhere", "explicit", "implicit", "orphan", "public"}},
		{"one gateway", []pluginconf.AppsGateway{gw("gateway", "home")}, []string{"both", "explicit", "implicit"}},
		{"several gateways", []pluginconf.AppsGateway{gw("gateway", "home"), gw("gateway", "public")}, []string{"both", "explicit", "implicit", "public"}},
		{"the namespace must match", []pluginconf.AppsGateway{gw("media", "home")}, []string{"elsewhere"}},
		{"the name must match", []pluginconf.AppsGateway{gw("gateway", "other")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := pluginconf.DefaultKubernetes()
			cfg.Apps.Gateways = tc.gateways
			d := appsBuildWith(t, cfg, nil, &fakeGatus{}, nil, routes...)
			var got []string
			for _, a := range d.Apps {
				got = append(got, strings.TrimSuffix(a.Href[len("https://"):], ".example.com"))
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("apps = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAppsNamespaceExclusion(t *testing.T) {
	routes := []k8sruntime.Object{
		appsRoute("dash", "dash", nil, []string{"dash.example.com"}, nil),
		appsRoute("media", "plex", nil, []string{"plex.example.com"}, nil),
		appsRoute("kube-system", "hubble", nil, []string{"hubble.example.com"}, nil),
	}
	for _, tc := range []struct {
		exclude []string
		want    int
	}{{nil, 3}, {[]string{"dash"}, 2}, {[]string{"dash", "kube-system"}, 1}, {[]string{"absent"}, 3}} {
		cfg := pluginconf.DefaultKubernetes()
		cfg.Apps.ExcludeNamespaces = tc.exclude
		if got := len(appsBuildWith(t, cfg, nil, &fakeGatus{}, nil, routes...).Apps); got != tc.want {
			t.Errorf("exclude %v: %d apps, want %d", tc.exclude, got, tc.want)
		}
	}
}

// The prefix names the annotation keys; a trailing slash is the same prefix.
func TestAppsAnnotationPrefix(t *testing.T) {
	ann := map[string]string{
		"gethomepage.dev/name": "Home", "gethomepage.dev/icon": "home.png",
		"dash.example.com/name": "Mine", "dash.example.com/description": "Custom", "dash.example.com/icon": "mine.svg", "dash.example.com/group": "Platform",
	}
	route := appsRoute("media", "r", nil, []string{"x.example.com"}, ann)
	for _, tc := range []struct {
		prefix, name, desc, icon string
		group                    model.Group
	}{
		{"", "Home", "", "home.png", model.GroupApps},
		{"gethomepage.dev", "Home", "", "home.png", model.GroupApps},
		{"gethomepage.dev/", "Home", "", "home.png", model.GroupApps},
		{"dash.example.com", "Mine", "Custom", "mine.svg", model.GroupPlatform},
		{"dash.example.com/", "Mine", "Custom", "mine.svg", model.GroupPlatform},
		{"other.example.com", "x", "", "", model.GroupApps},
	} {
		cfg := pluginconf.DefaultKubernetes()
		cfg.Apps.AnnotationPrefix = tc.prefix
		d := appsBuildWith(t, cfg, nil, &fakeGatus{}, nil, route)
		if len(d.Apps) != 1 {
			t.Fatalf("prefix %q: %+v", tc.prefix, d.Apps)
		}
		a := d.Apps[0]
		if a.Name != tc.name || a.Description != tc.desc || a.Icon != tc.icon || a.Group != tc.group {
			t.Errorf("prefix %q: app = %+v", tc.prefix, a)
		}
	}
}

func appsIngress(ns, name string, hosts []string, ann map[string]string) *unstructured.Unstructured {
	u := obj(kube.GVRIngresses, ns, name)
	rules := []any{}
	for _, h := range hosts {
		rules = append(rules, map[string]any{"host": h})
	}
	rules = append(rules, map[string]any{}) // a rule without a host is no app
	_ = unstructured.SetNestedSlice(u.Object, rules, "spec", "rules")
	if ann != nil {
		u.SetAnnotations(ann)
	}
	return u
}

// With apps.ingress an Ingress rule host is an app too, grouped, annotated, excluded and
// referenced like a route's; without it Ingresses are not read.
func TestAppsIngressOnAndOff(t *testing.T) {
	objs := []k8sruntime.Object{
		appsRoute("media", "plex", nil, []string{"plex.example.com"}, nil),
		appsIngress("tools", "wiki", []string{"wiki.example.com", "docs.example.com"}, map[string]string{"gethomepage.dev/name": "Wiki", "gethomepage.dev/icon": "wiki.png"}),
		appsIngress("dash", "dash", []string{"dash.example.com"}, nil),
		appsIngress("media", "plex", []string{"plex.example.com"}, nil), // same name and host as the route
	}
	crds := []schema.GroupVersionResource{kube.GVRHTTPRoutes, kube.GVRIngresses}
	cfg := pluginconf.DefaultKubernetes()
	cfg.Apps.ExcludeNamespaces = []string{"dash"}

	off := appsBuildWith(t, cfg, crds, &fakeGatus{}, nil, objs...)
	if len(off.Apps) != 1 || off.Apps[0].Href != "https://plex.example.com" {
		t.Fatalf("ingress off: %+v", off.Apps)
	}
	cfg.Apps.Ingress = true
	on := appsBuildWith(t, cfg, crds, &fakeGatus{}, nil, objs...)
	if len(on.Apps) != 4 {
		t.Fatalf("ingress on: %+v", on.Apps)
	}
	plugintest.AssertRefs(t, nil, ID, on, model.App{})
	wiki := appByHref(t, on, "https://wiki.example.com")
	if wiki.Ref != "kubernetes:App/tools/ingress/wiki/wiki.example.com" || wiki.Name != "Wiki" || wiki.Icon != "wiki.png" || wiki.Namespace != "tools" {
		t.Errorf("wiki = %+v", wiki)
	}
	if docs := appByHref(t, on, "https://docs.example.com"); docs.Name != "Wiki" {
		t.Errorf("docs = %+v", docs)
	}
	var refs []string
	for _, a := range on.Apps {
		if a.Href == "https://plex.example.com" {
			refs = append(refs, a.Ref)
		}
	}
	slices.Sort(refs)
	if !slices.Equal(refs, []string{"kubernetes:App/media/httproute/plex/plex.example.com", "kubernetes:App/media/ingress/plex/plex.example.com"}) {
		t.Errorf("the route and the ingress of one name: refs = %v", refs)
	}
}

func TestAppsAreSortedByGroupThenLowerCasedName(t *testing.T) {
	home := []appsParent{appsHomeGateway}
	named := func(ns, name, display, group string) *unstructured.Unstructured {
		ann := map[string]string{"gethomepage.dev/name": display}
		if group != "" {
			ann["gethomepage.dev/group"] = group
		}
		return appsRoute(ns, name, home, []string{name + ".example.com"}, ann)
	}
	d := appsBuild(t, &fakeGatus{}, map[string]nsInfo{"acme": {group: model.GroupTenants}},
		named("media", "r1", "zeta", ""),
		named("media", "r2", "Beta", ""),
		named("media", "r3", "alpha", ""),
		named("media", "r4", "Gamma", "Platform"),
		named("media", "r5", "delta", "Platform"),
		named("acme", "r6", "Aardvark", ""),
	)
	var got []string
	for _, a := range d.Apps {
		got = append(got, string(a.Group)+":"+a.Name)
	}
	want := []string{"Apps:alpha", "Apps:Beta", "Apps:zeta", "Platform:delta", "Platform:Gamma", "Tenants:Aardvark"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// Equal names in one group keep a fixed order, so two builds of one cluster are equal.
func TestAppsOrderIsStableForEqualNames(t *testing.T) {
	home := []appsParent{appsHomeGateway}
	ann := map[string]string{"gethomepage.dev/name": "Same"}
	var objs []k8sruntime.Object
	for _, ns := range []string{"d", "b", "a", "c"} {
		objs = append(objs, appsRoute(ns, "r", home, []string{"h.example.com"}, ann))
	}
	c := testCluster(t, nil, objs...)
	first := buildApps(appsInput{c: c, ep: &fakeGatus{}})
	for range 20 {
		if again := buildApps(appsInput{c: c, ep: &fakeGatus{}}); !slices.Equal(again.Apps, first.Apps) {
			t.Fatalf("order changed between builds:\n%+v\n%+v", first.Apps, again.Apps)
		}
	}
	var ns []string
	for _, a := range first.Apps {
		ns = append(ns, a.Namespace)
	}
	if !slices.Equal(ns, []string{"a", "b", "c", "d"}) {
		t.Errorf("namespaces in order = %v", ns)
	}
}

func TestAppsOfAnEmptyClusterIsAnEmptyList(t *testing.T) {
	d := appsBuild(t, &fakeGatus{}, nil)
	if d.Apps == nil || len(d.Apps) != 0 {
		t.Fatalf("apps = %#v, want an empty non-nil slice", d.Apps)
	}
	raw, err := json.Marshal(d)
	if err != nil || string(raw) != `{"apps":[]}` {
		t.Errorf("json = %s, %v", raw, err)
	}
	// Routes that are not apps leave it empty too.
	d = appsBuild(t, &fakeGatus{}, nil, appsRoute("dash", "dash", []appsParent{appsHomeGateway}, []string{"dash.example.com"}, nil))
	if d.Apps == nil || len(d.Apps) != 0 {
		t.Fatalf("apps = %#v, want an empty non-nil slice", d.Apps)
	}
}

// The ref names namespace, source, route and host: several hostnames of one route, and routes of
// one name in two namespaces (even for the same host), never collide.
func TestRefsAreUniqueUnderCollidingNames(t *testing.T) {
	home := []appsParent{appsHomeGateway}
	d := appsBuild(t, &fakeGatus{}, nil,
		appsRoute("media", "web", home, []string{"a.example.com", "b.example.com", "c.example.com"}, nil),
		appsRoute("media", "web-twin", home, []string{"a.example.com"}, nil),
		appsRoute("tools", "web", home, []string{"a.example.com", "b.example.com"}, map[string]string{"gethomepage.dev/name": "Same"}),
	)
	if len(d.Apps) != 6 {
		t.Fatalf("apps = %+v, want 6", d.Apps)
	}
	plugintest.AssertRefs(t, nil, ID, d, model.App{})
	refs := map[string]bool{}
	for _, a := range d.Apps {
		refs[a.Ref] = true
	}
	for _, want := range []string{
		"kubernetes:App/media/httproute/web/a.example.com",
		"kubernetes:App/media/httproute/web/c.example.com",
		"kubernetes:App/media/httproute/web-twin/a.example.com",
		"kubernetes:App/tools/httproute/web/a.example.com",
		"kubernetes:App/tools/httproute/web/b.example.com",
	} {
		if !refs[want] {
			t.Errorf("missing ref %s in %v", want, refs)
		}
	}
}
