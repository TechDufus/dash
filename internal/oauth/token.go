package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/credentials"
)

// access is the sealed content of an access token: the grant, its audience, the scopes (the
// grant's, or fewer when a refresh asked for fewer) and the expiry in unix seconds.
type access struct {
	Grant    string   `json:"g"`
	Audience string   `json:"a"`
	Scopes   []string `json:"s"`
	Expires  int64    `json:"e"`
}

// Access is what an access token says once opened. Its grant must still exist and be active,
// and the role is derived from it, on every request (tokens.Resolver).
type Access struct {
	GrantID, Audience string
	Scopes            []string
	Expires           time.Time
}

// Errors of OpenAccess; the bearer step answers every one with 401 invalid_token.
var (
	ErrAccessInvalid = errors.New("oauth: invalid access token")
	ErrAccessExpired = errors.New("oauth: expired access token")
)

// sealedVersion is the version prefix of session.Codec, which an access token leaves out: a dot
// would end the scrubber's token class (contract D7).
const sealedVersion = "v1."

func (s *Server) sealAccess(g credentials.Grant, scopes []string, now time.Time) (string, error) {
	sealed, err := s.codec.Seal(access{Grant: g.ID, Audience: g.Audience, Scopes: scopes, Expires: now.Add(AccessTTL).Unix()})
	if err != nil {
		return "", err
	}
	return AccessPrefix + strings.TrimPrefix(sealed, sealedVersion), nil
}

// OpenAccess opens an access token sealed by this server (or by one with the same key: the
// same DASH_SECRET_KEY and credentials.yaml). It checks the seal and the expiry only.
func (s *Server) OpenAccess(token string) (Access, error) {
	body, ok := strings.CutPrefix(token, AccessPrefix)
	if !ok || strings.Contains(body, ".") {
		return Access{}, ErrAccessInvalid
	}
	var a access
	if err := s.codec.Open(sealedVersion+body, &a); err != nil || a.Grant == "" {
		return Access{}, ErrAccessInvalid
	}
	exp := time.Unix(a.Expires, 0)
	if !s.now().Before(exp) {
		return Access{}, ErrAccessExpired
	}
	return Access{GrantID: a.Grant, Audience: a.Audience, Scopes: a.Scopes, Expires: exp}, nil
}

// TokenResponse is the answer of /oauth/token (RFC 6749 5.1).
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// TokenEvent reports what a token request did to a grant that the API records in the audit
// log: the grant revoked because a code or a refresh token was used twice.
type TokenEvent struct {
	Grant  credentials.Grant
	Reason string // "code_reuse", "refresh_reuse" or "code_misuse"
}

