package demo

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// SignIn names the demo viewer on every request that names no user and passes every request on:
// it answers nothing itself, so the retired routes (/api/actions/*, /api/silences) reach the API,
// which refuses them as it does in production.
func TestSignInNamesTheViewerAndPassesEveryRequestOn(t *testing.T) {
	var user string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user = r.Header.Get("X-Forwarded-User")
		w.WriteHeader(http.StatusTeapot)
	})
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/actions/reconcile"}, {http.MethodPost, "/api/silences"},
		{http.MethodDelete, "/api/silences/abc"}, {http.MethodPost, "/api/actions/restart"},
		{http.MethodGet, "/api/v1/me"},
	} {
		user = ""
		rec := httptest.NewRecorder()
		SignIn(next).ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusTeapot || user != "demo" {
			t.Errorf("%s %s: got %d, user %q, want the request on to the API as demo", c.method, c.path, rec.Code, user)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.Header.Set("X-Forwarded-User", "alice")
	SignIn(next).ServeHTTP(httptest.NewRecorder(), r)
	if user != "alice" {
		t.Errorf("a request that names a user was signed in as %q", user)
	}
}
