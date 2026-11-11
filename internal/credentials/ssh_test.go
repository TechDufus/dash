package credentials

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/secret"
)

// openSSH opens the store of dir and readies it for the SSH identity with keys.
func openSSH(t *testing.T, dir string, keys *secret.Keyring) (*Store, SSHOpen) {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.OpenSSH(keys)
	if err != nil {
		t.Fatal(err)
	}
	return s, res
}

// The key pair is made at first need, sealed in the file, and the same after a restart. The file
// stays at schemaVersion 1 until then.
func TestSSHIdentityIsSealedAndKept(t *testing.T) {
	dir, keys := t.TempDir(), secret.NewKeyring(pushKey(t), secret.Key{})
	s, _ := openSSH(t, dir, keys)
	if f := fileOf(t, dir); !strings.Contains(f, "schemaVersion: 1") || strings.Contains(f, "ssh:") {
		t.Fatalf("after OpenSSH the file is not a plain version 1:\n%s", f)
	}
	priv, err := s.SSHIdentity(pushT0)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.SSHIdentity(pushT0.Add(time.Hour)); !bytes.Equal(again, priv) {
		t.Error("a second call made another key")
	}
	f := fileOf(t, dir)
	if !strings.Contains(f, "schemaVersion: 2") || !strings.Contains(f, "ssh-ed25519 AAAA") || !strings.Contains(f, " dash") {
		t.Errorf("the file lacks the version or the public key:\n%s", f)
	}
	for _, clear := range []string{base64.RawURLEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(priv.Seed())} {
		if strings.Contains(f, clear) {
			t.Error("the file holds the private key in clear")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, res := openSSH(t, dir, keys)
	if res != (SSHOpen{}) {
		t.Errorf("reopening with the same key = %+v, want nothing to do", res)
	}
	if got, err := s2.SSHIdentity(pushT0); err != nil || !bytes.Equal(got, priv) {
		t.Errorf("the key did not survive a restart: %v", err)
	}
}

// Without DASH_SECRET_KEY or without a data dir there is no identity, and the errors say which.
func TestSSHIdentityNeedsAKeyAndADataDir(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.SSHIdentity(pushT0); !errors.Is(err, ErrSSHNoKey) {
		t.Errorf("before OpenSSH: %v, want ErrSSHNoKey", err)
	}
	if _, err := s.OpenSSH(nil); !errors.Is(err, ErrSSHNoKey) {
		t.Errorf("OpenSSH(nil) = %v, want ErrSSHNoKey", err)
	}
	mem, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.OpenSSH(secret.NewKeyring(pushKey(t), secret.Key{})); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.SSHIdentity(pushT0); !errors.Is(err, ErrSSHNotPersistent) {
		t.Errorf("in memory: %v, want ErrSSHNotPersistent", err)
	}
}

// A rotation with DASH_SECRET_KEY_PREVIOUS re-seals the key and keeps it; a rotation without it
// loses the key, and the next call makes another one.
func TestSSHIdentityAcrossKeyRotation(t *testing.T) {
	dir := t.TempDir()
	a, b, c := pushKey(t), pushKey(t), pushKey(t)
	s, _ := openSSH(t, dir, secret.NewKeyring(a, secret.Key{}))
	priv, err := s.SSHIdentity(pushT0)
	if err != nil {
		t.Fatal(err)
	}
	before := fileOf(t, dir)
	_ = s.Close()

	s, res := openSSH(t, dir, secret.NewKeyring(b, a))
	if !res.Resealed || res.Lost {
		t.Fatalf("rotation with the previous key = %+v, want resealed", res)
	}
	if fileOf(t, dir) == before {
		t.Error("the file was not rewritten under the new key")
	}
	if got, err := s.SSHIdentity(pushT0); err != nil || !bytes.Equal(got, priv) {
		t.Fatalf("the key changed across a rotation with the previous key: %v", err)
	}
	_ = s.Close()

	s, res = openSSH(t, dir, secret.NewKeyring(c, secret.Key{}))
	if !res.Lost {
		t.Fatalf("rotation without the previous key = %+v, want lost", res)
	}
	got, err := s.SSHIdentity(pushT0)
	if err != nil || bytes.Equal(got, priv) {
		t.Fatalf("after a lost key: same key %v, err %v; want a new one", bytes.Equal(got, priv), err)
	}
}

// A stored identity that is not a sealed key is a file dash refuses to start on, as any other
// malformed section.
func TestSSHIdentityMalformedIsRefused(t *testing.T) {
	doc := emptyDocument(make([]byte, SaltSize))
	doc.SSH = &sshDoc{Identity: &sshIdentityDoc{PublicKey: "ssh-ed25519 AAAA", PrivateKey: "clear"}}
	data, err := encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decode(data); err == nil {
		t.Error("a malformed identity was accepted")
	}
}
