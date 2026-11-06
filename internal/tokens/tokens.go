// Package tokens resolves bearer credentials to principals (design M.1, M.4, M.5, M.6): the
// personal access tokens of the credential store, the static tokens of auth.tokens and the
// OAuth access tokens of internal/oauth. A token is bound to one audience; presented at the
// other surface it is refused like an unknown one.
package tokens

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/oauth"
)

// ErrInvalid is every refusal: unknown, expired, revoked, for another audience, or an owner the
// current mapping gives no role. The API answers all of them with the same 401 invalid_token.
var ErrInvalid = errors.New("tokens: invalid token")

// AudienceError is a valid token presented at the other surface. It is an ErrInvalid (the same
// 401), and its message names the audience the token is for, which the client already holds.
type AudienceError struct{ Audience string }

func (e *AudienceError) Error() string {
	return "token not valid for this API (audience " + e.Audience + ")"
}

// Is makes an AudienceError an ErrInvalid.
func (e *AudienceError) Is(target error) bool { return target == ErrInvalid }

// TokenPrefix starts every dash token: dash_pat_ (personal access tokens), dash_st_ (static
// tokens, from dash token generate), dash_at_ (OAuth access tokens) and dash_rt_ (OAuth refresh
// tokens, which only /oauth/token takes). A credential without it is refused before any lookup.
const TokenPrefix = "dash_"

// StaticIDPrefix starts the TokenID of a static token's principal: static:<name>.
const StaticIDPrefix = "static:"

// StaticUserPrefix starts the user of a static token's principal: token:<name>.
const StaticUserPrefix = "token:"

// Resolver turns a bearer credential into a token principal. Its methods are safe for
// concurrent use; SetStatic replaces the static tokens for the next request.
type Resolver struct {
	store  *credentials.Store // the personal access tokens and OAuth grants; nil = none
	roles  *auth.RoleHolder   // the live mapping a token owner's role is derived with
	now    func() time.Time
	static atomic.Pointer[map[string]config.StaticToken] // by hash
	oauth  AccessOpener                                  // nil = no OAuth server
}

// AccessOpener opens OAuth access tokens: *oauth.Server.
type AccessOpener interface {
	OpenAccess(token string) (oauth.Access, error)
}

// SetOAuth makes the resolver accept the access tokens o seals. Call it before serving.
func (r *Resolver) SetOAuth(o AccessOpener) { r.oauth = o }

// New is a resolver over store's personal access tokens, deriving their owners' roles with
// roles (the mode's live mapping), and the static tokens.
func New(store *credentials.Store, roles *auth.RoleHolder, static []config.StaticToken, now func() time.Time) *Resolver {
	if now == nil {
		now = time.Now
	}
	if roles == nil {
		roles = &auth.RoleHolder{}
	}
	r := &Resolver{store: store, roles: roles, now: now}
	r.SetStatic(static)
	return r
}

// SetStatic replaces the static tokens (auth.tokens, live on reload): a removed entry is
// refused from the next request on.
func (r *Resolver) SetStatic(static []config.StaticToken) {
	m := make(map[string]config.StaticToken, len(static))
	for _, t := range static {
		t.Scopes = slices.Clone(t.Scopes)
		m[t.Hash] = t
	}
	r.static.Store(&m)
}

// Resolve returns the principal of token at audience (api or mcp; "" refuses every token).
// A credential that is not a dash token (dash_...) is refused before any lookup. The static
// tokens are looked up by the hash of what was presented, so no secret is compared; a personal
// access token is compared in constant time (credentials.Store.LookupPAT), and its use is noted
// only once it passed the audience and role checks.
func (r *Resolver) Resolve(token, audience string) (auth.Principal, error) {
	if audience == "" || !strings.HasPrefix(token, TokenPrefix) {
		return auth.Principal{}, ErrInvalid
	}
	if st, ok := (*r.static.Load())[credentials.HashToken(token)]; ok {
		if st.Audience != audience {
			return auth.Principal{}, &AudienceError{Audience: st.Audience}
		}
		return auth.Principal{
			Kind: auth.KindToken, User: StaticUserPrefix + st.Name, Role: auth.Role(st.Role), Via: "bearer",
			TokenID: StaticIDPrefix + st.Name, Audience: st.Audience, Scopes: slices.Clone(st.Scopes),
		}, nil
	}
	if r.store == nil {
		return auth.Principal{}, ErrInvalid
	}
	if strings.HasPrefix(token, oauth.AccessPrefix) {
		return r.resolveAccess(token, audience)
	}
	now := r.now()
	p, err := r.store.LookupPAT(token, now)
	if err != nil {
		return auth.Principal{}, ErrInvalid
	}
	if p.Audience != audience {
		return auth.Principal{}, &AudienceError{Audience: p.Audience}
	}
	role := r.roles.RoleForClaims(p.User, p.Groups, p.RoleClaim)
	if role == "" {
		return auth.Principal{}, ErrInvalid
	}
	r.store.NoteUsed(p.ID, now)
	return auth.Principal{
		Kind: auth.KindToken, User: p.User, Role: role, Via: "bearer", Groups: p.Groups, RoleClaim: p.RoleClaim,
		TokenID: p.ID, Audience: p.Audience, Scopes: p.Scopes, ActionNamespaces: p.ActionNamespaces,
	}, nil
}

