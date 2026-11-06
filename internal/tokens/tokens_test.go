package tokens

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func addPAT(t *testing.T, s *credentials.Store, p credentials.PAT) string {
	t.Helper()
	token, id, hash, err := credentials.NewPAT()
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Hash, p.Name, p.CreatedAt, p.ExpiresAt = id, hash, "t", now, now.Add(time.Hour)
	if err := s.AddPAT(p); err != nil {
		t.Fatal(err)
	}
	return token
}

// A personal access token resolves at its own audience only, and its owner's role is derived
// from the recorded groups with the current mapping on every request.
func TestPATPrincipalAndRole(t *testing.T) {
	s, err := credentials.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	roles := auth.NewRoleHolder(auth.RoleMap{AdminGroups: []string{"ops"}})
	r := New(s, roles, nil, func() time.Time { return now })
	token := addPAT(t, s, credentials.PAT{User: "bob", Audience: "api", Scopes: []string{"read"}, Groups: []string{"ops"}, ActionNamespaces: []string{"apps"}})

	p, err := r.Resolve(token, "api")
	if err != nil || p.Kind != auth.KindToken || p.User != "bob" || p.Role != auth.RoleAdmin || p.Audience != "api" ||
		!slices.Equal(p.Scopes, []string{"read"}) || !slices.Equal(p.ActionNamespaces, []string{"apps"}) || p.TokenID == "" || p.Interactive() {
		t.Fatalf("Resolve = %+v, %v", p, err)
	}
	if _, err := r.Resolve(token, "mcp"); !errors.Is(err, ErrInvalid) {
		t.Errorf("at the other audience: %v", err)
	}
	if _, err := r.Resolve(token, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("outside both surfaces: %v", err)
	}
	roles.Store(auth.RoleMap{})
	if p, _ := r.Resolve(token, "api"); p.Role != auth.RoleViewer {
		t.Errorf("after the mapping changed: role %q, want viewer", p.Role)
	}
}

// A static token is token:<name> with its configured role; SetStatic removes it at once.
func TestStaticTokens(t *testing.T) {
	token, _, hash, err := credentials.NewPAT()
	if err != nil {
		t.Fatal(err)
	}
	r := New(nil, nil, []config.StaticToken{{Name: "ci", Role: "viewer", Audience: "mcp", Scopes: []string{"read"}, Hash: hash}}, nil)
	p, err := r.Resolve(token, "mcp")
	if err != nil || p.User != "token:ci" || p.TokenID != "static:ci" || p.Role != auth.RoleViewer || p.Kind != auth.KindToken {
		t.Fatalf("Resolve = %+v, %v", p, err)
	}
	if _, err := r.Resolve(token, "api"); err == nil {
		t.Error("a static mcp token resolved at the api")
	}
	r.SetStatic(nil)
	if _, err := r.Resolve(token, "mcp"); err == nil {
		t.Error("a removed static token still resolves")
	}
}

// A credential that is not a dash token is refused before any lookup, even when its hash is
// configured; a token refused at the other audience is not noted as used, an accepted one is.
func TestResolveRefusesNonDashTokensAndNotesOnlyAcceptedUse(t *testing.T) {
	plain := "not-a-dash-token-but-long-enough-to-look-random-0123456789"
	s, err := credentials.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	r := New(s, nil, []config.StaticToken{{Name: "x", Role: "viewer", Audience: "api", Scopes: []string{"read"}, Hash: credentials.HashToken(plain)}}, func() time.Time { return now })
	if _, err := r.Resolve(plain, "api"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a configured hash of a non-dash token resolved: %v", err)
	}
	token := addPAT(t, s, credentials.PAT{User: "bob", Audience: "mcp", Scopes: []string{"read"}})
	if _, err := r.Resolve(token, "api"); err == nil {
		t.Fatal("an mcp token resolved at the api")
	}
	if p := s.PATs("bob"); p[0].LastUsedAt != nil {
		t.Errorf("a refused token was noted as used at %v", p[0].LastUsedAt)
	}
	if _, err := r.Resolve(token, "mcp"); err != nil {
		t.Fatal(err)
	}
	if p := s.PATs("bob"); p[0].LastUsedAt == nil || !p[0].LastUsedAt.Equal(now) {
		t.Errorf("an accepted token's lastUsedAt = %v, want %v", p[0].LastUsedAt, now)
	}
}

