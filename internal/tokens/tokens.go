// Package tokens resolves bearer credentials to principals (design M.1, M.4, M.5): the
// personal access tokens of the credential store and the static tokens of auth.tokens. A token
// is bound to one audience; presented at the other surface it is refused like an unknown one.
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
// tokens, from dash token generate) and the OAuth tokens of a later release. A credential
// without it is refused before any lookup.
const TokenPrefix = "dash_"

// StaticIDPrefix starts the TokenID of a static token's principal: static:<name>.
const StaticIDPrefix = "static:"

// StaticUserPrefix starts the user of a static token's principal: token:<name>.
const StaticUserPrefix = "token:"

// Resolver turns a bearer credential into a token principal. Its methods are safe for
// concurrent use; SetStatic replaces the static tokens for the next request.
type Resolver struct {
	store  *credentials.Store // the personal access tokens; nil = none
	roles  *auth.RoleHolder   // the live mapping a token owner's role is derived with
	now    func() time.Time
	static atomic.Pointer[map[string]config.StaticToken] // by hash
}

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
