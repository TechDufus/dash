package session_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/session/sessiontest"
)

func TestCodecRoundTripUsesAFreshNonce(t *testing.T) {
	c := sessiontest.Codec()
	in := session.Payload{User: "alice", Groups: []string{"ops"}, IssuedAt: 1, ExpiresAt: 2, UserEpoch: 3, GlobalEpoch: 4}
	a, err := c.Seal(in)
	must(t, err)
	b, err := c.Seal(in)
	must(t, err)
	if a == b {
		t.Fatal("two seals of one value are equal: the nonce is not random")
	}
	for _, s := range []string{a, b} {
		if !strings.HasPrefix(s, "v1.") {
			t.Errorf("sealed value %q lacks the v1. prefix", s[:8])
		}
		var out session.Payload
		must(t, c.Open(s, &out))
		if out.User != "alice" || out.Groups[0] != "ops" || out.GlobalEpoch != 4 {
			t.Errorf("round trip = %+v", out)
		}
	}
}

func TestNewCodecNeedsAKeyOfKeySize(t *testing.T) {
	for _, n := range []int{0, 16, session.KeySize - 1, session.KeySize + 1} {
		if _, err := session.NewCodec(make([]byte, n)); err == nil {
			t.Errorf("NewCodec accepted a %d-byte key", n)
		}
	}
}

// One flipped byte anywhere in the sealed value, a value sealed with another key, and every
// malformed shape are ErrInvalid, and the target is left as it was.
func TestTamperedCookieIsInvalid(t *testing.T) {
	c := sessiontest.Codec()
	sealed, err := c.Seal(session.Payload{User: "alice", IssuedAt: 1, ExpiresAt: 2})
	must(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, "v1."))
	must(t, err)
	check := func(name, s string) {
		t.Helper()
		out := session.Payload{User: "untouched"}
		if err := c.Open(s, &out); !errors.Is(err, session.ErrInvalid) {
			t.Errorf("%s: Open = %v, want ErrInvalid", name, err)
		}
		if out.User != "untouched" {
			t.Errorf("%s: Open changed the target to %+v", name, out)
		}
	}
	for i := range raw {
		flipped := bytes.Clone(raw)
		flipped[i] ^= 0x01
		check("flipped byte", "v1."+base64.RawURLEncoding.EncodeToString(flipped))
	}
	other, err := session.NewCodec(bytes.Repeat([]byte{0x43}, session.KeySize))
	must(t, err)
	foreign, err := other.Seal(session.Payload{User: "alice", IssuedAt: 1, ExpiresAt: 2})
	must(t, err)
	check("another key", foreign)
	check("empty", "")
	check("no prefix", strings.TrimPrefix(sealed, "v1."))
	check("v2 prefix", "v2."+strings.TrimPrefix(sealed, "v1."))
	check("truncated", sealed[:len(sealed)-1])
	check("only the nonce", "v1."+base64.RawURLEncoding.EncodeToString(raw[:24]))
	check("padded", sealed+"=")
	check("not base64", "v1.!!!!")
	check("over the size limit", sealed+strings.Repeat("A", session.MaxSealedBytes))
	// A sealed value of another shape (an unknown field) is not a payload.
	odd, err := c.Seal(map[string]any{"u": "alice", "role": "admin"})
	must(t, err)
	check("unknown field", odd)

	if err := c.Open(sealed, session.Payload{}); err == nil || errors.Is(err, session.ErrInvalid) {
		t.Errorf("Open into a non-pointer: %v, want a usage error", err)
	}
}

func TestSealRefusesOverMaxSealedBytesAtTheBoundary(t *testing.T) {
	c := sessiontest.Codec()
	sawOK, sawTooLarge := false, false
	for n := 2500; n < 2700; n++ {
		s := strings.Repeat("a", n)
		// "v1." + base64url(24-byte nonce + JSON string + 16-byte tag)
		want := 3 + base64.RawURLEncoding.EncodedLen(24+len(s)+2+16)
		out, err := c.Seal(s)
		switch {
		case want > session.MaxSealedBytes:
			sawTooLarge = true
			if !errors.Is(err, session.ErrTooLarge) || out != "" {
				t.Fatalf("n=%d: sealed length %d > %d: got %d bytes, %v; want ErrTooLarge", n, want, session.MaxSealedBytes, len(out), err)
			}
		default:
			sawOK = true
			if err != nil || len(out) != want {
				t.Fatalf("n=%d: got %d bytes, %v; want %d bytes", n, len(out), err, want)
			}
		}
	}
	if !sawOK || !sawTooLarge {
		t.Fatal("the loop did not cross the boundary")
	}
}

type recordingSource struct {
	info string
	key  []byte
	err  error
}

func (s *recordingSource) Derive(info string) ([]byte, error) {
	s.info = info
	return s.key, s.err
}

