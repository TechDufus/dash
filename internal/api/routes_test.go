package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// legacyRoutes is the unversioned integration routes the SPA still calls, as ServeMux patterns.
// It is pinned here on purpose: adding a route outside /api/v1 fails the test until it is added
// to this list (a decision), and each plugin migration deletes its entries together with the
// route. P3.13 deletes the Legacy flag and this list.
var legacyRoutes = []string{
	"GET /api/config",
	"GET /api/vms/{vmid}",
	"GET /api/pods/{ns}/{name}",
	"GET /api/logs",
	"GET /api/logs/tail",
	"POST /api/actions/restart",
	"POST /api/actions/reconcile",
	"POST /api/silences",
	"DELETE /api/silences/{id}",
}

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
		if r.Legacy || r.Plugin || r.Infra {
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

func TestLegacyRoutesAreAllowlistedAndExcludedFromSpec(t *testing.T) {
	var registered []string
	for _, r := range newServer(nil).routeTable() {
		v1 := strings.HasPrefix(r.Path, "/api/v1/")
		switch {
		case r.Infra:
			if r.Legacy || v1 {
				t.Errorf("%s is infrastructure and cannot also be legacy or versioned", r.pattern())
			}
		case r.Legacy:
			if v1 {
				t.Errorf("%s is under /api/v1 but flagged legacy", r.pattern())
			}
			registered = append(registered, r.pattern())
		case !v1:
			t.Errorf("%s is outside /api/v1 and not flagged Legacy: the contract lives under /api/v1", r.pattern())
		}
	}
	slices.Sort(registered)
	want := slices.Sorted(slices.Values(legacyRoutes))
	if !slices.Equal(registered, want) {
		t.Errorf("legacy routes registered:\n%v\nallowlist:\n%v", registered, want)
	}

	doc, err := OpenAPI(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for op := range specOperations(t, doc) {
		if _, path, _ := strings.Cut(op, " "); !strings.HasPrefix(path, "/api/v1/") {
			t.Errorf("the document lists %s: legacy routes stay out of it", op)
		}
	}
}

// The mux is built from the table alone: no handler is registered anywhere else.
func TestMuxHasNoRouteOutsideTheTable(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := f.Name()
		// routes.go is the table; health.go builds the health listener's own two-route mux.
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "routes.go" || name == "health.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, ".HandleFunc(") || strings.Contains(line, ".Handle(") {
				t.Errorf("%s:%d registers a route outside the table: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// The legacy routes keep their paths and middleware; they get the error envelope and a request
// id like everything else under /api.
func TestLegacyRoutesKeepTheirPathsAndGetTheEnvelope(t *testing.T) {
	s := newServer(nil)
	h := s.Handler()
	for _, pattern := range legacyRoutes {
		method, path, _ := strings.Cut(pattern, " ")
		// Without a user the chain refuses every one of them.
		w := req{method: method, path: samplePath(path), header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
		e := wantError(t, w, http.StatusUnauthorized, model.ErrCodeUnauthenticated)
		if id := w.Header().Get("X-Request-Id"); id == "" || e.RequestID != id {
			t.Errorf("%s: requestId %q, header %q", pattern, e.RequestID, id)
		}
	}

	// The ones that read answer where they always did.
	for _, path := range []string{"/api/config", "/api/vms/100", "/api/pods/apps/web", "/api/logs?ns=media&pod=web"} {
		if w := (req{path: path}.do(h)); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, w.Code, w.Body)
		}
	}
	// And a failing one carries a code and its request id.
	w := req{path: "/api/vms/abc"}.do(h)
	e := wantError(t, w, http.StatusBadRequest, model.ErrCodeInvalidInput)
	if e.RequestID == "" || e.RequestID != w.Header().Get("X-Request-Id") {
		t.Errorf("requestId %q, header %q", e.RequestID, w.Header().Get("X-Request-Id"))
	}
}

// The three core paths that moved to /api/v1 are gone, with no alias.
func TestMovedCorePathsAre404WithCode(t *testing.T) {
	h := newServer(nil).Handler()
	for _, path := range []string{"/api/me", "/api/stream", "/api/series?name=host.cpu&range=1h", "/api/topics/vms", "/api/plugins"} {
		w := req{path: path}.do(h)
		e := wantError(t, w, http.StatusNotFound, model.ErrCodeNotFound)
		if e.RequestID == "" || e.RequestID != w.Header().Get("X-Request-Id") {
			t.Errorf("%s: requestId %q, header %q", path, e.RequestID, w.Header().Get("X-Request-Id"))
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
