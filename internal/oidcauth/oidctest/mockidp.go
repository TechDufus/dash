// Package oidctest is a mock OpenID Connect provider for the OIDC login's tests (contract §4.4,
// P4.4): an httptest.Server with discovery, a JWKS, a token endpoint that enforces PKCE S256 and
// the client's credentials, and a UserInfo endpoint that only counts calls (the login must make
// none). It signs RS256 ID tokens whose claims the test shapes. It lives outside the _test files
// so the API's route tests and the package's own tests share one IdP.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Paths of the endpoints the discovery document names.
const (
	PathDiscovery = "/.well-known/openid-configuration"
	PathAuthorize = "/authorize"
	PathToken     = "/token"
	PathJWKS      = "/jwks"
	PathUserInfo  = "/userinfo"
)

// Delete is a claim value that removes the claim from the minted ID token.
const Delete = deleteClaim("delete")

type deleteClaim string

// keyID names the signing key in the JWKS and the token header.
const keyID = "test-key"

var (
	keysOnce         sync.Once
	signing, foreign *rsa.PrivateKey
	errKeyGeneration error
)

func keys(t testing.TB) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keysOnce.Do(func() {
		if signing, errKeyGeneration = rsa.GenerateKey(rand.Reader, 2048); errKeyGeneration == nil {
			foreign, errKeyGeneration = rsa.GenerateKey(rand.Reader, 2048)
		}
	})
	if errKeyGeneration != nil {
		t.Fatal(errKeyGeneration)
	}
	return signing, foreign
}

// Grant is one authorization the test lets the IdP give: Claims are merged over the defaults
// (iss, aud, sub, iat, exp = now+5m, nonce from the authorization request); a Delete value drops
// a claim. ForeignKey signs the ID token with a key the JWKS does not hold.
type Grant struct {
	Claims     map[string]any
	ForeignKey bool
}

// IdP is the mock provider. Its methods are safe for concurrent use.
type IdP struct {
	Server       *httptest.Server
	ClientID     string
	ClientSecret string
	RedirectURL  string

	key, foreign *rsa.PrivateKey
	now          func() time.Time

	mu       sync.Mutex
	codes    map[string]pending
	hits     map[string]int
	tokenReq url.Values
	down     bool
	echo     bool
}

type pending struct {
	grant     Grant
	nonce     string
	challenge string
	redirect  string
}

// New starts a provider for one client; now is the clock of the iat and exp it mints (nil =
// time.Now). The server stops at the end of the test.
func New(t testing.TB, clientID, clientSecret, redirectURL string, now func() time.Time) *IdP {
	t.Helper()
	if now == nil {
		now = time.Now
	}
	key, foreign := keys(t)
	i := &IdP{
		ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
		key: key, foreign: foreign, now: now,
		codes: map[string]pending{}, hits: map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathDiscovery, i.discovery)
	mux.HandleFunc("GET "+PathJWKS, i.jwks)
	mux.HandleFunc("POST "+PathToken, i.token)
	mux.HandleFunc(PathUserInfo, func(w http.ResponseWriter, _ *http.Request) {
		i.hit(PathUserInfo)
		http.Error(w, "the login must not call UserInfo", http.StatusTeapot)
	})
	i.Server = httptest.NewServer(mux)
	t.Cleanup(i.Server.Close)
	return i
}

// Issuer is the provider's issuer URL.
func (i *IdP) Issuer() string { return i.Server.URL }

// SetDown makes discovery answer 503 (true) or work again (false).
func (i *IdP) SetDown(down bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.down = down
}

// SetEchoOnTokenError makes a refused token request answer a body that echoes the request
// (the client secret and the code included), to prove the login never logs that body.
func (i *IdP) SetEchoOnTokenError(echo bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.echo = echo
}

// Hits is the number of requests path has served.
func (i *IdP) Hits(path string) int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.hits[path]
}

// TokenRequest is the form of the last token request (nil before the first).
func (i *IdP) TokenRequest() url.Values {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.tokenReq
}

