package model

import "time"

// OAuthClientAndroid is the client ID of the built-in first-party app (design M.6): the only
// client that may be granted approvals or push, and only for the api audience.
const OAuthClientAndroid = "dash-android"

// Scopes only the built-in app may hold (design M.3): an OAuth client other than dash-android
// that asks for one gets invalid_scope, and the consent screen never offers one to it.
const (
	OAuthScopeApprovals = "approvals"
	OAuthScopePush      = "push"
)

// Kinds of OAuth client.
const (
	OAuthClientKindBuiltin  = "builtin"  // dash-android
	OAuthClientKindMetadata = "metadata" // a client ID metadata document: the client ID is its https URL
	OAuthClientKindConfig   = "config"   // an auth.oauth.clients entry
)

// OAuthGrantsMaxPerUser is how many grants one user may hold; a consent past it is refused with
// 409 grant_limit.
const OAuthGrantsMaxPerUser = 20

// OAuthClient names the application an OAuth request or a grant is for.
type OAuthClient struct {
	// ID is dash-android, the URL of a client ID metadata document, or an auth.oauth.clients
	// clientID.
	ID   string `json:"id" dash:"required"`
	Name string `json:"name" dash:"required"` // the name the client gives itself, or its config entry's
	Kind string `json:"kind" dash:"required,enum=builtin|metadata|config"`
}

// OAuthConsent is GET /api/v1/oauth/consents/{id}: an authorization request waiting for the
// signed-in user's decision. It lives 10 minutes.
type OAuthConsent struct {
	ID     string      `json:"id" dash:"required"`
	Client OAuthClient `json:"client" dash:"required"`
	// RedirectURI is where the browser goes with the answer: the client's own address.
	RedirectURI string `json:"redirectUri" dash:"required"`
	Audience    string `json:"audience" dash:"required,enum=api|mcp"` // api (/api/v1) or mcp (/mcp)
	// Scopes are the scopes the client asked for that the user may grant: read, logs,
	// actions:<plugin> (administrators, for a plugin whose actions are enabled) and, for
	// dash-android only, approvals and push. Never empty.
	Scopes []string `json:"scopes" dash:"required"`
	// DefaultScopes are the ones the screen ticks before the user does anything: read, when it
	// is offered, and nothing else.
	DefaultScopes []string  `json:"defaultScopes" dash:"required"`
	ExpiresAt     time.Time `json:"expiresAt" dash:"required"`
}

// OAuthConsentDecision is the body of POST /api/v1/oauth/consents/{id}.
type OAuthConsentDecision struct {
	// Approve true creates the grant with Scopes; false sends the client access_denied.
	Approve bool `json:"approve"`
	// Scopes are the ones the user ticked: a non-empty subset of OAuthConsent.scopes when
	// approving, ignored when denying.
	Scopes []string `json:"scopes,omitempty"`
}

// OAuthConsentResult answers POST /api/v1/oauth/consents/{id}: the address the SPA sends the
// browser to with location.assign. It carries code, state and iss, or error=access_denied,
// state and iss.
type OAuthConsentResult struct {
	Redirect string `json:"redirect" dash:"required"`
}

// OAuthGrantInfo is one grant as GET /api/v1/oauth/grants lists it: a user's consent to one
// client, which its refresh token keeps alive. No secret is part of it.
type OAuthGrantInfo struct {
	ID       string      `json:"id" dash:"required"`
	User     string      `json:"user" dash:"required"`
	Client   OAuthClient `json:"client" dash:"required"`
	Audience string      `json:"audience" dash:"required,enum=api|mcp"`
	Scopes   []string    `json:"scopes" dash:"required"`
	// CreatedAt is the consent; ExpiresAt the absolute end (180 days later), however often the
	// client refreshes. A grant unused for 30 days ends earlier.
	CreatedAt time.Time `json:"createdAt" dash:"required"`
	ExpiresAt time.Time `json:"expiresAt" dash:"required"`
	// LastUsedAt is when the client last used the grant: a refresh, or a request with one of
	// its access tokens (to within five minutes); null when it never did.
	LastUsedAt *time.Time `json:"lastUsedAt" dash:"required,nullable" tstype:"string | null,required"`
}

// OAuthGrantList is GET /api/v1/oauth/grants: the caller's grants, or with ?user=
// (administrators) one user's or, for ?user=*, everyone's.
type OAuthGrantList struct {
	Grants []OAuthGrantInfo `json:"grants" dash:"required"`
}
