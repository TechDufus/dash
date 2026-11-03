// Package session is the stateless session cookie of the local and oidc modes (contract D8): an
// XChaCha20-Poly1305 sealed payload whose key is derived from DASH_SECRET_KEY and the session salt
// of credentials.yaml, the cookie names and attributes, the idle and absolute limits, the epoch
// checks, the authenticator of the session modes (session step, then the anonymous viewer), and
// the validation of a post-login "next" path.
//
// The payload never holds a role (design M.8): the role is mapped on every request from the
// user, the groups and the role-claim values, so a change of the admin lists applies on the next
// request.
package session

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
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
	// cmd/dash derives the session key for PurposeSession+"/"+auth.mode, so a session of the
	// local mode never opens in the oidc mode or the other way round.
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
	if src == nil {
		return nil, errors.New("session: no key source")
	}
	if purpose == "" || len(salt) == 0 {
		return nil, errors.New("session: a derived key needs a purpose and a salt")
	}
	key, err := src.Derive(purpose + "|" + base64.RawURLEncoding.EncodeToString(salt))
	if err != nil {
		return nil, err
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("session: derived key is %d bytes, want %d", len(key), KeySize)
	}
	return key, nil
}

// RandomKey is a per-process key: used when there is no DASH_SECRET_KEY or no persistent
// credential store (epochs and salt could not survive a restart, so neither may the sessions).
func RandomKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("session: random key: %w", err)
	}
	return key, nil
}

// sealedPrefix versions the sealed format; codecAD binds it as additional data.
const sealedPrefix = "v1."

var codecAD = []byte("dash/codec/v1")

// sealedEncoding is base64url without padding; Strict refuses non-canonical trailing bits, so
// one sealed value has one spelling.
var sealedEncoding = base64.RawURLEncoding.Strict()

// Codec seals and opens values with one key: JSON, XChaCha20-Poly1305 with a random nonce,
// base64url without padding behind a "v1." version prefix.
type Codec struct {
	aead cipher.AEAD
}

// NewCodec returns the codec of key, which must be KeySize bytes.
func NewCodec(key []byte) (*Codec, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("session: codec key must be %d bytes", KeySize)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("session: codec: %w", err)
	}
	return &Codec{aead: aead}, nil
}

// sealedLen is the length of the sealed form of a plaintext of n bytes.
func sealedLen(n int) int {
	return len(sealedPrefix) + sealedEncoding.EncodedLen(chacha20poly1305.NonceSizeX+n+chacha20poly1305.Overhead)
}

// Seal encodes v as JSON and seals it. ErrTooLarge when the result exceeds MaxSealedBytes.
func (c *Codec) Seal(v any) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("session: codec without a key")
	}
	pt, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("session: encode: %w", err)
	}
	if sealedLen(len(pt)) > MaxSealedBytes {
		return "", ErrTooLarge
	}
	buf := make([]byte, chacha20poly1305.NonceSizeX, chacha20poly1305.NonceSizeX+len(pt)+chacha20poly1305.Overhead)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("session: nonce: %w", err)
	}
	sealed := c.aead.Seal(buf, buf, pt, codecAD)
	return sealedPrefix + sealedEncoding.EncodeToString(sealed), nil
}

// Open opens sealed into v. ErrInvalid for anything this codec did not seal, unchanged.
func (c *Codec) Open(sealed string, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("session: Open needs a non-nil pointer")
	}
	if c == nil || c.aead == nil || len(sealed) > MaxSealedBytes {
		return ErrInvalid
	}
	body, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return ErrInvalid
	}
	raw, err := sealedEncoding.DecodeString(body)
	if err != nil || len(raw) < chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return ErrInvalid
	}
	nonce, ct := raw[:chacha20poly1305.NonceSizeX], raw[chacha20poly1305.NonceSizeX:]
	pt, err := c.aead.Open(nil, nonce, ct, codecAD)
	if err != nil {
		return ErrInvalid
	}
	// Decode into a fresh value, so v stays untouched when the content does not fit it.
	fresh := reflect.New(rv.Elem().Type())
	dec := json.NewDecoder(bytes.NewReader(pt))
	dec.DisallowUnknownFields()
	if err := dec.Decode(fresh.Interface()); err != nil {
		return ErrInvalid
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrInvalid
	}
	rv.Elem().Set(fresh.Elem())
	return nil
}
