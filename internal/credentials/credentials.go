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
package credentials

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sigs.k8s.io/yaml"
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

// errClosed is returned by writes after Close.
var errClosed = errors.New("credentials: store is closed")

// tempPattern names the temporary files of the atomic write; Open removes the ones a crash left.
const tempPattern = ".credentials.yaml.tmp-*"

const header = "# Managed by dash: the local administrator account and the session epochs. Do not edit.\n" +
	"# Deleting this file ends every session and reopens /setup.\n"

// Admin is the local administrator account (auth.mode=local).
type Admin struct {
	User string `json:"user"`
	// Hash is the argon2id PHC string of the password; never logged, never served.
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
}

// document is the on-disk shape of credentials.yaml.
type document struct {
	SchemaVersion int    `json:"schemaVersion"`
	Admin         *Admin `json:"admin,omitempty"`
	// EnvHashes are the hex SHA-256 fingerprints of the DASH_ADMIN_PASSWORD_HASH values seen per
	// user, never the hashes themselves.
	EnvHashes   map[string]string `json:"envHashes,omitempty"`
	Epochs      map[string]uint64 `json:"epochs"`
	GlobalEpoch uint64            `json:"globalEpoch"`
	// SessionSalt is base64url (no padding) of SaltSize random bytes.
	SessionSalt string `json:"sessionSalt"`
}

// Store is the open credential store. Its methods are safe for concurrent use.
type Store struct {
	dir, path string // both "" for an in-memory store

	mu     sync.RWMutex
	lock   *os.File // the flock holder; nil in memory and after Close
	closed bool
	doc    document
	salt   []byte
}

