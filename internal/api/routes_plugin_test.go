package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// routesRegistry is a test registry: two running plugins whose routes cover a path value, the
// index route, query parameters, and a stream.
func routesRegistry(t *testing.T) *pluginEnv {
	t.Helper()
	ok := func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }
	a := plugintest.New("aa")
	a.RoutesFn = func(r plugin.Routes) {
		r.Get("/vms/{vmid}", ok, plugin.RouteSpec{Summary: "One VM", Scope: "read", Query: []plugin.Param{{Name: "fields"}}})
		r.Get("/{$}", ok, plugin.RouteSpec{Summary: "The index"})
		r.Stream("/tail/{pod}", func(context.Context, *http.Request, auth.Principal, plugin.Stream) error {
			return &plugin.ActionError{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Message: "mounted"}
		}, plugin.StreamSpec{Summary: "Tail a pod", Scope: "logs", Query: []plugin.Param{{Name: "q"}, {Name: "since"}}})
	}
	b := plugintest.New("bb")
	b.RoutesFn = func(r plugin.Routes) {
		r.Get("/items", ok, plugin.RouteSpec{Summary: "Items", Scope: "read", Query: []plugin.Param{{Name: "ns"}}})
	}
	silent := plugintest.New("cc") // a plugin without routes adds none to either side
	e := newPluginEnv(t, nil, a, b, silent)
	e.eventually(t, "the plugins to run", func() bool { return e.sup.Running("aa") && e.sup.Running("bb") && e.sup.Running("cc") })
	return e
}

// paramsOf are the "in:name" of the parameters of an operation.
func paramsOf(t *testing.T, op map[string]json.RawMessage) []string {
	t.Helper()
	var p struct {
		Parameters []struct{ Name, In string } `json:"parameters"`
	}
	raw, _ := json.Marshal(op)
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, q := range p.Parameters {
		out = append(out, q.In+":"+q.Name)
	}
	return out
}

// For every manifest of the registry, the GET routes and streams the supervisor reports are the
// plugin operations of the document, and the other way round: the same method (GET), the same
// path under /api/v1/plugins/<id>/, the same parameters. A stream is a route like the others.
func TestEveryPluginRouteMatchesItsSpecEntryAndNothingElse(t *testing.T) {
	e := routesRegistry(t)
	doc, err := OpenAPI(e.sup.Manifests(), e.sup.RouteInfos)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatal(err)
	}

	// What the supervisor reports: "GET path" -> parameters.
	want := map[string][]string{}
	nRoutes, nStreams := 0, 0
	for _, m := range e.sup.Manifests() {
		base := "/api/v1/plugins/" + string(m.ID)
		for _, ri := range e.sup.RouteInfos(m.ID) {
			path := pathVarRE.ReplaceAllString(base+strings.ReplaceAll(ri.Pattern, "{$}", ""), "{$1}")
			var params []string
			for _, v := range pathVarRE.FindAllStringSubmatch(ri.Pattern, -1) {
				params = append(params, "path:"+v[1])
			}
			for _, q := range ri.Spec.Query {
				params = append(params, "query:"+q.Name)
			}
			want["GET "+path] = params
			if ri.Stream != nil {
				nStreams++
			} else {
				nRoutes++
			}
		}
	}
	if nRoutes != 3 || nStreams != 1 {
		t.Fatalf("the supervisor reports %d routes and %d streams, want 3 and 1", nRoutes, nStreams)
	}

	// What the document has under a plugin's path, whatever the method.
	got := map[string][]string{}
	for path, ops := range spec.Paths {
		for _, m := range e.sup.Manifests() {
			if !strings.HasPrefix(path, "/api/v1/plugins/"+string(m.ID)+"/") || strings.Contains(path, "/actions/") {
				continue
			}
			for method, op := range ops {
				got[strings.ToUpper(method)+" "+path] = paramsOf(t, op)
			}
		}
	}
	if !maps.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Errorf("plugin operations in the document:\n%v\nroutes the supervisor reports:\n%v", got, want)
	}

	// And every one of them is served: the routes answer, the stream reaches its handler.
	h := e.Handler()
	for _, m := range e.sup.Manifests() {
		for _, ri := range e.sup.RouteInfos(m.ID) {
			path := "/api/v1/plugins/" + string(m.ID) + samplePath(strings.ReplaceAll(ri.Pattern, "{$}", ""))
			w := req{path: path}.do(h)
			if ri.Stream != nil {
				if e := wantError(t, w, http.StatusBadRequest, model.ErrCodeInvalidInput); e.Error != "mounted" {
					t.Errorf("GET %s: %+v", path, e)
				}
			} else if w.Code != http.StatusOK {
				t.Errorf("GET %s = %d %s", path, w.Code, w.Body)
			}
		}
	}
}
