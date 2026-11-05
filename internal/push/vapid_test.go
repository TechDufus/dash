package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// verifyVAPID checks an Authorization header as a push service does (RFC 8292 3): the ES256
// signature against the k key, and returns the claims and that key.
func verifyVAPID(t *testing.T, header string) (claims map[string]any, k string) {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid t=")
	tok, k, ok2 := strings.Cut(rest, ", k=")
	if !ok || !ok2 {
		t.Fatalf("Authorization = %q", header)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT = %q", tok)
	}
	var head map[string]string
	if err := json.Unmarshal(mustB64(t, parts[0]), &head); err != nil || head["alg"] != "ES256" || head["typ"] != "JWT" {
		t.Fatalf("header = %v, %v", head, err)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), mustB64(t, k))
	if err != nil {
		t.Fatal(err)
	}
	sig := mustB64(t, parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the signature does not verify with the k key")
	}
	if err := json.Unmarshal(mustB64(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	return claims, k
}

// A VAPID JWT verifies with the advertised public key and carries the endpoint's origin, an
// expiry of at most 12 hours and the contact.
func TestVAPIDSignatureVerifies(t *testing.T) {
	key, err := GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	v, err := NewVAPID(key, "mailto:ops@example.com", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	h, err := v.Authorization("https://push.example.net:8443/up/abc?x=1")
	if err != nil {
		t.Fatal(err)
	}
	claims, k := verifyVAPID(t, h)
	if k != v.PublicKey() {
		t.Errorf("k = %s, want the public key %s", k, v.PublicKey())
	}
	if claims["aud"] != "https://push.example.net:8443" || claims["sub"] != "mailto:ops@example.com" {
		t.Errorf("claims = %v", claims)
	}
	if exp := int64(claims["exp"].(float64)); exp <= now.Unix() || exp > now.Add(12*time.Hour).Unix() {
		t.Errorf("exp = %d, want within 12 hours of %d", exp, now.Unix())
	}
	raw, err := VAPIDKeyBytes(key)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key bytes: %d, %v", len(raw), err)
	}
	back, err := ParseVAPIDKey(raw)
	if err != nil || !back.Equal(key) {
		t.Errorf("ParseVAPIDKey: %v", err)
	}
}

// One JWT is cached per audience until an hour before it expires.
func TestVAPIDCachesPerAudience(t *testing.T) {
	key, err := GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	v, err := NewVAPID(key, "https://dash.example.com", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	auth := func(endpoint string) string {
		h, err := v.Authorization(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	first := auth("https://a.example.net/1")
	if auth("https://a.example.net/2") != first {
		t.Error("the same audience got a new JWT")
	}
	if auth("https://b.example.net/1") == first {
		t.Error("another audience got the same JWT")
	}
	now = now.Add(VAPIDLifetime - vapidRefresh - time.Second)
	if auth("https://a.example.net/1") != first {
		t.Error("the JWT was replaced more than an hour before its expiry")
	}
	now = now.Add(time.Second)
	renewed := auth("https://a.example.net/1")
	if renewed == first {
		t.Error("the JWT was not replaced an hour before its expiry")
	}
	if exp := int64(verifyClaims(t, renewed)["exp"].(float64)); exp != now.Add(VAPIDLifetime).Unix() {
		t.Errorf("renewed exp = %d", exp)
	}
	if _, err := v.Authorization("/relative"); err == nil {
		t.Error("a relative endpoint was signed for")
	}
}

func verifyClaims(t *testing.T, h string) map[string]any {
	t.Helper()
	c, _ := verifyVAPID(t, h)
	return c
}
