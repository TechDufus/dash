package tokens

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/oauth"
)

// opener opens every dash_at_ token as the access it holds.
type opener struct{ a oauth.Access }

func (o opener) OpenAccess(string) (oauth.Access, error) { return o.a, nil }

// An OAuth access token resolves only while its grant exists and is active, at the grant's
// audience; its role comes from the grant's recorded groups and the current mapping, and its
// scopes are narrowed to the grant's.
func TestResolveOAuthAccess(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	store, err := credentials.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	id, _ := credentials.NewGrantID()
	_, hash, _ := credentials.NewRefreshToken(id)
	g := credentials.Grant{ID: id, User: "vera", ClientID: "inspector", Audience: "api", Scopes: []string{"read"}, Groups: []string{"ops"},
		RefreshHash: hash, CreatedAt: now, ExpiresAt: now.Add(credentials.GrantLifetime), RefreshedAt: now}
	if err := store.AddGrant(g); err != nil {
		t.Fatal(err)
	}
	roles := auth.NewRoleHolder(auth.RoleMap{AdminGroups: []string{"ops"}})
	clock := now
	r := New(store, roles, nil, func() time.Time { return clock })
	const tok = "dash_at_sealed"
	if _, err := r.Resolve(tok, "api"); !errors.Is(err, ErrInvalid) {
		t.Errorf("without an OAuth server: %v", err)
	}
	r.SetOAuth(opener{oauth.Access{GrantID: id, Audience: "api", Scopes: []string{"read", "logs"}}})
	clock = now.Add(time.Minute)
	if _, err := r.Resolve(tok, "mcp"); !errors.As(err, new(*AudienceError)) {
		t.Errorf("at mcp: %v", err)
	}
	if l := store.Grants("vera"); l[0].LastUsedAt != nil {
		t.Errorf("a refused token noted a use: %v", l[0].LastUsedAt)
	}
	p, err := r.Resolve(tok, "api")
	if err != nil || p.Kind != auth.KindToken || p.User != "vera" || p.Role != auth.RoleAdmin || p.GrantID != id || p.ClientID != "inspector" ||
		!slices.Equal(p.Scopes, []string{"read"}) || p.TokenID != "" {
		t.Fatalf("principal = %+v, %v", p, err)
	}
	// Connected apps' "Last used" is the access token's use, not only the last refresh.
	if l := store.Grants("vera"); l[0].LastUsedAt == nil || !l[0].LastUsedAt.Equal(clock) {
		t.Errorf("lastUsedAt after a request = %v, want %v", l[0].LastUsedAt, clock)
	}
	clock = now
	roles.Store(auth.RoleMap{})
	if p, _ := r.Resolve(tok, "api"); p.Role != auth.RoleViewer {
		t.Errorf("after a mapping change: role %q", p.Role)
	}
	clock = now.Add(credentials.GrantIdle)
	if _, err := r.Resolve(tok, "api"); !errors.Is(err, ErrInvalid) {
		t.Errorf("an idle grant: %v", err)
	}
	clock = now
	if _, _, err := store.RevokeGrant(id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(tok, "api"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a revoked grant: %v", err)
	}
}