// Authorize plays the user's visit to the authorization URL authURL (what /auth/oidc/start
// redirected to): it checks the request (code flow, this client, the redirect URL, an S256
// challenge, a state and a nonce) and returns the code and the state the IdP sends back to the
// callback. The token endpoint mints grant for that code, once.
func (i *IdP) Authorize(t testing.TB, authURL string, grant Grant) (code, state string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("authorization URL %q: %v", authURL, err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != i.Server.URL+PathAuthorize {
		t.Fatalf("authorization URL %s, want %s", got, i.Server.URL+PathAuthorize)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": i.ClientID, "redirect_uri": i.RedirectURL, "code_challenge_method": "S256",
	} {
		if got := q.Get(k); got != want {
			t.Fatalf("authorization request %s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if q.Get(k) == "" {
			t.Fatalf("authorization request without %s", k)
		}
	}
	if !strings.Contains(" "+q.Get("scope")+" ", " openid ") {
		t.Fatalf("scope %q lacks openid", q.Get("scope"))
	}
	code = rand.Text()
	i.mu.Lock()
	i.codes[code] = pending{grant: grant, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	i.mu.Unlock()
	return code, q.Get("state")
}

func (i *IdP) hit(path string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.hits[path]++
}

func (i *IdP) discovery(w http.ResponseWriter, _ *http.Request) {
	i.hit(PathDiscovery)
	i.mu.Lock()
	down := i.down
	i.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                i.Issuer(),
		"authorization_endpoint":                i.Issuer() + PathAuthorize,
		"token_endpoint":                        i.Issuer() + PathToken,
		"jwks_uri":                              i.Issuer() + PathJWKS,
		"userinfo_endpoint":                     i.Issuer() + PathUserInfo,
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (i *IdP) jwks(w http.ResponseWriter, _ *http.Request) {
	i.hit(PathJWKS)
	pub := i.key.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": keyID,
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// token is the token endpoint: authorization_code with this client's credentials (Basic or in
// the form), the redirect URL of the authorization, and a code_verifier whose S256 is the
// challenge. A code works once.
func (i *IdP) token(w http.ResponseWriter, r *http.Request) {
	i.hit(PathToken)
	if err := r.ParseForm(); err != nil {
		i.refuse(w, r, "invalid_request")
		return
	}
	i.mu.Lock()
	i.tokenReq = r.PostForm
	p, ok := i.codes[r.PostForm.Get("code")]
	delete(i.codes, r.PostForm.Get("code"))
	i.mu.Unlock()

	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case id != i.ClientID || secret != i.ClientSecret:
		i.refuse(w, r, "invalid_client")
	case r.PostForm.Get("grant_type") != "authorization_code" || !ok:
		i.refuse(w, r, "invalid_grant")
	case r.PostForm.Get("redirect_uri") != p.redirect:
		i.refuse(w, r, "invalid_grant")
	case b64(sum[:]) != p.challenge:
		i.refuse(w, r, "invalid_grant")
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "at-" + rand.Text(), "token_type": "Bearer", "expires_in": 300,
			"id_token": i.mint(p),
		})
	}
}

func (i *IdP) refuse(w http.ResponseWriter, r *http.Request, code string) {
	i.mu.Lock()
	echo := i.echo
	i.mu.Unlock()
	body := map[string]any{"error": code}
	if echo {
		id, secret, _ := r.BasicAuth()
		body["error_description"] = "you sent " + r.PostForm.Encode() + " as " + id + ":" + secret
		if s, err := url.QueryUnescape(secret); err == nil {
			body["error_description"] = body["error_description"].(string) + " (" + s + ")"
		}
	}
	status := http.StatusBadRequest
	if code == "invalid_client" {
		status = http.StatusUnauthorized
	}
	writeJSON(w, status, body)
}

// mint signs the ID token of p.
func (i *IdP) mint(p pending) string {
	now := i.now()
	claims := map[string]any{
		"iss": i.Issuer(), "aud": i.ClientID, "sub": "subject-1",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": p.nonce,
	}
	for k, v := range p.grant.Claims {
		if _, del := v.(deleteClaim); del {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	key := i.key
	if p.grant.ForeignKey {
		key = i.foreign
	}
	return sign(key, claims)
}

// sign is the RS256 compact JWS of claims under key, with this IdP's key ID.
func sign(key *rsa.PrivateKey, claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": keyID})
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	input := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return input + "." + b64(sig)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
