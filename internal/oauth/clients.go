package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/netguard"
)

// The client ID metadata document fetch (design M.6).
const (
	cimdTimeout  = 5 * time.Second
	cimdMaxBytes = 64 << 10
	cimdTTL      = 24 * time.Hour
	cimdMaxCache = 256 // documents kept; the oldest goes first
	cimdFetches  = 4   // fetches at once
)

// Client is an OAuth client dash knows: public, identified by its ID.
type Client struct {
	ID, Name, Kind string
	RedirectURIs   []string
	Audiences      []string // api, mcp or both
}

// info is the wire form of the client.
func (c *Client) info() model.OAuthClient {
	return model.OAuthClient{ID: c.ID, Name: c.Name, Kind: c.Kind}
}

// android is the built-in first-party app: the api audience only, and the only client that may
// be granted approvals and push.
var android = Client{
	ID: model.OAuthClientAndroid, Name: "dash for Android", Kind: model.OAuthClientKindBuiltin,
	RedirectURIs: []string{AndroidRedirect}, Audiences: []string{model.TokenAudienceAPI},
}

// errUnknownClient is a client_id dash cannot use; its text is shown to the person in the
// browser, never sent to a redirect URI that was not verified.
var errUnknownClient = errors.New("unknown client")

// errNotFetched is a client ID metadata document dash would have to fetch for a caller who may
// not make it fetch (an anonymous one): the request cannot be checked before sign-in.
var errNotFetched = errors.New("the client's metadata document is not fetched for an anonymous caller")

// client resolves a client_id: dash-android, an auth.oauth.clients entry, or the https URL of a
// client ID metadata document, fetched only when fetch is set (errNotFetched otherwise). An
// http URL is refused with the reason, so its developer learns what to change.
func (s *Server) client(ctx context.Context, id string, fetch bool) (*Client, error) {
	if id == model.OAuthClientAndroid {
		c := android
		return &c, nil
	}
	if c, ok := s.clients[id]; ok {
		return &Client{ID: c.ClientID, Name: c.Name, Kind: model.OAuthClientKindConfig,
			RedirectURIs: slices.Clone(c.RedirectURIs), Audiences: slices.Clone(c.Audiences)}, nil
	}
	switch {
	case strings.HasPrefix(id, "https://"):
		return s.cimd.get(ctx, id, fetch)
	case len(id) >= len("http://") && strings.EqualFold(id[:len("http://")], "http://"):
		return nil, fmt.Errorf("%w: client metadata URLs must be https", errUnknownClient)
	}
	return nil, errUnknownClient
}

// knownClient reports whether id is a client dash knows without fetching anything: dash-android,
// an auth.oauth.clients entry, or a metadata document in the cache.
func (s *Server) knownClient(id string) bool {
	if _, ok := s.clients[id]; ok || id == model.OAuthClientAndroid {
		return true
	}
	return s.cimd.cached(id)
}

// ClientInfo is the wire form of a stored grant's client; a client gone from the config keeps the
// name the grant recorded.
func ClientInfo(id, name, kind string) model.OAuthClient {
	return model.OAuthClient{ID: id, Name: name, Kind: kind}
}

// metadataURLProblem is why id cannot be a client ID metadata document URL, or "": https, a
// host, a path, and no fragment, user information or dot segment (the CIMD draft, 3).
func metadataURLProblem(id string) string {
	u, err := url.Parse(id)
	switch {
	case err != nil || u.Scheme != "https" || u.Hostname() == "":
		return "is not an https URL"
	case u.User != nil || u.Fragment != "" || strings.Contains(id, "#"):
		return "has user information or a fragment"
	case u.Path == "" || u.Path == "/":
		return "has no path"
	case slices.ContainsFunc(strings.Split(u.Path, "/"), func(seg string) bool { return seg == "." || seg == ".." }):
		return "has a dot segment"
	case len(id) > 2048:
		return "is longer than 2048 bytes"
	}
	return ""
}

// cimdDocument is the part of a client ID metadata document dash reads.
type cimdDocument struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type cimdEntry struct {
	client  Client
	fetched time.Time
}

// cimdCache fetches client ID metadata documents through the guarded client and keeps the
// valid ones for cimdTTL.
type cimdCache struct {
	http     *http.Client
	now      func() time.Time
	slots    chan struct{}
	reserved map[string]bool // first-party names and IDs, folded (nameKey); set before use

	mu      sync.Mutex
	entries map[string]cimdEntry
}

func newCIMDCache(hc *http.Client, now func() time.Time) *cimdCache {
	return &cimdCache{http: hc, now: now, slots: make(chan struct{}, cimdFetches), reserved: map[string]bool{}, entries: map[string]cimdEntry{}}
}

// reserve keeps names (a built-in or config client's ID and name) from metadata documents: a
// document whose client_name is one of them is refused, so it cannot pass for a first-party app
// on the consent screen (contract G7-R3 fix A4).
func (c *cimdCache) reserve(names ...string) {
	for _, n := range names {
		if k := nameKey(n); k != "" {
			c.reserved[k] = true
		}
	}
}

