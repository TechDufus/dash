// Package credentials is dash's credential store, credentials.yaml in the data directory (design
// M.7, contract D10): the local administrator account, the per-user session epochs, the global
// epoch and the session salt. It is not the settings store: auth.* is not editable from the UI,
// and a restored settings backup must never bring back an old password.
//
// The file is mode 0600 and carries a schemaVersion. Every write is atomic (a temporary file,
// fsync, rename, fsync of the directory) and there are no .bak copies. The store has a mutex of
// its own and holds its own flock on .credentials.lock, so a second dash on the same data
// directory is refused even when the settings store is read-only (and takes no lock). Without a
// writable data directory the store lives in memory (Persistent is false) and starts empty at
// every start. Phase 7 extends the document (personal access tokens, grants, push keys) under
// the same lock.
//
// Scaffold (G4-R2-S13): the signatures are final; the bodies are the credentials+session
// slice's (S10).
package credentials

import (
	"errors"
	"time"
)

// Names and version of the store on disk.
const (
	// FileName is the credential file in the data directory.
	FileName = "credentials.yaml"
	// LockName is the file the store holds an exclusive flock on while it is open.
	LockName = ".credentials.lock"
	// SchemaVersion is the version of the document this dash writes; a newer one is an error.
	SchemaVersion = 1
	// SaltSize is the length of the session salt.
	SaltSize = 32
)

// ErrLocked means another dash process holds the data directory's credential store.
var ErrLocked = errors.New("credentials store is locked by another dash instance")

// errSkeleton is what the scaffold's bodies return until the slice implements them.
var errSkeleton = errors.New("credentials: not implemented in the scaffold")

// Admin is the local administrator account (auth.mode=local).
type Admin struct {
	User string `json:"user"`
	// Hash is the argon2id PHC string of the password; never logged, never served.
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store is the open credential store. Its methods are safe for concurrent use.
type Store struct{}

// Open opens the store of dir. A writable dir gives a persistent store: it takes the flock
// (ErrLocked when another process holds it), reads credentials.yaml if it exists (a newer
// schemaVersion or a file that cannot be parsed is an error, never overwritten), and writes a new
// file with a fresh session salt when there is none. A missing, unwritable or empty dir gives an
// in-memory store with a random salt (Persistent is false). cmd/dash opens it only in the
// session modes, so proxy, none, dev and demo never create the file.
func Open(dir string) (*Store, error) { return nil, errSkeleton }

// Persistent reports whether the store is backed by credentials.yaml. An in-memory store forgets
// the account, the epochs and the salt at every start.
func (s *Store) Persistent() bool { return false }

// Path is the credential file, "" for an in-memory store.
func (s *Store) Path() string { return "" }

// Close releases the lock. Later writes fail.
func (s *Store) Close() error { return nil }

// Admin returns the stored administrator account, false when there is none.
func (s *Store) Admin() (Admin, bool) { return Admin{}, false }

// SetAdmin stores the administrator account, replacing the one before.
func (s *Store) SetAdmin(a Admin) error { return errSkeleton }

// Epoch is the session epoch of user, 0 when it was never bumped. A session cookie is valid
// only while its ep equals it.
func (s *Store) Epoch(user string) uint64 { return 0 }

// BumpEpoch increments the epoch of user and persists it: every session of that user dies.
func (s *Store) BumpEpoch(user string) error { return errSkeleton }

// GlobalEpoch is the epoch of every session. A session cookie is valid only while its ge
// equals it.
func (s *Store) GlobalEpoch() uint64 { return 0 }

// BumpGlobal increments the global epoch and persists it: every session of every user dies.
func (s *Store) BumpGlobal() error { return errSkeleton }

// SessionSalt is the random salt mixed into the session key derivation (design D8). It is
// created with the file, so deleting or restoring credentials.yaml kills every session. The
// caller must not modify the returned slice.
func (s *Store) SessionSalt() []byte { return nil }

// NoteEnvHash records the SHA-256 fingerprint of the hash DASH_ADMIN_PASSWORD_HASH supplies for
// user (never the hash itself) and, when a different fingerprint was recorded before, bumps the
// epoch of user: the "password change" of an env-managed account ends its sessions at the next
// start. changed reports that bump. The first fingerprint bumps nothing.
func (s *Store) NoteEnvHash(user, hash string) (changed bool, err error) { return false, errSkeleton }
