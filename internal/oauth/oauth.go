// Package oauth is dash's own OAuth 2.1 authorization server (design M.6, contract G7-R3): the
// metadata documents (RFC 8414, RFC 9728), the authorization code flow with PKCE S256 and
// resource indicators (RFC 8707), refresh token rotation, revocation (RFC 7009) and the sealed
// access tokens the bearer step opens. It knows no HTTP routing: internal/api serves its
// endpoints, audits the writes and applies CORS and the rate limits.
//
// Clients are public (no client secret): the built-in dash-android, client ID metadata
// documents fetched through internal/netguard, and auth.oauth.clients. Pending authorization
// requests (10 minutes) and codes (60 seconds, single use) live in memory; grants live in the
// credential store.
package oauth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/netguard"
	"github.com/TechDufus/dash/internal/session"
)

// Lifetimes and bounds (design M.6).
const (
	PendingTTL = 10 * time.Minute    // an authorization request waiting for consent
	CodeTTL    = credentials.CodeTTL // an authorization code; the store prunes grants never exchanged past it
	AccessTTL  = 15 * time.Minute    // an access token

	// maxPending and maxCodes bound what dash holds for requests: only signed-in people create
	// pending requests (at most maxPendingPerUser each, the oldest goes), and codes come from
	// their consents.
	maxPending        = 1024
	maxPendingPerUser = 20
	maxCodes          = 1024
)

// AccessPurpose is the HKDF purpose of the access-token key, derived with the credential
// store's salt like the session key (session.DeriveKey).
const AccessPurpose = "dash/oauth-access/v1"

// AccessPrefix starts every access token: dash_at_<raw base64url>, with no dot, so the
// scrubber's token class covers the whole token (contract D7).
const AccessPrefix = "dash_at_"

// AndroidRedirect is the redirect URI of the built-in dash-android client.
const AndroidRedirect = "io.github.techdufus.dash:/oauth2redirect"

// Options configure a Server.
type Options struct {
	// PublicURL is server.publicURL; /oauth/authorize lives there, behind an identity proxy if
	// there is one. DirectURL is server.directURL ("" = none): when set it is the issuer and the
	// base of the token, revoke and metadata endpoints and of the resources (contract D20).
	PublicURL, DirectURL string
	// Store keeps the grants; it must outlive the process (Persistent).
	Store *credentials.Store
	// AccessKey seals the access tokens: 32 bytes, derived from DASH_SECRET_KEY and the store's
	// salt, or random for this process.
	AccessKey []byte
	// Clients are auth.oauth.clients.
	Clients []config.OAuthClient
	// CIMDAllowPrivate lets a client ID metadata document come from a private address.
	CIMDAllowPrivate bool
	// HTTPClient fetches client ID metadata documents; nil builds the netguard client. Tests
	// set one that reaches a local server.
	HTTPClient *http.Client
	Now        func() time.Time
}

// Server is the authorization server. Its methods are safe for concurrent use.
type Server struct {
	publicURL, base string
	store           *credentials.Store
	codec           *session.Codec
	clients         map[string]config.OAuthClient
	cimd            *cimdCache
	now             func() time.Time

	mu      sync.Mutex
	pending map[string]*pending
	codes   map[string]*code
	// expired are the grants of codes that expired unexchanged (sweepLocked), revoked by
	// revokeExpired once s.mu is released, so no file write happens under it.
	expired []string

	// onExpire learns of each grant revokeExpired removed; set before serving.
	onExpire func(credentials.Grant)

	// mcpOn reports whether the MCP server is on now (SetMCPAvailable); nil is off.
	mcpOn func() bool
}

// OnGrantExpired sets f to learn of every grant revoked because its authorization code expired
// before it was exchanged (the API audits it as oauth.grant.expire). Call it before serving.
func (s *Server) OnGrantExpired(f func(credentials.Grant)) { s.onExpire = f }

// SetMCPAvailable sets f to report, per request, whether this dash serves the MCP server: the API
// server's mcp.enabled, false in auth.mode none. Call it before serving.
func (s *Server) SetMCPAvailable(f func() bool) { s.mcpOn = f }

// New builds a server.
func New(o Options) (*Server, error) {
	if o.Store == nil {
		return nil, errors.New("oauth: a credential store is required")
	}
	codec, err := session.NewCodec(o.AccessKey)
	if err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = netguard.NewClient(netguard.Options{AllowPrivate: o.CIMDAllowPrivate, Timeout: cimdTimeout})
	}
	public := strings.TrimRight(o.PublicURL, "/")
	s := &Server{
		publicURL: public, base: public, store: o.Store, codec: codec, now: o.Now,
		clients: map[string]config.OAuthClient{}, cimd: newCIMDCache(hc, o.Now),
		pending: map[string]*pending{}, codes: map[string]*code{},
	}
	if o.DirectURL != "" {
		s.base = strings.TrimRight(o.DirectURL, "/")
	}
	s.cimd.reserve(android.ID, android.Name)
	for _, c := range o.Clients {
		s.clients[c.ClientID] = c
		s.cimd.reserve(c.ClientID, c.Name)
	}
	return s, nil
}

// Issuer is the issuer identifier: server.directURL when set, else server.publicURL.
func (s *Server) Issuer() string { return s.base }