// Token answers a token request (the form of /oauth/token): authorization_code with PKCE, or
// refresh_token with rotation. clientID is the request's client_id, which the API also keys
// its rate limit on. A grant revoked because of reuse comes back as an event.
func (s *Server) Token(form url.Values) (*TokenResponse, *TokenEvent, *Error) {
	if k := duplicated(form, "grant_type", "code", "redirect_uri", "client_id", "code_verifier", "refresh_token", "resource", "scope"); k != "" {
		return nil, nil, oauthErr("invalid_request", k+" is given more than once")
	}
	clientID, _ := single(form, "client_id")
	if clientID == "" {
		return nil, nil, oauthErr("invalid_request", "client_id is required")
	}
	switch gt, _ := single(form, "grant_type"); gt {
	case "authorization_code":
		return s.exchangeCode(form, clientID)
	case "refresh_token":
		return s.refresh(form, clientID)
	case "":
		return nil, nil, oauthErr("invalid_request", "grant_type is required")
	default:
		return nil, nil, oauthErr("unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// pkceMatches checks an RFC 7636 S256 verifier against the challenge, in constant time.
func pkceMatches(verifier, challenge string) bool {
	if !codeChallengeRE.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

func (s *Server) exchangeCode(form url.Values, clientID string) (*TokenResponse, *TokenEvent, *Error) {
	value, _ := single(form, "code")
	now := s.now()
	s.mu.Lock()
	s.sweepLocked(now)
	c, ok := s.codes[value]
	if ok && c.used {
		// A code used twice was intercepted: the grant it made goes (OAuth 2.1 4.1.3).
		s.mu.Unlock()
		return nil, s.revokeFor(c.grantID, "code_reuse"), oauthErr("invalid_grant", "the code was already used")
	}
	if ok {
		c.used = true
	}
	s.mu.Unlock()
	if !ok || value == "" {
		return nil, nil, oauthErr("invalid_grant", "the code is unknown or expired")
	}
	redirect, _ := single(form, "redirect_uri")
	verifier, _ := single(form, "code_verifier")
	resource, _ := single(form, "resource")
	switch {
	case clientID != c.clientID:
		return nil, s.revokeFor(c.grantID, "code_misuse"), oauthErr("invalid_grant", "the code was issued to another client")
	case redirect != c.redirectURI:
		return nil, s.revokeFor(c.grantID, "code_misuse"), oauthErr("invalid_grant", "redirect_uri differs from the authorization request")
	case !pkceMatches(verifier, c.challenge):
		return nil, s.revokeFor(c.grantID, "code_misuse"), oauthErr("invalid_grant", "code_verifier does not match the code_challenge")
	case resource != "" && s.audienceOf(resource) != c.audience:
		return nil, s.revokeFor(c.grantID, "code_misuse"), oauthErr("invalid_target", "resource differs from the authorization request")
	}
	g, found := s.store.GrantByID(c.grantID)
	if !found || !g.Active(now) {
		return nil, nil, oauthErr("invalid_grant", "the grant was revoked")
	}
	at, err := s.sealAccess(g, c.scopes, now)
	if err != nil {
		return nil, nil, oauthErr("server_error", "")
	}
	return &TokenResponse{AccessToken: at, TokenType: "Bearer", ExpiresIn: int(AccessTTL.Seconds()), RefreshToken: c.refresh, Scope: strings.Join(c.scopes, " ")}, nil, nil
}

func (s *Server) refresh(form url.Values, clientID string) (*TokenResponse, *TokenEvent, *Error) {
	rt, _ := single(form, "refresh_token")
	id, ok := credentials.ParseRefreshToken(rt)
	if !ok {
		return nil, nil, oauthErr("invalid_grant", "the refresh token is unknown")
	}
	g, found := s.store.GrantByID(id)
	if !found {
		return nil, nil, oauthErr("invalid_grant", "the refresh token is unknown or revoked")
	}
	// Only the client the grant is for may use it: checked before the rotation, so a wrong
	// client neither rotates nor revokes anything.
	if g.ClientID != clientID {
		return nil, nil, oauthErr("invalid_grant", "the refresh token was issued to another client")
	}
	if resource, _ := single(form, "resource"); resource != "" && s.audienceOf(resource) != g.Audience {
		return nil, nil, oauthErr("invalid_target", "resource differs from the grant's")
	}
	scopes := g.Scopes
	if raw, _ := single(form, "scope"); raw != "" {
		scopes = strings.Fields(raw)
		if slices.ContainsFunc(scopes, func(sc string) bool { return !slices.Contains(g.Scopes, sc) }) {
			return nil, nil, oauthErr("invalid_scope", "a refresh may only narrow the granted scopes")
		}
	}
	next, hash, err := credentials.NewRefreshToken(id)
	if err != nil {
		return nil, nil, oauthErr("server_error", "")
	}
	now := s.now()
	rotated, err := s.store.RotateRefresh(rt, hash, now)
	switch {
	case errors.Is(err, credentials.ErrRefreshReuse):
		return nil, &TokenEvent{Grant: g, Reason: "refresh_reuse"}, oauthErr("invalid_grant", "the refresh token was already used; the grant is revoked")
	case errors.Is(err, credentials.ErrTokenUnknown), errors.Is(err, credentials.ErrTokenExpired):
		return nil, nil, oauthErr("invalid_grant", "the refresh token is unknown, expired or revoked")
	case err != nil:
		return nil, nil, oauthErr("server_error", "the grant could not be written")
	}
	at, err := s.sealAccess(rotated, scopes, now)
	if err != nil {
		return nil, nil, oauthErr("server_error", "")
	}
	return &TokenResponse{AccessToken: at, TokenType: "Bearer", ExpiresIn: int(AccessTTL.Seconds()), RefreshToken: next, Scope: strings.Join(scopes, " ")}, nil, nil
}

// revokeFor revokes grant id for reason and reports it; nil when it was gone already.
func (s *Server) revokeFor(id, reason string) *TokenEvent {
	g, ok, _ := s.store.RevokeGrant(id)
	if !ok {
		return nil
	}
	return &TokenEvent{Grant: g, Reason: reason}
}

// RevocationTarget is the grant a revocation request (RFC 7009, the form of /oauth/revoke)
// names: by its refresh token, current or rotated, or by an access token it sealed. false when
// the token names none, or the request's client_id is another client's: RFC 7009 answers both
// with 200 and does nothing. Revoking either kind of token revokes the grant, and so every
// token of it.
func (s *Server) RevocationTarget(form url.Values) (credentials.Grant, bool) {
	if duplicated(form, "token", "token_type_hint", "client_id") != "" {
		return credentials.Grant{}, false
	}
	token, _ := single(form, "token")
	clientID, _ := single(form, "client_id")
	var g credentials.Grant
	var ok bool
	if strings.HasPrefix(token, credentials.RefreshPrefix) {
		g, ok = s.store.RefreshGrant(token)
	} else if a, err := s.OpenAccess(token); err == nil {
		g, ok = s.store.GrantByID(a.GrantID)
	}
	if !ok || (clientID != "" && clientID != g.ClientID) {
		return credentials.Grant{}, false
	}
	return g, true
}