// Revive re-reads an earlier principal's credential (contract D52): a token as it stands now
// (its current scopes and namespaces, its owner's current role), a grant with the earlier scopes
// it still holds; a revoked or expired one, another user's, another audience's and a static
// token never revive.
func TestRevive(t *testing.T) {
	s, err := credentials.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	roles := auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{"alice"}})
	r := New(s, roles, nil, func() time.Time { return clock })
	token := addPAT(t, s, credentials.PAT{User: "alice", Audience: "mcp", Scopes: []string{"actions:aa"}, ActionNamespaces: []string{"apps"}})
	p, err := r.Resolve(token, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.Revive(p); err != nil || got.TokenID != p.TokenID || got.Role != auth.RoleAdmin || !slices.Equal(got.Scopes, []string{"actions:aa"}) ||
		!slices.Equal(got.ActionNamespaces, []string{"apps"}) || got.Audience != "mcp" {
		t.Errorf("Revive = %+v, %v", got, err)
	}
	if got := r.CredentialName(p); got != "t" {
		t.Errorf("CredentialName = %q", got)
	}
	roles.Store(auth.RoleMap{})
	if got, _ := r.Revive(p); got.Role != auth.RoleViewer {
		t.Errorf("after the mapping changed: %+v", got)
	}
	for name, bad := range map[string]auth.Principal{
		"another user":     {Kind: auth.KindToken, User: "bob", TokenID: p.TokenID, Audience: "mcp"},
		"another audience": {Kind: auth.KindToken, User: "alice", TokenID: p.TokenID, Audience: "api"},
		"a static token":   {Kind: auth.KindToken, User: StaticUserPrefix + "ci", TokenID: StaticIDPrefix + "ci", Audience: "mcp"},
		"a person":         {Kind: auth.KindProxy, User: "alice"},
	} {
		if _, err := r.Revive(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	clock = now.Add(time.Hour)
	if _, err := r.Revive(p); !errors.Is(err, ErrInvalid) {
		t.Errorf("an expired token: %v", err)
	}
	clock = now
	if _, _, err := s.RevokePAT(p.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Revive(p); !errors.Is(err, ErrInvalid) {
		t.Errorf("a revoked token: %v", err)
	}

	id, err := credentials.NewGrantID()
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := credentials.NewRefreshToken(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddGrant(credentials.Grant{
		ID: id, User: "alice", ClientID: "https://app.example.com/client.json", ClientName: "App", ClientKind: "metadata",
		Audience: "mcp", Scopes: []string{"read", "actions:aa"}, RefreshHash: hash, CreatedAt: now, ExpiresAt: now.Add(credentials.GrantLifetime), RefreshedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	gp := auth.Principal{Kind: auth.KindToken, User: "alice", GrantID: id, Audience: "mcp", Scopes: []string{"actions:aa", "logs"}}
	if got, err := r.Revive(gp); err != nil || got.GrantID != id || got.ClientID == "" || !slices.Equal(got.Scopes, []string{"actions:aa"}) {
		t.Errorf("grant Revive = %+v, %v", got, err)
	}
	if got := r.CredentialName(gp); got != "App" {
		t.Errorf("grant CredentialName = %q", got)
	}
	clock = now.Add(credentials.GrantIdle)
	if _, err := r.Revive(gp); !errors.Is(err, ErrInvalid) {
		t.Errorf("an idle-expired grant: %v", err)
	}
	clock = now
	if _, _, err := s.RevokeGrant(id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Revive(gp); !errors.Is(err, ErrInvalid) {
		t.Errorf("a revoked grant: %v", err)
	}
}
