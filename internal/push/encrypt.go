// Package push sends Web Push and UnifiedPush notifications (design P.2): RFC 8030 delivery,
// RFC 8291 message encryption (aes128gcm only) and RFC 8292 VAPID signatures (ES256), on the
// standard library alone so the RFC 8291 test vector reproduces byte for byte.
package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// RecordSize is the rs of the one record dash sends (RFC 8188 2.1).
const RecordSize = 4096

// headerLen is salt (16), rs (4), idlen (1) and the 65-byte application server key.
const headerLen = 16 + 4 + 1 + 65

// MaxPlaintext is the most plaintext one record carries: the record holds the plaintext, the
// padding delimiter and the 16-byte tag (RFC 8291 4).
const MaxPlaintext = RecordSize - headerLen - 1 - 16

// ErrBadKeys is the error of subscription keys that are not a P-256 point and a 16-byte secret.
var ErrBadKeys = errors.New("push: the keys are not an uncompressed P-256 public key and a 16-byte auth secret")

// b64 is unpadded base64url, the encoding of keys on the wire.
var b64 = base64.RawURLEncoding

// DecodeKeys decodes a subscription's p256dh and auth (PushSubscription.toJSON: unpadded
// base64url; padding is tolerated) and checks them: an uncompressed P-256 point and a 16-byte
// secret.
func DecodeKeys(p256dh, auth string) (ua *ecdh.PublicKey, secret []byte, err error) {
	pub, err1 := b64.DecodeString(strings.TrimRight(p256dh, "="))
	secret, err2 := b64.DecodeString(strings.TrimRight(auth, "="))
	if err1 != nil || err2 != nil || len(secret) != 16 || len(pub) != 65 {
		return nil, nil, ErrBadKeys
	}
	if ua, err = ecdh.P256().NewPublicKey(pub); err != nil {
		return nil, nil, ErrBadKeys
	}
	return ua, secret, nil
}

// Encrypt seals plaintext for the user agent's key ua and auth secret (RFC 8291 3.4) into one
// aes128gcm record (RFC 8188) with a fresh ephemeral key and salt: the body of a push request
// with Content-Encoding: aes128gcm.
func Encrypt(ua *ecdh.PublicKey, authSecret, plaintext []byte) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(as, salt, ua, authSecret, plaintext)
}

// encrypt is Encrypt with the application server's ephemeral key and the salt given, which
// the RFC 8291 Appendix A vector fixes.
func encrypt(as *ecdh.PrivateKey, salt []byte, ua *ecdh.PublicKey, authSecret, plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, fmt.Errorf("push: a plaintext of %d bytes is over %d", len(plaintext), MaxPlaintext)
	}
	if len(authSecret) != 16 || len(salt) != 16 {
		return nil, ErrBadKeys
	}
	cek, nonce, err := deriveKeys(as, ua, as.PublicKey().Bytes(), ua.Bytes(), salt, authSecret)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(cek)
	if err != nil {
		return nil, err
	}
	out := make([]byte, headerLen, headerLen+len(plaintext)+1+gcm.Overhead())
	copy(out, salt)
	binary.BigEndian.PutUint32(out[16:], RecordSize)
	out[20] = 65
	copy(out[21:], as.PublicKey().Bytes())
	record := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 0x02) // the last record's delimiter, no padding
	return gcm.Seal(out, nonce, record, nil), nil
}

// deriveKeys is RFC 8291 3.3 and 3.4: the ECDH secret between priv and peer, combined with the
// auth secret over both public keys (user agent first), then the content key and nonce.
func deriveKeys(priv *ecdh.PrivateKey, peer *ecdh.PublicKey, asPub, uaPub, salt, authSecret []byte) (cek, nonce []byte, err error) {
	shared, err := priv.ECDH(peer)
	if err != nil {
		return nil, nil, err
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPub...), asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	if nonce, err = hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12); err != nil {
		return nil, nil, err
	}
	return cek, nonce, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Decrypt opens a body Encrypt made, as the user agent with key ua and the auth secret does:
// the receiving side, for tests and fake push services. Only one record is accepted.
func Decrypt(ua *ecdh.PrivateKey, authSecret, body []byte) ([]byte, error) {
	if len(body) < headerLen+17 || body[20] != 65 {
		return nil, errors.New("push: not an aes128gcm body with a 65-byte key id")
	}
	if rs := binary.BigEndian.Uint32(body[16:]); rs < uint32(len(body)-headerLen) {
		return nil, errors.New("push: more than one record")
	}
	as, err := ecdh.P256().NewPublicKey(body[21:headerLen])
	if err != nil {
		return nil, err
	}
	cek, nonce, err := deriveKeys(ua, as, as.Bytes(), ua.PublicKey().Bytes(), body[:16], authSecret)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(cek)
	if err != nil {
		return nil, err
	}
	record, err := gcm.Open(nil, nonce, body[headerLen:], nil)
	if err != nil {
		return nil, err
	}
	i := len(record) - 1
	for i >= 0 && record[i] == 0 {
		i--
	}
	if i < 0 || record[i] != 0x02 {
		return nil, errors.New("push: the record is not the last one")
	}
	return record[:i], nil
}
