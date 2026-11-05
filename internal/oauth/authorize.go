package oauth

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// pending is an authorization request waiting for the user's consent (PendingTTL).
type pending struct {
	id          string
	client      Client
	redirectURI string
	state       string
	challenge   string // the S256 code_challenge
	audience    string
	scopes      []string // what the client asked for, validated for the client and the audience
	user        string   // the person who first opened the consent, "" until then
	expires     time.Time
}

// code is an authorization code (CodeTTL, single use). The grant exists from the consent on;
// the refresh token it carries is handed out with the first access token.
type code struct {
	grantID     string
	refresh     string
	clientID    string
	redirectURI string
	challenge   string
	audience    string
	scopes      []string
	expires     time.Time
	used        bool
}

// codeChallengeRE is an RFC 7636 code challenge or verifier: 43 to 128 unreserved characters.
var codeChallengeRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// maxState bounds the state parameter, which dash only echoes.
const maxState = 1024

// AuthorizeResult is what /oauth/authorize answers. Exactly one field is set: ConsentURL, the
// SPA's consent page for a valid request; ErrorRedirect, the client's redirect URI with the
// error, once client and redirect URI are verified (RFC 6749 4.1.2.1).
type AuthorizeResult struct {
	ConsentURL    string
	ErrorRedirect string
}

// ErrBusy refuses an authorization request while maxPending requests wait for consent.
var ErrBusy = errors.New("oauth: too many authorization requests waiting for consent; try again later")

// Authorize validates an authorization request (the query of /oauth/authorize) and stores it
// for the consent. An *Error comes back when the client or its redirect URI cannot be verified:
// it is shown in the browser, never sent anywhere. actions are the plugins with actions enabled.
func (s *Server) Authorize(ctx context.Context, q url.Values, actions []string) (AuthorizeResult, error) {
	if k := duplicated(q, "client_id", "redirect_uri"); k != "" {
		return AuthorizeResult{}, oauthErr("invalid_request", k+" is given more than once")
	}
	clientID, _ := single(q, "client_id")
	if clientID == "" {
		return AuthorizeResult{}, oauthErr("invalid_request", "client_id is required")
	}
	c, err := s.client(ctx, clientID)
	if err != nil {
		return AuthorizeResult{}, oauthErr("invalid_client", err.Error())
	}
	redirect, _ := single(q, "redirect_uri")
	if redirect == "" || !c.redirectAllowed(redirect) {
		return AuthorizeResult{}, oauthErr("invalid_request", "redirect_uri is not one the client registered")
	}

	// From here on an error goes back to the client.
	state, _ := single(q, "state")
	fail := func(code, desc string) (AuthorizeResult, error) {
		if len(state) > maxState {
			state = ""
		}
		return AuthorizeResult{ErrorRedirect: withParams(redirect, url.Values{
			"error": {code}, "error_description": {desc}, "state": {state}, "iss": {s.base},
		})}, nil
	}
	if k := duplicated(q, "response_type", "state", "code_challenge", "code_challenge_method", "resource", "scope"); k != "" {
		return fail("invalid_request", k+" is given more than once")
	}
	if rt, _ := single(q, "response_type"); rt != "code" {
		return fail("unsupported_response_type", "response_type must be code")
	}
	if state == "" || len(state) > maxState {
		return fail("invalid_request", "state is required (at most 1024 bytes)")
	}
	challenge, _ := single(q, "code_challenge")
	method, _ := single(q, "code_challenge_method")
	switch {
	case challenge == "":
		return fail("invalid_request", "PKCE is required: code_challenge with code_challenge_method S256")
	case method != "S256":
		return fail("invalid_request", "code_challenge_method must be S256")
	case !codeChallengeRE.MatchString(challenge):
		return fail("invalid_request", "code_challenge is not 43 to 128 characters of base64url")
	}
	resource, _ := single(q, "resource")
	if resource == "" {
		return fail("invalid_request", "resource is required: "+s.Resource(model.TokenAudienceAPI)+" or "+s.Resource(model.TokenAudienceMCP))
	}
	audience := s.audienceOf(resource)
	if audience == "" {
		return fail("invalid_target", "resource is not "+s.Resource(model.TokenAudienceAPI)+" or "+s.Resource(model.TokenAudienceMCP))
	}
	if !slices.Contains(c.Audiences, audience) {
		return fail("invalid_target", "this client may not ask for the "+audience+" resource")
	}
	rawScope, _ := single(q, "scope")
	scopes, desc := s.requestedScopes(c, audience, rawScope, actions)
	if desc != "" {
		return fail("invalid_scope", desc)
	}

	id, err := randomID()
	if err != nil {
		return AuthorizeResult{}, err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if len(s.pending) >= maxPending {
		return AuthorizeResult{}, ErrBusy
	}
	s.pending[id] = &pending{
		id: id, client: *c, redirectURI: redirect, state: state, challenge: challenge,
		audience: audience, scopes: scopes, expires: now.Add(PendingTTL),
	}
	return AuthorizeResult{ConsentURL: s.publicURL + "/consent/" + id}, nil
}

// requestedScopes parses the scope parameter: absent means read; each scope must be one the
// audience supports, and approvals and push only for dash-android (design M.6), which gets
// invalid_scope for any other client. desc is the refusal, "" when the scopes are fine.
func (s *Server) requestedScopes(c *Client, audience, raw string, actions []string) (scopes []string, desc string) {
	supported := ScopesSupported(audience, actions)
	for _, sc := range strings.Fields(raw) {
		switch {
		case (sc == model.OAuthScopeApprovals || sc == model.OAuthScopePush) && c.ID != model.OAuthClientAndroid:
			return nil, sc + " is only for the first-party app"
		case !slices.Contains(supported, sc):
			return nil, "scope " + sc + " is not supported for the " + audience + " resource"
		}
		if !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	if len(scopes) == 0 {
		scopes = []string{model.TokenScopeRead}
	}
	return scopes, ""
}

// sweepLocked forgets expired pending requests and codes. A code that expired unused revokes
// the grant its consent created, which no client can use any more. s.mu is held.
func (s *Server) sweepLocked(now time.Time) {
	for id, p := range s.pending {
		if !now.Before(p.expires) {
			delete(s.pending, id)
		}
	}
	for c, cd := range s.codes {
		if now.Before(cd.expires) {
			continue
		}
		delete(s.codes, c)
		if !cd.used {
			_, _, _ = s.store.RevokeGrant(cd.grantID)
		}
	}
}
