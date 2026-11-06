package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/oauth"
)

// A grant whose authorization code expired before it was exchanged is revoked by the next
// sweep and audited as oauth.grant.expire (reason code_expired), by dash itself.
func TestExpiredCodeGrantIsAudited(t *testing.T) {
	clock := fixedNow
	s, dir := auditedServer(t, func(d *Deps) {
		store, err := credentials.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		d.Credentials = store
		o, err := oauth.New(oauth.Options{
			PublicURL: d.Cfg.PublicURL, Store: store, AccessKey: make([]byte, 32),
			Clients: []config.OAuthClient{{ClientID: "inspector", Name: "Inspector", RedirectURIs: []string{"http://127.0.0.1/callback"}, Audiences: []string{"api"}}},
			Now:     func() time.Time { return clock },
		})
		if err != nil {
			t.Fatal(err)
		}
		d.OAuth = o
	})
	consent := func() string {
		t.Helper()
		w := req{path: authorizeURL("api", nil)}.do(s.Handler())
		loc := w.Header().Get("Location")
		if w.Code != http.StatusFound || !strings.Contains(loc, "/consent/") {
			t.Fatalf("authorize = %d %s", w.Code, loc)
		}
		return loc[strings.LastIndex(loc, "/")+1:]
	}
	if _, err := s.d.OAuth.Approve(consent(), aliceAdmin, []string{"read"}, nil); err != nil {
		t.Fatal(err)
	}
	gid := s.d.Credentials.Grants("")[0].ID
	clock = clock.Add(oauth.CodeTTL)
	consent() // the next request sweeps the expired code
	if n := len(s.d.Credentials.Grants("")); n != 0 {
		t.Fatalf("grants after the code expired = %d", n)
	}
	var found []audit.Record
	for _, r := range auditRecords(t, dir) {
		if r.Action == auditGrantExpire {
			found = append(found, r)
		}
	}
	want := audit.Actor{Type: "system", User: "dash"}
	if len(found) != 1 || found[0].Kind != audit.KindEvent || found[0].Reason != "code_expired" || found[0].Target != grantTarget(credentials.Grant{ID: gid, User: "alice"}) || found[0].Actor != want {
		t.Errorf("expire records = %+v, want one event for grant %s", found, gid)
	}
}
