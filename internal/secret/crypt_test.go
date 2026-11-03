package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func newKey(t *testing.T) (Key, string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	s := base64.StdEncoding.EncodeToString(raw)
	k, err := ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return k, s
}

func TestParseKey(t *testing.T) {
	k, s := newKey(t)
	if len(k.kid) != 8 {
		t.Errorf("kid = %q, want 8 hex chars", k.kid)
	}
	// Other base64 spellings of the same bytes give the same key id.
	raw, _ := base64.StdEncoding.DecodeString(s)
	for name, enc := range map[string]string{
		"padded url":   base64.URLEncoding.EncodeToString(raw),
		"raw url":      base64.RawURLEncoding.EncodeToString(raw),
		"raw std":      base64.RawStdEncoding.EncodeToString(raw),
		"with newline": s + "\n",
	} {
		got, err := ParseKey(enc)
		if err != nil || got.kid != k.kid {
			t.Errorf("%s: kid=%q err=%v, want %q", name, got.kid, err, k.kid)
		}
	}
	for name, bad := range map[string]string{
		"empty":     "",
		"not b64":   "!!!notbase64!!!",
		"too short": base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"too long":  base64.StdEncoding.EncodeToString(make([]byte, 33)),
	} {
		_, err := ParseKey(bad)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("%s: error echoes the key text: %v", name, err)
		}
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	k, _ := newKey(t)
	ring := NewKeyring(k, Key{})
	const path = "plugins.proxmox.tokenSecret"
	enc, err := ring.Encrypt(path, canary)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(enc) || !strings.HasPrefix(enc, "enc:v1:"+k.kid+":") || strings.Contains(enc, canary) {
		t.Fatalf("bad ciphertext %q", enc)
	}
	got, err := ring.Decrypt(path, enc)
	if err != nil || got != canary {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}
	if ring.NeedsRekey(enc) {
		t.Error("a value sealed with the current key does not need a rekey")
	}
	// Random nonce: sealing twice differs.
	enc2, _ := ring.Encrypt(path, canary)
	if enc == enc2 {
		t.Error("two encryptions of the same value are identical (nonce reuse)")
	}
	// Empty plaintext still round-trips.
	e, err := ring.Encrypt(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ring.Decrypt(path, e); err != nil || got != "" {
		t.Errorf("empty round trip = %q, %v", got, err)
	}
}

func TestDecryptFailsAtAnotherPath(t *testing.T) {
	k, _ := newKey(t)
	ring := NewKeyring(k, Key{})
	enc, _ := ring.Encrypt("plugins.proxmox.tokenSecret", canary)
	if _, err := ring.Decrypt("plugins.grafana.apiKey", enc); !errors.Is(err, ErrDecrypt) {
		t.Errorf("moved ciphertext: err = %v, want ErrDecrypt", err)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	k1, _ := newKey(t)
	k2, _ := newKey(t)
	enc, _ := NewKeyring(k1, Key{}).Encrypt("p", canary)
	_, err := NewKeyring(k2, Key{}).Decrypt("p", enc)
	if !errors.Is(err, ErrUnknownKey) {
		t.Errorf("wrong key: err = %v, want ErrUnknownKey", err)
	}
	// Same kid but corrupted bytes: authentication fails.
	tampered := enc[:len(enc)-6] + "AAAA=="
	if _, err := NewKeyring(k1, Key{}).Decrypt("p", tampered); !errors.Is(err, ErrDecrypt) {
		t.Errorf("tampered: err = %v, want ErrDecrypt", err)
	}
}

func TestNoKey(t *testing.T) {
	var nilRing *Keyring
	k, _ := newKey(t)
	enc, _ := NewKeyring(k, Key{}).Encrypt("p", "v")
	for name, ring := range map[string]*Keyring{"nil": nilRing, "zero": NewKeyring(Key{}, Key{})} {
		if _, err := ring.Encrypt("p", "v"); !errors.Is(err, ErrNoKey) {
			t.Errorf("%s Encrypt: %v", name, err)
		}
		if _, err := ring.Decrypt("p", enc); !errors.Is(err, ErrNoKey) {
			t.Errorf("%s Decrypt: %v", name, err)
		}
		if ring.NeedsRekey(enc) {
			t.Errorf("%s NeedsRekey must be false without a key", name)
		}
	}
	if !strings.Contains(ErrNoKey.Error(), "DASH_SECRET_KEY") {
		t.Error("ErrNoKey must name the missing key")
	}
}

func TestPreviousKeyDecryptsAndNeedsRekey(t *testing.T) {
	oldKey, _ := newKey(t)
	newK, _ := newKey(t)
	const path = "plugins.grafana.apiKey"
	sealedOld, _ := NewKeyring(oldKey, Key{}).Encrypt(path, canary)

	ring := NewKeyring(newK, oldKey)
	got, err := ring.Decrypt(path, sealedOld)
	if err != nil || got != canary {
		t.Fatalf("previous key Decrypt = %q, %v", got, err)
	}
	if !ring.NeedsRekey(sealedOld) {
		t.Error("a value sealed with the previous key needs a rekey")
	}
	re, err := ring.Encrypt(path, got)
	if err != nil {
		t.Fatal(err)
	}
	if ring.NeedsRekey(re) || !strings.Contains(re, ":"+newK.kid+":") {
		t.Errorf("re-encrypted value should use the new key: %s", re)
	}
	// Once the previous key is dropped, the old value is unreadable, not blank.
	if _, err := NewKeyring(newK, Key{}).Decrypt(path, sealedOld); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("after dropping the previous key: %v", err)
	}
}

func TestMalformedEncrypted(t *testing.T) {
	k, _ := newKey(t)
	ring := NewKeyring(k, Key{})
	good, _ := ring.Encrypt("p", "v")
	_, b64, _ := strings.Cut(strings.TrimPrefix(good, "enc:v1:"), ":")
	short := base64.StdEncoding.EncodeToString(make([]byte, 10))
	// The key's own id, uppercased, with a final hex letter: a random id can be all digits,
	// and then uppercasing it would leave a valid id.
	upperKid := strings.ToUpper(k.kid[:len(k.kid)-1]) + "F"
	for name, s := range map[string]string{
		"no prefix":       "plain",
		"wrong version":   "enc:v2:" + k.kid + ":" + b64,
		"no kid":          "enc:v1:",
		"no separator":    "enc:v1:" + k.kid,
		"short kid":       "enc:v1:abc:" + b64,
		"uppercase kid":   "enc:v1:" + upperKid + ":" + b64,
		"non-hex kid":     "enc:v1:zzzzzzzz:" + b64,
		"bad base64":      "enc:v1:" + k.kid + ":***",
		"truncated":       "enc:v1:" + k.kid + ":" + short,
		"empty payload":   "enc:v1:" + k.kid + ":",
		"extra colon kid": "enc:v1:" + k.kid + ":" + ":" + b64,
	} {
		_, err := ring.Decrypt("p", s)
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
		if ring.NeedsRekey(s) {
			t.Errorf("%s: NeedsRekey must be false for a malformed value", name)
		}
	}
}

func TestIsEncrypted(t *testing.T) {
	for s, want := range map[string]bool{"enc:v1:x": true, "enc:v2:x": false, "": false, "plain": false, " enc:v1:x": false} {
		if IsEncrypted(s) != want {
			t.Errorf("IsEncrypted(%q) = %v", s, !want)
		}
	}
}

// Derive depends on the current key and the info only: the same key in another base64 spelling
// derives the same bytes, another info or another key derives others, and the previous key of a
// rotation plays no part (a rotation changes every derived key).
func TestDeriveBindsTheCurrentKeyAndTheInfo(t *testing.T) {
	k, s := newKey(t)
	raw, _ := base64.StdEncoding.DecodeString(s)
	same, err := ParseKey(base64.RawURLEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := newKey(t)
	derive := func(ring *Keyring, info string) []byte {
		t.Helper()
		b, err := ring.Derive(info)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != DerivedKeySize {
			t.Fatalf("Derive returned %d bytes, want %d", len(b), DerivedKeySize)
		}
		return b
	}
	base := derive(NewKeyring(k, Key{}), "dash/session/v1|salt")
	if !bytes.Equal(base, derive(NewKeyring(same, Key{}), "dash/session/v1|salt")) {
		t.Error("the same key derived different bytes")
	}
	if !bytes.Equal(base, derive(NewKeyring(k, other), "dash/session/v1|salt")) {
		t.Error("the previous key changed the derived bytes")
	}
	for name, b := range map[string][]byte{
		"other info": derive(NewKeyring(k, Key{}), "dash/session/v1|other"),
		"other key":  derive(NewKeyring(other, Key{}), "dash/session/v1|salt"),
		"rotated":    derive(NewKeyring(other, k), "dash/session/v1|salt"),
	} {
		if bytes.Equal(base, b) {
			t.Errorf("%s derived the same bytes", name)
		}
	}
	if bytes.Contains(base, raw) {
		t.Error("the derived bytes contain the key")
	}
	var nilRing *Keyring
	for name, ring := range map[string]*Keyring{"nil": nilRing, "zero": NewKeyring(Key{}, Key{})} {
		if _, err := ring.Derive("x"); !errors.Is(err, ErrNoKey) {
			t.Errorf("%s Derive: %v, want ErrNoKey", name, err)
		}
	}
}
