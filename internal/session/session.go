// Package session is the stateless session cookie of the local and oidc modes (contract D8): an
// XChaCha20-Poly1305 sealed payload whose key is derived from DASH_SECRET_KEY and the session salt
// of credentials.yaml, the cookie names and attributes, the idle and absolute limits, the epoch
// checks, the authenticator of the session modes (session step, then the anonymous viewer), and
// the validation of a post-login "next" path.
//
// The payload never holds a role (design M.8): the role is mapped on every request from the
// user, the groups and the role-claim values, so a change of the admin lists applies on the next
// request.
//
// Scaffold (G4-R2-S13): the signatures are final; the bodies are the credentials+session
// slice's (S10).
package session

import (
	"errors"
	"time"
)

// Limits and names (design D8).
const (
	// IdleTimeout is the sliding idle limit: a request in its second half re-issues the cookie.
	IdleTimeout = 12 * time.Hour
	// MaxLifetime is the absolute limit, counted from the login (iat).
	MaxLifetime = 7 * 24 * time.Hour
	// MaxSealedBytes is the largest sealed value Seal returns (browsers drop cookies over ~4 KiB).
	MaxSealedBytes = 3500

	// CookieSecure is the session cookie's name when server.publicURL is https; CookiePlain on http.
	CookieSecure = "__Host-dash_session"
	CookiePlain  = "dash_session"

	// PurposeSession and PurposeOIDCState are the HKDF info prefixes of the two sealed cookies.
	// The phase 7 OAuth server uses "dash/oauth-access/v1", never these.
	PurposeSession   = "dash/session/v1"
	PurposeOIDCState = "dash/oidc-state/v1"

	// KeySize is the length of a codec key.
	KeySize = 32
)

// Errors of the codec and the manager.
var (
	// ErrTooLarge means the sealed value would exceed MaxSealedBytes: the login is refused with
	// a clear error rather than setting a cookie the browser would drop.
	ErrTooLarge = errors.New("session: the sealed cookie would be larger than 3500 bytes")
	// ErrInvalid means a sealed value that is malformed, sealed with another key or tampered with.
	ErrInvalid = errors.New("session: invalid cookie")
	// ErrNoSession means the request carries no session cookie.
	ErrNoSession = errors.New("session: no cookie")
	// ErrExpired means a session past its idle or absolute limit.
	ErrExpired = errors.New("session: expired")
	// ErrRevoked means a session whose user epoch or global epoch is not the current one.
	ErrRevoked = errors.New("session: revoked")
)

// errSkeleton is what the scaffold's bodies return until the slice implements them.
var errSkeleton = errors.New("session: not implemented in the scaffold")

// Payload is the sealed content of a session cookie. It has no role (design M.8). Groups and
// RoleClaim hold only what the mapping can use (Keep), so the cookie stays small.
type Payload struct {
	User        string   `json:"u"`
	Groups      []string `json:"g,omitempty"`
	RoleClaim   []string `json:"rc,omitempty"`
	IssuedAt    int64    `json:"iat"` // unix seconds of the login
	ExpiresAt   int64    `json:"exp"` // unix seconds: min(last renewal+IdleTimeout, iat+MaxLifetime)
	UserEpoch   uint64   `json:"ep"`
	GlobalEpoch uint64   `json:"ge"`
}

// KeySource derives purpose keys from the operator's DASH_SECRET_KEY; *secret.Keyring implements
// it (HKDF-SHA256, the previous key never used).
type KeySource interface {
	Derive(info string) ([]byte, error)
}

// DeriveKey is the key of purpose (PurposeSession, PurposeOIDCState) for the store's salt:
// src.Derive with the info "<purpose>|<base64url salt>". Without a key (src.Derive fails with
// secret.ErrNoKey) it fails: the caller then uses RandomKey, and sessions end with the process.
func DeriveKey(src KeySource, purpose string, salt []byte) ([]byte, error) {
	return nil, errSkeleton
}

// RandomKey is a per-process key: used when there is no DASH_SECRET_KEY or no persistent
// credential store (epochs and salt could not survive a restart, so neither may the sessions).
func RandomKey() ([]byte, error) { return nil, errSkeleton }

// Codec seals and opens values with one key: JSON, XChaCha20-Poly1305 with a random nonce,
// base64url without padding behind a "v1." version prefix.
type Codec struct{}

// NewCodec returns the codec of key, which must be KeySize bytes.
func NewCodec(key []byte) (*Codec, error) { return nil, errSkeleton }

// Seal encodes v as JSON and seals it. ErrTooLarge when the result exceeds MaxSealedBytes.
func (c *Codec) Seal(v any) (string, error) { return "", errSkeleton }

// Open opens sealed into v. ErrInvalid for anything this codec did not seal, unchanged.
func (c *Codec) Open(sealed string, v any) error { return errSkeleton }
