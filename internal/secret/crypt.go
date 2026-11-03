package secret

import (
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// encPrefix starts every sealed value: enc:v1:<kid>:<base64(nonce24||ct||tag)>.
const encPrefix = "enc:v1:"

// Errors from Decrypt and Encrypt. Callers list the affected field paths on
// startup (design C.4); none of these messages carry key or plaintext bytes.
var (
	ErrNoKey      = errors.New("no secret key configured (set DASH_SECRET_KEY or DASH_SECRET_KEY_FILE)")
	ErrMalformed  = errors.New("malformed enc:v1 value")
	ErrUnknownKey = errors.New("value was encrypted with a key that is not configured")
	ErrDecrypt    = errors.New("decryption failed: wrong key, or the value was moved to another field")
)

// Key is one 32-byte encryption key. The zero Key means "none". ikm is the key itself, kept only
// for Derive; nothing else reads it.
type Key struct {
	kid  string
	aead cipher.AEAD
	ikm  []byte
}

// ParseKey reads a key from base64 (standard or URL alphabet, padded or not)
// and requires exactly 32 bytes. The error never echoes the input.
func ParseKey(s string) (Key, error) {
	s = strings.TrimSpace(s)
	var raw []byte
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			raw = b
			break
		}
	}
	if raw == nil {
		return Key{}, errors.New("secret key is not valid base64")
	}
	if len(raw) != chacha20poly1305.KeySize {
		return Key{}, fmt.Errorf("secret key must be %d bytes, got %d (generate one with: openssl rand -base64 32)", chacha20poly1305.KeySize, len(raw))
	}
	aead, err := chacha20poly1305.NewX(raw)
	if err != nil {
		return Key{}, err
	}
	sum := sha256.Sum256(raw)
	return Key{kid: hex.EncodeToString(sum[:4]), aead: aead, ikm: raw}, nil
}

func (k Key) isZero() bool { return k.aead == nil }

// Keyring holds the key new values are sealed with and, during a rotation,
// the key old values were sealed with (DASH_SECRET_KEY_PREVIOUS). A nil
// *Keyring is valid and means no key is configured.
type Keyring struct {
	current, previous Key
}

// NewKeyring returns a keyring. Pass the zero Key as previous when no
// rotation is in progress.
func NewKeyring(current, previous Key) *Keyring {
	return &Keyring{current: current, previous: previous}
}

// DerivedKeySize is the length of a key Derive returns.
const DerivedKeySize = 32

// Derive returns a key for another purpose than sealing settings, derived from the current key
// with HKDF-SHA256 and info (the session cookies, design D8: "dash/session/v1|<salt>"). The key
// itself never leaves the keyring, and the result depends only on the key and info. The previous
// key is never used, so a rotation changes every derived key. ErrNoKey without a current key.
func (k *Keyring) Derive(info string) ([]byte, error) {
	if k == nil || k.current.isZero() {
		return nil, ErrNoKey
	}
	return hkdf.Key(sha256.New, k.current.ikm, nil, info, DerivedKeySize)
}

// IsEncrypted reports whether s is a sealed value.
func IsEncrypted(s string) bool { return strings.HasPrefix(s, encPrefix) }

// Encrypt seals plaintext with the current key. path is bound as additional
// data, so the result only decrypts at the same field.
func (k *Keyring) Encrypt(path, plaintext string) (string, error) {
	if k == nil || k.current.isZero() {
		return "", ErrNoKey
	}
	aead := k.current.aead
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), []byte(path))
	return encPrefix + k.current.kid + ":" + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt opens a value sealed by Encrypt for the same path, using whichever
// of the current or previous key its kid names.
func (k *Keyring) Decrypt(path, s string) (string, error) {
	kid, payload, err := parseSealed(s)
	if err != nil {
		return "", err
	}
	if k == nil || k.current.isZero() {
		return "", ErrNoKey
	}
	key := k.byKid(kid)
	if key.isZero() {
		return "", ErrUnknownKey
	}
	ns := key.aead.NonceSize()
	if len(payload) < ns+key.aead.Overhead() {
		return "", fmt.Errorf("%w: payload too short", ErrMalformed)
	}
	plain, err := key.aead.Open(nil, payload[:ns], payload[ns:], []byte(path))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}

// NeedsRekey reports whether s is a sealed value that the current key did not
// seal, so startup should re-encrypt it. Anything that is not a well-formed
// enc:v1 value reports false.
func (k *Keyring) NeedsRekey(s string) bool {
	kid, _, err := parseSealed(s)
	if err != nil || k == nil || k.current.isZero() {
		return false
	}
	return kid != k.current.kid
}

func (k *Keyring) byKid(kid string) Key {
	switch {
	case !k.current.isZero() && kid == k.current.kid:
		return k.current
	case !k.previous.isZero() && kid == k.previous.kid:
		return k.previous
	}
	return Key{}
}

// parseSealed splits enc:v1:<kid>:<base64> and validates its shape.
func parseSealed(s string) (kid string, payload []byte, err error) {
	rest, ok := strings.CutPrefix(s, encPrefix)
	if !ok {
		return "", nil, fmt.Errorf("%w: missing %s prefix", ErrMalformed, encPrefix)
	}
	kid, b64, ok := strings.Cut(rest, ":")
	if !ok || !validKid(kid) {
		return "", nil, fmt.Errorf("%w: bad key id", ErrMalformed)
	}
	payload, err = base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", nil, fmt.Errorf("%w: payload is not base64", ErrMalformed)
	}
	return kid, payload, nil
}

func validKid(kid string) bool {
	if len(kid) != 8 {
		return false
	}
	for _, c := range kid {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
