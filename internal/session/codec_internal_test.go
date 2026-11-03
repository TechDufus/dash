package session

import (
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// The associated data is the value "dash/codec/v1" (pinned here, not taken from codecAD): a value
// sealed under the same key with any other AD does not open, so a later format bump cannot be
// confused with this one. Every seal draws a fresh nonce. (From the v0.12.0 security
// verification.)
func TestCodecADIsPinnedAndNoncesAreFresh(t *testing.T) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	c, err := NewCodec(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte(`{"u":"alice","iat":1,"exp":2,"ep":0,"ge":0}`)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	seal := func(ad string) string {
		return sealedPrefix + sealedEncoding.EncodeToString(aead.Seal(append([]byte{}, nonce...), nonce, pt, []byte(ad)))
	}
	var p Payload
	if err := c.Open(seal("dash/codec/v1"), &p); err != nil || p.User != "alice" {
		t.Fatalf("a value sealed with AD dash/codec/v1 does not open: %v", err)
	}
	for _, ad := range []string{"", "dash/codec/v2", "dash/codec/v1 ", "dash/session/v1"} {
		if err := c.Open(seal(ad), &p); err == nil {
			t.Errorf("a value sealed with AD %q opened", ad)
		}
	}
	seen := make(map[string]bool, 2000)
	for range 2000 {
		s, err := c.Seal(Payload{User: "alice", IssuedAt: 1, ExpiresAt: 2})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := sealedEncoding.DecodeString(s[len(sealedPrefix):])
		if err != nil {
			t.Fatal(err)
		}
		n := string(raw[:chacha20poly1305.NonceSizeX])
		if seen[n] {
			t.Fatal("a nonce repeated")
		}
		seen[n] = true
	}
}
