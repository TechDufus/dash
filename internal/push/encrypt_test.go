package push

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"strings"
	"testing"
)

// The example of RFC 8291 section 5 and Appendix A.
const (
	rfcPlaintext  = "When I grow up, I want to be a watermelon"
	rfcASPrivate  = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPrivate  = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic   = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcSalt       = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuthSecret = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcBody       = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := b64.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// With the application server key and salt of RFC 8291 Appendix A, the body is the RFC's, byte
// for byte; the user agent's private key opens it.
func TestEncryptReproducesRFC8291(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	ua, auth, err := DecodeKeys(rfcUAPublic, rfcAuthSecret)
	if err != nil {
		t.Fatal(err)
	}
	body, err := encrypt(as, mustB64(t, rfcSalt), ua, auth, []byte(rfcPlaintext))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(body); got != rfcBody {
		t.Fatalf("body =\n%s\nwant\n%s", got, rfcBody)
	}
	uaPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := Decrypt(uaPriv, auth, mustB64(t, rfcBody)); err != nil || string(plain) != rfcPlaintext {
		t.Errorf("Decrypt = %q, %v", plain, err)
	}
}

// Encrypt uses a fresh key and salt each time, and the largest plaintext fits one 4096-byte
// record; a larger one is refused.
func TestEncryptRoundTripAndBound(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := bytes.Repeat([]byte{7}, 16)
	big := bytes.Repeat([]byte("x"), MaxPlaintext)
	a, err := Encrypt(uaPriv.PublicKey(), auth, big)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != RecordSize {
		t.Errorf("len = %d, want %d", len(a), RecordSize)
	}
	if plain, err := Decrypt(uaPriv, auth, a); err != nil || !bytes.Equal(plain, big) {
		t.Errorf("Decrypt: %v", err)
	}
	b, err := Encrypt(uaPriv.PublicKey(), auth, big)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a[:16], b[:16]) || bytes.Equal(a[21:86], b[21:86]) {
		t.Error("the salt or the ephemeral key repeats")
	}
	if _, err := Encrypt(uaPriv.PublicKey(), auth, append(big, 'x')); err == nil {
		t.Error("a plaintext over one record was sealed")
	}
	if _, err := Decrypt(uaPriv, bytes.Repeat([]byte{8}, 16), a); err == nil {
		t.Error("another auth secret opened the body")
	}
}

func TestDecodeKeys(t *testing.T) {
	if _, _, err := DecodeKeys(rfcUAPublic+"=", rfcAuthSecret+"=="); err != nil {
		t.Errorf("padded keys: %v", err)
	}
	for name, k := range map[string][2]string{
		"short auth":       {rfcUAPublic, "BTBZMqHH6r4Tts7J_aSI"},
		"compressed point": {b64.EncodeToString(append([]byte{2}, mustB64(t, rfcUAPublic)[1:33]...)), rfcAuthSecret},
		"not on the curve": {strings.Replace(rfcUAPublic, "BCVx", "BCVy", 1), rfcAuthSecret},
		"not base64":       {"!!", rfcAuthSecret},
	} {
		if _, _, err := DecodeKeys(k[0], k[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
