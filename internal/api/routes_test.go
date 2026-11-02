package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// samplePath is a request path that matches the ServeMux path: every wildcard filled with "x".
func samplePath(path string) string {
	out, rest := "", path
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			return out + rest
		}
		j := strings.IndexByte(rest, '}')
		out, rest = out+rest[:i]+"x", rest[j+1:]
	}
}

// opKey is "METHOD path" of an OpenAPI operation.
func specOperations(t *testing.T, doc []byte) map[string]bool {
	t.Helper()
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			out[strings.ToUpper(method)+" "+path] = true
		}
	}
	return out
}

func TestEveryRouteAnswersAtItsOwnPattern(t *testing.T) {
	s := newServer(nil)
	mux := s.mux()
	seen := map[string]bool{}
	for _, r := range s.routeTable() {
		method := r.Method
		if method == "" {
			method = http.MethodGet
		}
		req := httptest.NewRequest(method, samplePath(r.Path), nil)
		if _, pattern := mux.Handler(req); pattern != r.pattern() {
			t.Errorf("%s %s is served by %q, want %q", method, req.URL.Path, pattern, r.pattern())
		}
		if seen[r.pattern()] {
			t.Errorf("%s is in the table twice", r.pattern())
		}
		seen[r.pattern()] = true
		if r.Handler == nil {
			t.Errorf("%s has no handler", r.pattern())
		}
	}
}

// The document and the table agree: every route of the contract is a documented operation and
// every operation is a route.
func TestEveryRouteMatchesItsSpecEntryAndNothingElse(t *testing.T) {
	doc, err := OpenAPI(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := specOperations(t, doc)

	want := map[string]bool{}
	ids := map[string]bool{}
	for _, r := range newServer(nil).routeTable() {
		if r.Plugin || r.Infra {
			continue
		}
		want[r.Method+" "+r.Path] = true
		if r.ID == "" || ids[r.ID] || r.Summary == "" || r.Tag == "" || r.Role == "" {
			t.Errorf("route %s %s is not fully described (id %q, summary %q, tag %q, role %q) or repeats an id", r.Method, r.Path, r.ID, r.Summary, r.Tag, r.Role)
		}
		ids[r.ID] = true
		if !strings.HasPrefix(r.Path, "/api/v1/") {
			t.Errorf("%s %s is documented but not under /api/v1", r.Method, r.Path)
		}
	}
	if !maps.Equal(spec, want) {
		t.Errorf("operations in the document:\n%v\nroutes of the contract:\n%v", keys(spec), keys(want))
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Every route of the table is registered with the route label and the panic recovery around it:
// with each handler replaced by one that panics, every route still answers 500 internal and is
// counted under its own pattern. The mux is built from the table alone (muxOf(routeTable())),
// so this holds for every route there is.
func TestEveryTableRouteRunsInsideTheRouteLabelAndRecovery(t *testing.T) {
	s := newServer(nil)
	s.d.Metrics = obs.New("test", RoutePatterns(), nil)
	table := s.routeTable()
	for i := range table {
		table[i].Handler = func(http.ResponseWriter, *http.Request) { panic("kaboom") }
	}
	// The guard answers a request without a principal before the handler, so the requests are
	// admin's: this test is about what runs around the handler, not the guard.
	mux := s.d.Metrics.Instrument(s.muxOf(table))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, asPrincipal(r, auth.Principal{Kind: auth.KindProxy, User: "ada", Role: auth.RoleAdmin}))
	})
	for _, r := range table {
		method := r.Method
		if method == "" {
			method = http.MethodGet
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, samplePath(r.Path), nil))
		if e := apiError(t, w); w.Code != http.StatusInternalServerError || e.Code != model.ErrCodeInternal {
			t.Errorf("%s: %d %s, want 500 internal", r.pattern(), w.Code, w.Body)
		}
	}
	ss := scrapeMetrics(t, s)
	for _, r := range table {
		route := r.pattern()
		if route == "/" {
			route = obs.RouteStatic
		}
		if v, ok := seriesValue(t, ss, "dash_http_requests_total", map[string]string{"route": route, "code": "500"}); !ok || v != 1 {
			t.Errorf("%s counted %v (present %v), want one 500 under its own pattern", route, v, ok)
		}
	}
}

// The core paths that moved to /api/v1 or into a plugin are gone, with no alias. The silence
// routes moved into the alertmanager plugin's actions (P3.3), the logs into the victorialogs
// plugin's routes (P3.5), the VM detail into the proxmox plugin's (P3.6).
func TestMovedCorePathsAre404WithCode(t *testing.T) {
	h := newServer(nil).Handler()
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/me"}, {"GET", "/api/stream"}, {"GET", "/api/series?name=node.cpu&range=1h&node=cp1"},
		{"GET", "/api/topics/cluster"}, {"GET", "/api/plugins"}, {"GET", "/api/config"},
		{"GET", "/api/logs?ns=media&pod=web"}, {"GET", "/api/logs/tail?ns=media&pod=web"},
		{"POST", "/api/silences"}, {"DELETE", "/api/silences/00000000-0000-4000-8000-000000000001"},
		{"GET", "/api/vms/100"},            // the proxmox plugin's route (P3.6): /api/v1/plugins/proxmox/vms/{vmid}
		{"GET", "/api/pods/media/plex-0"},  // the kubernetes plugin's route (P3.8): /api/v1/plugins/kubernetes/pods/{ns}/{name}
		{"POST", "/api/actions/restart"},   // the kubernetes plugin's action (P3.8): /api/v1/plugins/kubernetes/actions/restart
		{"POST", "/api/actions/reconcile"}, // the flux plugin's action (P3.11): /api/v1/plugins/flux/actions/reconcile
	} {
		path := c.path
		w := req{method: c.method, path: path}.do(h)
		e := wantError(t, w, http.StatusNotFound, model.ErrCodeNotFound)
		if e.RequestID == "" || e.RequestID != w.Header().Get("X-Request-Id") {
			t.Errorf("%s %s: requestId %q, header %q", c.method, path, e.RequestID, w.Header().Get("X-Request-Id"))
		}
	}
}

// Every API route refuses an unauthenticated request with the envelope, a code and the id.
func TestEveryTableRouteRefusesTheUnauthenticatedWithCodeAndRequestID(t *testing.T) {
	s := newServer(nil)
	h := s.Handler()
	for _, r := range s.routeTable() {
		if r.Path == "/" { // the SPA is public
			continue
		}
		method := r.Method
		if method == "" {
			method = http.MethodGet
		}
		w := req{method: method, path: samplePath(r.Path), header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
		e := wantError(t, w, http.StatusUnauthorized, model.ErrCodeUnauthenticated)
		if e.RequestID == "" || e.RequestID != w.Header().Get("X-Request-Id") {
			t.Errorf("%s: requestId %q, header %q", r.pattern(), e.RequestID, w.Header().Get("X-Request-Id"))
		}
	}
}