// resolveAccess resolves an OAuth access token: sealed by the server, not expired, its grant
// still there and active (a revoked grant ends its access tokens at once), at its audience. The
// role is derived from the groups and role-claim value the grant recorded, against the current
// mapping, and the token's scopes are narrowed to the grant's. A token that passed notes a use
// of its grant (Connected apps' "Last used"), as a personal access token does.
func (r *Resolver) resolveAccess(token, audience string) (auth.Principal, error) {
	if r.oauth == nil {
		return auth.Principal{}, ErrInvalid
	}
	a, err := r.oauth.OpenAccess(token)
	if err != nil {
		return auth.Principal{}, ErrInvalid
	}
	now := r.now()
	g, ok := r.store.GrantByID(a.GrantID)
	if !ok || !g.Active(now) || g.Audience != a.Audience {
		return auth.Principal{}, ErrInvalid
	}
	if g.Audience != audience {
		return auth.Principal{}, &AudienceError{Audience: g.Audience}
	}
	role := r.roles.RoleForClaims(g.User, g.Groups, g.RoleClaim)
	if role == "" {
		return auth.Principal{}, ErrInvalid
	}
	var scopes []string
	for _, sc := range a.Scopes {
		if slices.Contains(g.Scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	r.store.NoteGrantUsed(g.ID, now)
	return auth.Principal{
		Kind: auth.KindToken, User: g.User, Role: role, Via: "bearer", Groups: g.Groups, RoleClaim: g.RoleClaim,
		GrantID: g.ID, ClientID: g.ClientID, Audience: g.Audience, Scopes: scopes,
	}, nil
}

// Revive is p, a personal access token's or an OAuth grant's principal resolved earlier, as its
// credential stands now (contract D52): an approval runs as the requester at the moment it is
// approved. The token or grant must still exist, be unexpired (a grant active) and be for the
// same audience, and its owner must still have a role. A token's scopes and actionNamespaces
// are the token's now; a grant's scopes are p's that the grant still holds. A static token
// never revives: it has no owner to approve for it. Every refusal is ErrInvalid.
func (r *Resolver) Revive(p auth.Principal) (auth.Principal, error) {
	if r.store == nil || p.Kind != auth.KindToken || strings.HasPrefix(p.TokenID, StaticIDPrefix) {
		return auth.Principal{}, ErrInvalid
	}
	now := r.now()
	switch {
	case p.TokenID != "":
		t, ok := r.store.PATByID(p.TokenID)
		if !ok || !now.Before(t.ExpiresAt) || t.Audience != p.Audience || t.User != p.User {
			return auth.Principal{}, ErrInvalid
		}
		role := r.roles.RoleForClaims(t.User, t.Groups, t.RoleClaim)
		if role == "" {
			return auth.Principal{}, ErrInvalid
		}
		return auth.Principal{
			Kind: auth.KindToken, User: t.User, Role: role, Via: "bearer", Groups: t.Groups, RoleClaim: t.RoleClaim,
			TokenID: t.ID, Audience: t.Audience, Scopes: slices.Clone(t.Scopes), ActionNamespaces: slices.Clone(t.ActionNamespaces),
		}, nil
	case p.GrantID != "":
		g, ok := r.store.GrantByID(p.GrantID)
		if !ok || !g.Active(now) || g.Audience != p.Audience || g.User != p.User {
			return auth.Principal{}, ErrInvalid
		}
		role := r.roles.RoleForClaims(g.User, g.Groups, g.RoleClaim)
		if role == "" {
			return auth.Principal{}, ErrInvalid
		}
		var scopes []string
		for _, sc := range p.Scopes {
			if slices.Contains(g.Scopes, sc) {
				scopes = append(scopes, sc)
			}
		}
		return auth.Principal{
			Kind: auth.KindToken, User: g.User, Role: role, Via: "bearer", Groups: g.Groups, RoleClaim: g.RoleClaim,
			GrantID: g.ID, ClientID: g.ClientID, Audience: g.Audience, Scopes: scopes,
		}, nil
	}
	return auth.Principal{}, ErrInvalid
}

// CredentialName is the name a person knows p's credential by: a personal access token's name,
// an OAuth grant's client name, a static token's name; "" when there is none.
func (r *Resolver) CredentialName(p auth.Principal) string {
	switch {
	case strings.HasPrefix(p.TokenID, StaticIDPrefix):
		return strings.TrimPrefix(p.TokenID, StaticIDPrefix)
	case r.store == nil:
		return ""
	case p.TokenID != "":
		t, _ := r.store.PATByID(p.TokenID)
		return t.Name
	case p.GrantID != "":
		g, _ := r.store.GrantByID(p.GrantID)
		return g.ClientName
	}
	return ""
}