// Resource is the resource identifier of audience (api or mcp): <issuer>/api or <issuer>/mcp.
func (s *Server) Resource(audience string) string { return s.base + "/" + audience }

// ResourceMetadataURL is the RFC 9728 document of audience, which the 401 challenge names.
func (s *Server) ResourceMetadataURL(audience string) string {
	return s.base + "/.well-known/oauth-protected-resource/" + audience
}

// mcpAvailable reports whether this dash serves the MCP server now, and so issues tokens for the
// mcp audience (contract G7-R3 fix A3). It is the one place that decides, live: while MCP is off
// a request for <issuer>/mcp is invalid_target and the mcp metadata is the JSON 404.
func (s *Server) mcpAvailable() bool { return s.mcpOn != nil && s.mcpOn() }

// Serves reports whether audience is one this dash issues tokens for now: api, and mcp while the
// MCP server is on. The protected resource metadata of any other is the JSON 404.
func (s *Server) Serves(audience string) bool {
	return audience == model.TokenAudienceAPI || (audience == model.TokenAudienceMCP && s.mcpAvailable())
}

// resourcesText names the resources a client may ask for, for an error description.
func (s *Server) resourcesText() string {
	if s.mcpAvailable() {
		return s.Resource(model.TokenAudienceAPI) + " or " + s.Resource(model.TokenAudienceMCP)
	}
	return s.Resource(model.TokenAudienceAPI)
}

// audienceOf maps a resource parameter to its audience, "" for none of dash's. A trailing slash
// is accepted (clients differ in how they spell a resource without a path).
func (s *Server) audienceOf(resource string) string {
	switch strings.TrimSuffix(resource, "/") {
	case s.Resource(model.TokenAudienceAPI):
		return model.TokenAudienceAPI
	case s.Resource(model.TokenAudienceMCP):
		return model.TokenAudienceMCP
	}
	return ""
}

// ScopesSupported are the scopes of audience: read, logs, actions:<id> for each plugin with
// actions enabled (actions) and, for the api audience, approvals and push, which only
// dash-android may be granted.
func ScopesSupported(audience string, actions []string) []string {
	out := []string{model.TokenScopeRead, model.TokenScopeLogs}
	for _, id := range actions {
		out = append(out, model.TokenScopeActionsPrefix+id)
	}
	if audience == model.TokenAudienceAPI {
		out = append(out, model.OAuthScopeApprovals, model.OAuthScopePush)
	}
	return out
}

// AuthorizationServerMetadata is the RFC 8414 document at
// /.well-known/oauth-authorization-server.
type AuthorizationServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RevocationEndpoint                         string   `json:"revocation_endpoint"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethodsSupported     []string `json:"revocation_endpoint_auth_methods_supported"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

// Metadata is the authorization server's RFC 8414 document; actions are the plugins with
// actions enabled.
func (s *Server) Metadata(actions []string) AuthorizationServerMetadata {
	return AuthorizationServerMetadata{
		Issuer:                                     s.base,
		AuthorizationEndpoint:                      s.publicURL + "/oauth/authorize",
		TokenEndpoint:                              s.base + "/oauth/token",
		RevocationEndpoint:                         s.base + "/oauth/revoke",
		ResponseTypesSupported:                     []string{"code"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:              []string{"S256"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		RevocationEndpointAuthMethodsSupported:     []string{"none"},
		ScopesSupported:                            ScopesSupported(model.TokenAudienceAPI, actions),
		ClientIDMetadataDocumentSupported:          true,
		AuthorizationResponseIssParameterSupported: true,
	}
}

// ProtectedResourceMetadata is the RFC 9728 document of one resource.
type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name"`
}

// ResourceMetadata is the RFC 9728 document of audience (api or mcp).
func (s *Server) ResourceMetadata(audience string, actions []string) ProtectedResourceMetadata {
	name := "dash API"
	if audience == model.TokenAudienceMCP {
		name = "dash MCP server"
	}
	return ProtectedResourceMetadata{
		Resource: s.Resource(audience), AuthorizationServers: []string{s.base},
		ScopesSupported: ScopesSupported(audience, actions), BearerMethodsSupported: []string{"header"},
		ResourceName: name,
	}
}

// Error is an OAuth error answer: Code is the RFC 6749 error code, Description text for a
// developer that never repeats a secret.
type Error struct {
	Code        string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

func (e *Error) Error() string { return "oauth: " + e.Code + ": " + e.Description }

func oauthErr(code, desc string) *Error { return &Error{Code: code, Description: desc} }

// randomID is 32 random bytes in base64url: a consent ID or a code.
func randomID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// withParams adds params to the query of uri (a redirect URI the client registered, which may
// carry a query of its own).
func withParams(uri string, params url.Values) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			if v != "" {
				q.Add(k, v)
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// single returns the one value of key in v; dup reports that key came more than once, which
// OAuth refuses (RFC 6749 3.1).
func single(v url.Values, key string) (val string, dup bool) {
	vs := v[key]
	if len(vs) > 1 {
		return "", true
	}
	if len(vs) == 1 {
		return vs[0], false
	}
	return "", false
}

// duplicated is the first of keys given more than once in v, "" when none is.
func duplicated(v url.Values, keys ...string) string {
	i := slices.IndexFunc(keys, func(k string) bool { _, dup := single(v, k); return dup })
	if i < 0 {
		return ""
	}
	return keys[i]
}