// Open opens the store of dir. A writable dir gives a persistent store: it takes the flock
// (ErrLocked when another process holds it), reads credentials.yaml if it exists (a newer
// schemaVersion or a file that cannot be parsed is an error, never overwritten), and writes a new
// file with a fresh session salt when there is none. A missing, unwritable or empty dir gives an
// in-memory store with a random salt (Persistent is false). cmd/dash opens it only in the
// session modes, so proxy, none, dev and demo never create the file.
func Open(dir string) (*Store, error) {
	if !writable(dir) {
		salt, err := newSalt()
		if err != nil {
			return nil, err
		}
		return &Store{doc: emptyDocument(salt), salt: salt}, nil
	}
	lock, err := acquireLock(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, path: filepath.Join(dir, FileName), lock: lock}
	removeStaleTemps(dir)
	if err := s.load(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// writable reports whether dir exists and accepts new files. Creating a probe is the only
// reliable test: mode bits ignore read-only mounts and ACLs.
func writable(dir string) bool {
	if dir == "" {
		return false
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".credentials-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	return os.Remove(f.Name()) == nil
}

// removeStaleTemps deletes temp files a crash left between create and rename. The caller holds
// the lock, so none belongs to a live writer.
func removeStaleTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if ok, _ := filepath.Match(tempPattern, e.Name()); ok {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func newSalt() ([]byte, error) {
	salt := make([]byte, SaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("credentials: session salt: %w", err)
	}
	return salt, nil
}

func emptyDocument(salt []byte) document {
	return document{
		SchemaVersion: SchemaVersion,
		Epochs:        map[string]uint64{},
		SessionSalt:   base64.RawURLEncoding.EncodeToString(salt),
	}
}

// load reads the file, or creates it with a fresh salt when there is none (or it is empty).
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("credentials: read %s: %w", s.path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		salt, err := newSalt()
		if err != nil {
			return err
		}
		doc := emptyDocument(salt)
		if _, err := s.persist(doc); err != nil {
			return err
		}
		s.doc, s.salt = doc, salt
		return nil
	}
	doc, salt, err := decode(data)
	if err != nil {
		return fmt.Errorf("credentials: %s: %w", s.path, err)
	}
	// A file written by hand or restored with a wider mode is narrowed: it holds a password hash.
	if fi, err := os.Stat(s.path); err == nil && fi.Mode().Perm() != 0o600 {
		if err := os.Chmod(s.path, 0o600); err != nil {
			return fmt.Errorf("credentials: chmod %s: %w", s.path, err)
		}
	}
	s.doc, s.salt = doc, salt
	return nil
}

// decode parses and checks a document. It never repairs one: the caller refuses to start rather
// than overwrite a file it cannot read.
func decode(data []byte) (document, []byte, error) {
	j, err := yaml.YAMLToJSON(data)
	if err != nil {
		return document{}, nil, fmt.Errorf("parse: %w", err)
	}
	var probe struct {
		SchemaVersion *json.Number `json:"schemaVersion"`
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.UseNumber()
	if err := dec.Decode(&probe); err != nil {
		return document{}, nil, fmt.Errorf("parse: %w", err)
	}
	if probe.SchemaVersion == nil {
		return document{}, nil, errors.New("schemaVersion is missing")
	}
	v, err := probe.SchemaVersion.Int64()
	if err != nil || v < 1 {
		return document{}, nil, errors.New("schemaVersion is not a positive integer")
	}
	if v > SchemaVersion {
		return document{}, nil, fmt.Errorf("schemaVersion %d is newer than this dash understands (%d); upgrade dash", v, SchemaVersion)
	}
	var doc document
	if err := json.Unmarshal(j, &doc); err != nil {
		return document{}, nil, fmt.Errorf("parse: %w", err)
	}
	salt, err := base64.RawURLEncoding.Strict().DecodeString(doc.SessionSalt)
	if err != nil || len(salt) != SaltSize {
		return document{}, nil, fmt.Errorf("sessionSalt must be %d bytes of base64url", SaltSize)
	}
	if doc.Admin != nil && (doc.Admin.User == "" || doc.Admin.Hash == "") {
		return document{}, nil, errors.New("admin needs a user and a hash")
	}
	if doc.Epochs == nil {
		doc.Epochs = map[string]uint64{}
	}
	return doc, salt, nil
}

func encode(doc document) ([]byte, error) {
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(header), b...), nil
}

// persist writes doc atomically: a 0600 temp file in the same directory, fsync, rename over
// the file, fsync of the directory. committed reports that the rename happened (a later
// directory fsync error still leaves the new file in place).
func (s *Store) persist(doc document) (committed bool, err error) {
	data, err := encode(doc)
	if err != nil {
		return false, fmt.Errorf("credentials: encode: %w", err)
	}
	f, err := os.CreateTemp(s.dir, tempPattern) // mode 0600
	if err != nil {
		return false, fmt.Errorf("credentials: create temp file: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return false, fmt.Errorf("credentials: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return false, fmt.Errorf("credentials: fsync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("credentials: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("credentials: replace %s: %w", s.path, err)
	}
	if err := syncDir(s.dir); err != nil {
		return true, fmt.Errorf("credentials: sync %s: %w", s.dir, err)
	}
	return true, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}

// update applies change to a copy of the document and persists it. The copy becomes the
// document when the write committed, or always for an in-memory store. With keepOnFailure the
// change is applied in memory even when the write fails: epoch bumps only revoke, so this
// process must honour them whether or not they reached the disk.
func (s *Store) update(keepOnFailure bool, change func(*document) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	next := s.doc
	next.EnvHashes = maps.Clone(s.doc.EnvHashes)
	next.Epochs = maps.Clone(s.doc.Epochs)
	if next.Epochs == nil {
		next.Epochs = map[string]uint64{}
	}
	if next.Admin != nil {
		a := *next.Admin
		next.Admin = &a
	}
	if !change(&next) {
		return nil
	}
	if s.path == "" {
		s.doc = next
		return nil
	}
	committed, err := s.persist(next)
	if committed || keepOnFailure {
		s.doc = next
	}
	return err
}

// Persistent reports whether the store is backed by credentials.yaml. An in-memory store forgets
// the account, the epochs and the salt at every start.
func (s *Store) Persistent() bool { return s.path != "" }

// Path is the credential file, "" for an in-memory store.
func (s *Store) Path() string { return s.path }

// Close releases the lock. Later writes fail.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}

// Admin returns the stored administrator account, false when there is none.
func (s *Store) Admin() (Admin, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.doc.Admin == nil {
		return Admin{}, false
	}
	return *s.doc.Admin, true
}

// SetAdmin stores the administrator account, replacing the one before.
func (s *Store) SetAdmin(a Admin) error {
	if a.User == "" || a.Hash == "" {
		return errors.New("credentials: the admin account needs a user and a hash")
	}
	return s.update(false, func(d *document) bool {
		d.Admin = &a
		return true
	})
}

// Epoch is the session epoch of user, 0 when it was never bumped. A session cookie is valid
// only while its ep equals it.
func (s *Store) Epoch(user string) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.Epochs[user]
}

// BumpEpoch increments the epoch of user and persists it: every session of that user dies. The
// bump applies to this process even when the write fails (the error says so).
func (s *Store) BumpEpoch(user string) error {
	return s.update(true, func(d *document) bool {
		d.Epochs[user]++
		return true
	})
}

// GlobalEpoch is the epoch of every session. A session cookie is valid only while its ge
// equals it.
func (s *Store) GlobalEpoch() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.GlobalEpoch
}

// BumpGlobal increments the global epoch and persists it: every session of every user dies. The
// bump applies to this process even when the write fails (the error says so).
func (s *Store) BumpGlobal() error {
	return s.update(true, func(d *document) bool {
		d.GlobalEpoch++
		return true
	})
}

// SessionSalt is the random salt mixed into the session key derivation (design D8). It is
// created with the file, so deleting or restoring credentials.yaml kills every session. The
// caller must not modify the returned slice.
func (s *Store) SessionSalt() []byte { return s.salt }

// NoteEnvHash records the SHA-256 fingerprint of the hash DASH_ADMIN_PASSWORD_HASH supplies for
// user (never the hash itself) and, when a different fingerprint was recorded before, bumps the
// epoch of user: the "password change" of an env-managed account ends its sessions at the next
// start. changed reports that bump. The first fingerprint bumps nothing.
func (s *Store) NoteEnvHash(user, hash string) (changed bool, err error) {
	if user == "" || hash == "" {
		return false, errors.New("credentials: NoteEnvHash needs a user and a hash")
	}
	sum := sha256.Sum256([]byte(hash))
	fp := hex.EncodeToString(sum[:])
	err = s.update(true, func(d *document) bool {
		prev, seen := d.EnvHashes[user]
		if seen && subtle.ConstantTimeCompare([]byte(prev), []byte(fp)) == 1 {
			return false
		}
		if d.EnvHashes == nil {
			d.EnvHashes = map[string]string{}
		}
		d.EnvHashes[user] = fp
		if seen {
			d.Epochs[user]++
			changed = true
		}
		return true
	})
	return changed, err
}
