package credentials

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/TechDufus/dash/internal/secret"
)

// The ssh plugin's identity: one ed25519 key pair dash signs in to its SSH hosts with. The
// private key is enc:v1-sealed with DASH_SECRET_KEY, bound to its path, as the VAPID key is.

// sshIdentityPath is the sealing path (the additional data of enc:v1) of the private key.
const sshIdentityPath = "credentials.ssh.identity.privateKey"

// sshComment ends the public key's authorized_keys line, so an administrator reading a host's
// authorized_keys knows whose key it is.
const sshComment = "dash"

// SSH identity errors.
var (
	// ErrSSHNoKey means the store has no DASH_SECRET_KEY to seal the private key with (OpenSSH
	// was not called, or called without a key).
	ErrSSHNoKey = errors.New("credentials: the SSH identity needs DASH_SECRET_KEY")
	// ErrSSHNotPersistent means the store lives in memory: a key made there would be a new one at
	// every start, and every host would refuse it again.
	ErrSSHNotPersistent = errors.New("credentials: the SSH identity needs a writable data dir")
)

// sshDoc is the ssh: section of credentials.yaml.
type sshDoc struct {
	Identity *sshIdentityDoc `json:"identity,omitempty"`
}

// sshIdentityDoc is the key pair: the public key in clear as one authorized_keys line, the
// 32-byte ed25519 seed sealed (base64url, no padding).
type sshIdentityDoc struct {
	PublicKey  string    `json:"publicKey"`
	PrivateKey string    `json:"privateKey"`
	CreatedAt  time.Time `json:"createdAt"`
}

func cloneSSH(s *sshDoc) *sshDoc {
	if s == nil {
		return nil
	}
	out := &sshDoc{}
	if s.Identity != nil {
		id := *s.Identity
		out.Identity = &id
	}
	return out
}

// holdsSSH reports whether the document carries the SSH identity, which keeps it at
// schemaVersion 2.
func holdsSSH(s *sshDoc) bool { return s != nil && s.Identity != nil }

// validSSH is the shape decode requires of a stored identity.
func validSSH(s *sshDoc) error {
	if s == nil || s.Identity == nil {
		return nil
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s.Identity.PublicKey)); err != nil || !secret.IsEncrypted(s.Identity.PrivateKey) {
		return errors.New("ssh identity is malformed")
	}
	return nil
}

// SSHOpen is what OpenSSH found.
type SSHOpen struct {
	// Lost: the stored private key could not be opened (DASH_SECRET_KEY rotated without
	// DASH_SECRET_KEY_PREVIOUS) and was dropped; SSHIdentity makes a new one, which every host
	// must be given again.
	Lost bool
	// Resealed: the private key moved from DASH_SECRET_KEY_PREVIOUS to DASH_SECRET_KEY.
	Resealed bool
}

// OpenSSH readies the store for the SSH identity with keys: it takes keys for every later call
// and re-seals a private key sealed with the previous key. A key that cannot be opened is
// dropped. OpenSSH generates no key: SSHIdentity does at first need, so an install without SSH
// hosts keeps its file as it was. The caller logs what SSHOpen reports. Nothing is written when
// nothing changed.
func (s *Store) OpenSSH(keys *secret.Keyring) (SSHOpen, error) {
	if keys == nil {
		return SSHOpen{}, ErrSSHNoKey
	}
	s.mu.Lock()
	s.sshKeys = keys
	s.mu.Unlock()
	var res SSHOpen
	err := s.update(false, func(d *document) bool {
		res = SSHOpen{}
		if !holdsSSH(d.SSH) {
			return false
		}
		id := d.SSH.Identity
		plain, err := keys.Decrypt(sshIdentityPath, id.PrivateKey)
		if err != nil {
			d.SSH.Identity, res.Lost = nil, true
			return true
		}
		if !keys.NeedsRekey(id.PrivateKey) {
			return false
		}
		if id.PrivateKey, err = keys.Encrypt(sshIdentityPath, plain); err != nil {
			return false
		}
		res.Resealed = true
		return true
	})
	if err != nil {
		return SSHOpen{}, err
	}
	return res, nil
}

// SSHIdentity is the SSH key pair, generated and stored at the first call. It needs OpenSSH
// with a key (ErrSSHNoKey) and a persistent store (ErrSSHNotPersistent). Nothing is stored when
// the write fails.
func (s *Store) SSHIdentity(now time.Time) (ed25519.PrivateKey, error) {
	keys, err := s.sshKeyring()
	if err != nil {
		return nil, err
	}
	if !s.Persistent() {
		return nil, ErrSSHNotPersistent
	}
	var id *sshIdentityDoc
	var genErr error
	err = s.update(false, func(d *document) bool {
		if holdsSSH(d.SSH) {
			id = d.SSH.Identity
			return false
		}
		if id, genErr = newSSHIdentity(keys, now); genErr != nil {
			return false
		}
		d.SSH = &sshDoc{Identity: id}
		return true
	})
	if genErr != nil {
		return nil, genErr
	}
	if err != nil {
		return nil, err
	}
	plain, err := keys.Decrypt(sshIdentityPath, id.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("credentials: the SSH identity: %w", err)
	}
	seed, err := base64.RawURLEncoding.DecodeString(plain)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("credentials: the SSH identity is malformed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// newSSHIdentity generates an ed25519 key pair with its seed sealed.
func newSSHIdentity(keys *secret.Keyring, now time.Time) (*sshIdentityDoc, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	sealed, err := keys.Encrypt(sshIdentityPath, base64.RawURLEncoding.EncodeToString(priv.Seed()))
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + sshComment
	return &sshIdentityDoc{PublicKey: line, PrivateKey: sealed, CreatedAt: now.UTC().Truncate(time.Second)}, nil
}

// sshKeyring is the keyring OpenSSH took, or ErrSSHNoKey.
func (s *Store) sshKeyring() (*secret.Keyring, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.sshKeys == nil {
		return nil, ErrSSHNoKey
	}
	return s.sshKeys, nil
}
