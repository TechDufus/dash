package model

import "time"

// Token audiences (design M.4): a token is bound to one surface, and using it at the other one
// is 401 invalid_token.
const (
	TokenAudienceAPI = "api" // /api/v1
	TokenAudienceMCP = "mcp" // /mcp
)

// Token scopes a personal access token or a static token may hold (design M.3, M.5): read,
// logs and actions:<plugin>, the actions scopes only for an administrator. approvals and push
// are never put on one.
const (
	TokenScopeRead = "read"
	TokenScopeLogs = "logs"
)

// TokenScopeActionsPrefix starts the scope actions:<plugin>, which runs that plugin's actions.
const TokenScopeActionsPrefix = "actions:"

// TokenExpiryDefaultDays is the expiry of a personal access token created without one (design
// M.5); expiry is required.
const TokenExpiryDefaultDays = 30

// TokenExpiryMaxDays is the longest expiry a personal access token may have.
const TokenExpiryMaxDays = 365

// TokenNameMaxLen is the longest name of a personal access token, in characters.
const TokenNameMaxLen = 64

// TokenInfo is one personal access token as GET /api/v1/tokens lists it. The secret is never
// part of it: dash keeps only its SHA-256.
type TokenInfo struct {
	ID       string `json:"id" dash:"required"`                    // the 10-character ID inside dash_pat_<id>_<secret>
	Name     string `json:"name" dash:"required"`                  // 1 to 64 characters, chosen by its owner
	User     string `json:"user" dash:"required"`                  // the owner
	Audience string `json:"audience" dash:"required,enum=api|mcp"` // api or mcp
	// Scopes are read, logs and actions:<plugin>.
	Scopes []string `json:"scopes" dash:"required"`
	// ActionNamespaces restricts the namespaces the token's actions may touch: exact names or
	// prefix* globs. Empty means no restriction beyond the plugin's own.
	ActionNamespaces []string  `json:"actionNamespaces" dash:"required"`
	CreatedAt        time.Time `json:"createdAt" dash:"required"`
	ExpiresAt        time.Time `json:"expiresAt" dash:"required"`
	// LastUsedAt is when a request last authenticated with the token, to within five minutes;
	// null when it was never used.
	LastUsedAt *time.Time `json:"lastUsedAt" dash:"required,nullable" tstype:"string | null,required"`
}

// TokenList is GET /api/v1/tokens: the caller's tokens, or with ?user= (administrators) one
// user's or, for ?user=*, everyone's.
type TokenList struct {
	Tokens []TokenInfo `json:"tokens" dash:"required"`
}

// TokenCreateRequest is the body of POST /api/v1/tokens.
type TokenCreateRequest struct {
	// Name is 1 to 64 characters, at least one of them visible.
	Name     string `json:"name" dash:"required,min=1,max=64"`
	Audience string `json:"audience" dash:"required,enum=api|mcp"`
	// Scopes are read, logs and actions:<plugin> (administrators only, for a plugin whose actions
	// are enabled); approvals and push are refused (403).
	Scopes []string `json:"scopes" dash:"required"`
	// ActionNamespaces are exact namespace names or prefix* globs; optional, and only with an
	// actions:<plugin> scope.
	ActionNamespaces []string `json:"actionNamespaces,omitempty"`
	// ExpiresInDays is 1 to 365; absent means 30.
	ExpiresInDays *int `json:"expiresInDays,omitempty" dash:"min=1,max=365"`
}

// TokenCreated answers a successful POST /api/v1/tokens (201). Token is the secret, shown this
// once: dash keeps only its hash and can never show it again.
type TokenCreated struct {
	Token string    `json:"token" dash:"required"` // dash_pat_<id>_<secret>
	Info  TokenInfo `json:"info" dash:"required"`
}