func TestDeriveKeyBindsPurposeAndSalt(t *testing.T) {
	salt := []byte{0xfb, 0xff, 0x01, 0x02}
	src := &recordingSource{key: bytes.Repeat([]byte{1}, session.KeySize)}
	_, err := session.DeriveKey(src, session.PurposeSession, salt)
	must(t, err)
	if want := "dash/session/v1|-_8BAg"; src.info != want {
		t.Errorf("info %q, want %q", src.info, want)
	}

	ring := keyring(t, 0x11)
	k := func(purpose string, salt []byte) []byte {
		t.Helper()
		key, err := session.DeriveKey(ring, purpose, salt)
		must(t, err)
		if len(key) != session.KeySize {
			t.Fatalf("key is %d bytes", len(key))
		}
		return key
	}
	base := k(session.PurposeSession, salt)
	if !bytes.Equal(base, k(session.PurposeSession, salt)) {
		t.Error("DeriveKey is not deterministic")
	}
	if bytes.Equal(base, k(session.PurposeOIDCState, salt)) {
		t.Error("the session and OIDC state keys are equal")
	}
	if bytes.Equal(base, k(session.PurposeSession, []byte{9, 9, 9, 9})) {
		t.Error("the salt does not change the key")
	}

	if _, err := session.DeriveKey(secret.NewKeyring(secret.Key{}, secret.Key{}), session.PurposeSession, salt); !errors.Is(err, secret.ErrNoKey) {
		t.Errorf("without a key: %v, want secret.ErrNoKey", err)
	}
	if _, err := session.DeriveKey(ring, session.PurposeSession, nil); err == nil {
		t.Error("DeriveKey accepted an empty salt")
	}
	if _, err := session.DeriveKey(&recordingSource{key: []byte{1}}, session.PurposeSession, salt); err == nil {
		t.Error("DeriveKey accepted a short key")
	}
}

func TestRandomKeyIsFreshPerCall(t *testing.T) {
	a, err := session.RandomKey()
	must(t, err)
	b, err := session.RandomKey()
	must(t, err)
	if len(a) != session.KeySize || bytes.Equal(a, b) {
		t.Fatalf("RandomKey: %d bytes, equal=%v", len(a), bytes.Equal(a, b))
	}
}

// keyring is a DASH_SECRET_KEY of 32 bytes of b.
func keyring(t *testing.T, b byte, previous ...secret.Key) *secret.Keyring {
	t.Helper()
	k, err := secret.ParseKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)))
	must(t, err)
	var prev secret.Key
	if len(previous) > 0 {
		prev = previous[0]
	}
	return secret.NewKeyring(k, prev)
}

func parseKey(t *testing.T, b byte) secret.Key {
	t.Helper()
	k, err := secret.ParseKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)))
	must(t, err)
	return k
}

// managerFor builds the session manager cmd/dash builds: the key from the keyring and the store's
// salt, the store's epochs.
func managerFor(t *testing.T, ring session.KeySource, store *credentials.Store, now func() time.Time) *session.Manager {
	t.Helper()
	key, err := session.DeriveKey(ring, session.PurposeSession, store.SessionSalt())
	must(t, err)
	c, err := session.NewCodec(key)
	must(t, err)
	return session.NewManager(session.Options{Codec: c, Epochs: store, Now: now})
}

func TestSecretKeyRotationDropsSessions(t *testing.T) {
	clk := newClock()
	store := memStore(t)
	cookie, err := managerFor(t, keyring(t, 0x11), store, clk.now).Issue(session.Identity{User: "alice"}, session.Keep{})
	must(t, err)

	// A restart with the same key keeps the session.
	if _, _, err := managerFor(t, keyring(t, 0x11), store, clk.now).Read(withCookies(cookie)); err != nil {
		t.Fatalf("same key: %v", err)
	}
	// DASH_SECRET_KEY rotated, the old one as DASH_SECRET_KEY_PREVIOUS: never used for sessions.
	rotated := managerFor(t, keyring(t, 0x22, parseKey(t, 0x11)), store, clk.now)
	if _, _, err := rotated.Read(withCookies(cookie)); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("after a key rotation: %v, want ErrInvalid", err)
	}
}

func TestDeletingCredentialsFileKillsEverySession(t *testing.T) {
	clk := newClock()
	ring := keyring(t, 0x11)
	dir := t.TempDir()
	store, err := credentials.Open(dir)
	must(t, err)
	var cookies []*http.Cookie
	for _, u := range []string{"alice", "bob"} {
		c, err := managerFor(t, ring, store, clk.now).Issue(session.Identity{User: u}, session.Keep{})
		must(t, err)
		cookies = append(cookies, c)
	}
	must(t, store.Close())

	// A restart with the file in place keeps every session.
	store, err = credentials.Open(dir)
	must(t, err)
	for _, c := range cookies {
		if _, _, err := managerFor(t, ring, store, clk.now).Read(withCookies(c)); err != nil {
			t.Fatalf("restart with the file: %v", err)
		}
	}
	must(t, store.Close())

	// Deleting credentials.yaml makes a new salt, so a new key: every cookie is dead, even though
	// the epochs (all 0) would match.
	must(t, os.Remove(filepath.Join(dir, credentials.FileName)))
	store, err = credentials.Open(dir)
	must(t, err)
	t.Cleanup(func() { store.Close() })
	for _, c := range cookies {
		if _, _, err := managerFor(t, ring, store, clk.now).Read(withCookies(c)); !errors.Is(err, session.ErrInvalid) {
			t.Errorf("after deleting %s: %v, want ErrInvalid", credentials.FileName, err)
		}
	}
}