// nameKey is how client names are compared: trimmed and case-folded.
func nameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// cached reports whether a valid document of id is in the cache.
func (c *cimdCache) cached(id string) bool {
	c.mu.Lock()
	e, ok := c.entries[id]
	c.mu.Unlock()
	return ok && c.now().Sub(e.fetched) < cimdTTL
}

// get returns the client of metadata document id from the cache, else fetches it when fetch is
// set, else errNotFetched.
func (c *cimdCache) get(ctx context.Context, id string, fetch bool) (*Client, error) {
	if problem := metadataURLProblem(id); problem != "" {
		return nil, fmt.Errorf("%w: the client ID %s", errUnknownClient, problem)
	}
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[id]
	c.mu.Unlock()
	if ok && now.Sub(e.fetched) < cimdTTL {
		cl := e.client
		return &cl, nil
	}
	if !fetch {
		return nil, errNotFetched
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	cl, err := c.fetch(ctx, id)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= cimdMaxCache {
		oldest := ""
		for k, v := range c.entries {
			if oldest == "" || v.fetched.Before(c.entries[oldest].fetched) {
				oldest = k
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[id] = cimdEntry{client: *cl, fetched: now}
	return cl, nil
}

// fetch reads and checks one document: 5 seconds, 64 KiB, no redirect, a global address unless
// auth.oauth.cimdAllowPrivate (the guarded client's rules), client_id equal to its URL, a
// client_name, and redirect URIs by the rule of auth.oauth.clients.
func (c *cimdCache) fetch(ctx context.Context, id string) (*Client, error) {
	ctx, cancel := context.WithTimeout(ctx, cimdTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUnknownClient, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, netguard.ErrRefused):
			return nil, fmt.Errorf("%w: its metadata document is on an address dash does not fetch from (auth.oauth.cimdAllowPrivate)", errUnknownClient)
		case errors.Is(err, netguard.ErrRedirect):
			return nil, fmt.Errorf("%w: its metadata document redirects, which dash does not follow", errUnknownClient)
		}
		return nil, fmt.Errorf("%w: its metadata document could not be fetched", errUnknownClient)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: its metadata document answered %d", errUnknownClient, resp.StatusCode)
	}
	body, err := netguard.ReadBody(resp.Body, cimdMaxBytes)
	if err != nil {
		if errors.Is(err, netguard.ErrTooLarge) {
			return nil, fmt.Errorf("%w: its metadata document is larger than 64 KiB", errUnknownClient)
		}
		return nil, fmt.Errorf("%w: its metadata document could not be read", errUnknownClient)
	}
	var doc cimdDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: its metadata document is not JSON", errUnknownClient)
	}
	name := strings.TrimSpace(doc.ClientName)
	switch {
	case doc.ClientID != id:
		return nil, fmt.Errorf("%w: its metadata document names another client_id", errUnknownClient)
	case name == "" || !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }):
		return nil, fmt.Errorf("%w: its metadata document has no usable client_name", errUnknownClient)
	case len(doc.RedirectURIs) == 0:
		return nil, fmt.Errorf("%w: its metadata document has no redirect_uris", errUnknownClient)
	case c.reserved[nameKey(name)]:
		return nil, fmt.Errorf("%w: its metadata document's client_name is the name of an app this dash knows", errUnknownClient)
	case doc.TokenEndpointAuthMethod != "" && doc.TokenEndpointAuthMethod != "none":
		return nil, fmt.Errorf("%w: dash serves only public clients (token_endpoint_auth_method none)", errUnknownClient)
	}
	for _, r := range doc.RedirectURIs {
		if config.RedirectURIProblem(r) != "" {
			return nil, fmt.Errorf("%w: its metadata document lists a redirect URI dash does not accept", errUnknownClient)
		}
	}
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
	}
	return &Client{
		ID: id, Name: name, Kind: model.OAuthClientKindMetadata, RedirectURIs: slices.Clone(doc.RedirectURIs),
		Audiences: []string{model.TokenAudienceAPI, model.TokenAudienceMCP},
	}, nil
}

// redirectAllowed reports whether uri is one the client registered: exactly, or for an http
// loopback redirect the same host, path and query on any port (RFC 8252 7.3).
func (c *Client) redirectAllowed(uri string) bool {
	if config.RedirectURIProblem(uri) != "" {
		return false
	}
	if slices.Contains(c.RedirectURIs, uri) {
		return true
	}
	got, err := url.Parse(uri)
	if err != nil || !config.LoopbackRedirect(got) {
		return false
	}
	for _, r := range c.RedirectURIs {
		reg, err := url.Parse(r)
		if err == nil && config.LoopbackRedirect(reg) && reg.Hostname() == got.Hostname() &&
			reg.EscapedPath() == got.EscapedPath() && reg.RawQuery == got.RawQuery {
			return true
		}
	}
	return false
}
