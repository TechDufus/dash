package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/plugins/ssh"
	"github.com/TechDufus/dash/internal/secret"
)

// sshKeys is the ssh plugin's key pair, kept sealed with DASH_SECRET_KEY in credentials.yaml
// (credentials.Store.SSHIdentity). It uses the credential store the auth mode opened when that
// one is backed by the data directory (local, oidc and proxy mode). In auth.mode=none, and in dev
// mode, whose store lives in memory, it opens the data directory's store itself, at the first
// need only: an install without SSH hosts writes nothing and takes no lock it did not before.
type sshKeys struct {
	keys    *secret.Keyring // DASH_SECRET_KEY, nil without one
	dataDir string
	auth    *credentials.Store // the auth mode's store; nil in auth.mode=none
	now     func() time.Time
	log     *slog.Logger

	mu     sync.Mutex
	store  *credentials.Store // the store the key pair lives in, once the first call chose it
	own    bool               // store was opened here, and close releases it
	opened bool               // OpenSSH ran
}

func newSSHKeys(keys *secret.Keyring, dataDir string, auth *credentials.Store, log *slog.Logger) *sshKeys {
	return &sshKeys{keys: keys, dataDir: dataDir, auth: auth, now: time.Now, log: log}
}

// Identity implements ssh.Identities. Without DASH_SECRET_KEY or a writable data directory the
// error wraps ssh.ErrNoIdentity, and the plugin says which to set.
func (s *sshKeys) Identity() (ed25519.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		return nil, fmt.Errorf("%w: DASH_SECRET_KEY is not set", ssh.ErrNoIdentity)
	}
	if s.store == nil {
		if s.auth != nil && s.auth.Persistent() {
			s.store = s.auth
		} else {
			st, err := credentials.Open(s.dataDir)
			if err != nil {
				return nil, err
			}
			s.store, s.own = st, true
		}
	}
	if !s.store.Persistent() {
		return nil, fmt.Errorf("%w: the data directory is not writable", ssh.ErrNoIdentity)
	}
	if !s.opened {
		res, err := s.store.OpenSSH(s.keys)
		if err != nil {
			return nil, err
		}
		s.opened = true
		if res.Lost {
			s.log.Warn("ssh: the stored SSH key does not open with DASH_SECRET_KEY (was it rotated without DASH_SECRET_KEY_PREVIOUS?); dash makes a new one, which every host must be given again")
		}
		if res.Resealed {
			s.log.Info("ssh: re-sealed the SSH key with the current DASH_SECRET_KEY")
		}
	}
	priv, err := s.store.SSHIdentity(s.now())
	if errors.Is(err, credentials.ErrSSHNoKey) || errors.Is(err, credentials.ErrSSHNotPersistent) {
		return nil, fmt.Errorf("%w: %w", ssh.ErrNoIdentity, err)
	}
	return priv, err
}

// close releases a store opened here; the auth mode's store is closed by its owner.
func (s *sshKeys) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.own && s.store != nil {
		_ = s.store.Close()
	}
	s.store, s.own, s.opened = nil, false, false
}
