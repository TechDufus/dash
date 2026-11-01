// Package settings is the UI settings store: a sparse YAML file, $DASH_DATA_DIR/ui-settings.yaml,
// holding only the keys an admin saved from the UI (design B.7). It is the lowest config layer;
// file and env values are never copied into it.
//
// A writable store owns the data directory through an flock on a separate .lock file. When the
// directory is missing or unwritable, or Options.ReadOnly is set, the store is read-only: it
// loads the file if it can, never creates or locks anything, and every write returns ErrReadOnly.
package settings

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
)

const (
	fileName    = "ui-settings.yaml"
	lockName    = ".lock"
	tempPattern = "." + fileName + ".tmp-*"
	backups     = 5
)

var (
	// ErrLocked means another dash process holds the data directory.
	ErrLocked = errors.New("settings store is locked by another dash instance")
	// ErrReadOnly means the store was opened read-only; the API layer answers 409.
	ErrReadOnly = errors.New("settings store is read-only")
	// ErrClosed is returned by writes after Close.
	ErrClosed = errors.New("settings store is closed")
)

// Options configure Open. The unexported fields are test seams.
type Options struct {
	// ReadOnly forces a read-only store (settings.ui: readonly, the GitOps opt-in).
	ReadOnly bool

	fs         fileSystem
	migrations []migration
}

// Store is safe for concurrent use.
type Store struct {
	dir, path  string
	fs         fileSystem
	migrations []migration
	readOnly   bool

	mu     sync.Mutex
	lock   *os.File // nil when read-only or closed
	closed bool
	values map[string]any
	rev    uint64
	raw    []byte // bytes currently on disk; the source of the next .bak.1
}

// Open loads dir/ui-settings.yaml. A missing or unwritable dir yields a read-only store, not an
// error; a corrupt file, a schemaVersion newer than this build, or a lock held elsewhere
// (ErrLocked) is an error, so dash never overwrites data it cannot read.
func Open(dir string, opts Options) (*Store, error) {
	s := &Store{dir: dir, path: filepath.Join(dir, fileName), fs: opts.fs, migrations: opts.migrations}
	if s.fs == nil {
		s.fs = osFS{}
	}
	if s.migrations == nil {
		s.migrations = migrations
	}
	s.readOnly = opts.ReadOnly || !s.writable()
	if !s.readOnly {
		lock, err := acquireLock(dir)
		if err != nil {
			return nil, err
		}
		s.lock = lock
		s.removeStaleTemps()
	}
	if err := s.load(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// writable reports whether dir exists and accepts new files. Creating the probe is the only
// reliable test: mode bits ignore read-only mounts and ACLs.
func (s *Store) writable() bool {
	if s.dir == "" {
		return false
	}
	if fi, err := os.Stat(s.dir); err != nil || !fi.IsDir() {
		return false
	}
	f, err := s.fs.CreateTemp(s.dir, ".probe-*")
	if err != nil {
		return false
	}
	f.Close()
	return s.fs.Remove(f.Name()) == nil
}

// removeStaleTemps deletes temp files a crash left between create and rename. We hold the lock,
// so none belongs to a live writer.
func (s *Store) removeStaleTemps() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if ok, _ := filepath.Match(tempPattern, e.Name()); ok {
			s.fs.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

func (s *Store) load() error {
	data, err := s.fs.ReadFile(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		data = nil
	case err != nil && s.readOnly && errors.Is(err, fs.ErrPermission):
		// A read-only store with an unreadable file serves defaults; nothing can be lost.
		data = nil
	case err != nil:
		return fmt.Errorf("settings: read %s: %w", s.path, err)
	}
	doc, err := decode(data, s.migrations)
	if err != nil {
		return fmt.Errorf("settings: %s: %w", s.path, err)
	}
	s.values, s.rev, s.raw = doc.Values, doc.Revision, data
	return nil
}

// ReadOnly reports whether writes are refused.
func (s *Store) ReadOnly() bool { return s.readOnly }

// Path is the store file, for config provenance.
func (s *Store) Path() string { return s.path }

// Revision counts writes and is persisted, so it is stable across restarts and usable as an ETag.
func (s *Store) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev
}

// Values returns a deep copy of the sparse tree for the config UI layer. Numbers are float64,
// as after any YAML load.
func (s *Store) Values() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMap(s.values)
}

// Get returns a deep copy of the value at a dotted path.
func (s *Store) Get(path string) (any, bool) {
	segs, err := splitPath(path)
	if err != nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := lookup(s.values, segs)
	return clone(v), ok
}

// Set stores v at a dotted path and writes only that key. Saving a value equal to the stored
// one is not a write.
func (s *Store) Set(path string, v any) error { return s.set(path, v, false) }

// Delete removes the key ("reset to default"); a missing key is not a write.
func (s *Store) Delete(path string) error { return s.delete(path, false) }

// SetScrubbed is Set for secrets: besides replacing the value it removes the path from every
// retained .bak.N, so restoring a backup cannot bring back the replaced secret.
func (s *Store) SetScrubbed(path string, v any) error { return s.set(path, v, true) }

// DeleteScrubbed is Delete for secrets, with the same backup scrubbing as SetScrubbed.
func (s *Store) DeleteScrubbed(path string) error { return s.delete(path, true) }

func (s *Store) set(path string, v any, scrub bool) error {
	segs, err := splitPath(path)
	if err != nil {
		return err
	}
	if v == nil {
		return fmt.Errorf("settings: cannot store nil at %q; use Delete", path)
	}
	nv, err := normalize(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	if cur, ok := lookup(s.values, segs); ok && reflect.DeepEqual(cur, nv) && !scrub {
		return nil
	}
	next := cloneMap(s.values)
	if err := setIn(next, segs, nv); err != nil {
		return err
	}
	return s.commit(next, segs, scrub)
}

func (s *Store) delete(path string, scrub bool) error {
	segs, err := splitPath(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	next := cloneMap(s.values)
	if !deleteIn(next, segs) {
		if scrub {
			// Not in the live file, but a backup may still hold it.
			return s.scrubBackups(segs)
		}
		return nil
	}
	return s.commit(next, segs, scrub)
}

func (s *Store) writableLocked() error {
	switch {
	case s.closed:
		return ErrClosed
	case s.readOnly:
		return ErrReadOnly
	}
	return nil
}

// commit writes next as the new document. In-memory state advances only if the file was replaced,
// so a failed write leaves the store exactly as it was.
func (s *Store) commit(next map[string]any, segs []string, scrub bool) error {
	backup := s.raw
	if scrub {
		// Scrub the retained backups first, and the bytes about to become .bak.1, so the old
		// value is never on disk in a backup, even for a moment, after the new one is written.
		if err := s.scrubBackups(segs); err != nil {
			return err
		}
		var err error
		if backup, err = s.scrubRaw(backup, segs); err != nil {
			return err
		}
	}
	rev := s.rev + 1
	data, err := encode(document{SchemaVersion: currentVersion(s.migrations), Revision: rev, Values: next})
	if err != nil {
		return err
	}
	committed, err := s.persist(data, backup)
	if committed {
		s.values, s.rev, s.raw = next, rev, data
	}
	return err
}

func (s *Store) backupName(n int) string { return s.path + ".bak." + strconv.Itoa(n) }

// persist writes data atomically: temp file in the same directory, fsync, rotate backups, rename
// over the store file, fsync the directory. The temp file is complete and durable before
// anything else changes, so an error before the rename leaves the old file untouched. committed
// reports whether the rename happened (a later directory fsync error is still an error).
func (s *Store) persist(data, backup []byte) (committed bool, err error) {
	tmp, err := s.writeTemp(data)
	if err != nil {
		return false, err
	}
	if len(backup) > 0 {
		if err := s.rotate(backup); err != nil {
			s.fs.Remove(tmp)
			return false, err
		}
	}
	if err := s.fs.Rename(tmp, s.path); err != nil {
		s.fs.Remove(tmp)
		return false, fmt.Errorf("settings: replace %s: %w", s.path, err)
	}
	if err := s.fs.SyncDir(s.dir); err != nil {
		return true, fmt.Errorf("settings: sync %s: %w", s.dir, err)
	}
	return true, nil
}

// writeTemp writes data to a fsynced temp file in the store directory and returns its name.
func (s *Store) writeTemp(data []byte) (string, error) {
	f, err := s.fs.CreateTemp(s.dir, tempPattern)
	if err != nil {
		return "", fmt.Errorf("settings: create temp file: %w", err)
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		s.fs.Remove(name)
		return "", fmt.Errorf("settings: write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		s.fs.Remove(name)
		return "", fmt.Errorf("settings: fsync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		s.fs.Remove(name)
		return "", fmt.Errorf("settings: close %s: %w", name, err)
	}
	return name, nil
}

// replaceFile atomically replaces name with data (temp, fsync, rename); the caller syncs the dir.
func (s *Store) replaceFile(name string, data []byte) error {
	tmp, err := s.writeTemp(data)
	if err != nil {
		return err
	}
	if err := s.fs.Rename(tmp, name); err != nil {
		s.fs.Remove(tmp)
		return fmt.Errorf("settings: replace %s: %w", name, err)
	}
	return nil
}

// rotate shifts .bak.1 to .bak.2 and so on, dropping .bak.5, then stores backup as the new
// .bak.1. Backups are only ever whole files, so a crash mid-rotation loses at most the oldest.
func (s *Store) rotate(backup []byte) error {
	if err := s.fs.Remove(s.backupName(backups)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("settings: drop oldest backup: %w", err)
	}
	for i := backups - 1; i >= 1; i-- {
		if err := s.fs.Rename(s.backupName(i), s.backupName(i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("settings: rotate backup %d: %w", i, err)
		}
	}
	return s.replaceFile(s.backupName(1), backup)
}

// scrubRaw returns data with the path removed, re-encoded at the current schema. Going through
// decode means a backup written under an older schema is migrated before the path is looked up.
func (s *Store) scrubRaw(data []byte, segs []string) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	doc, err := decode(data, s.migrations)
	if err != nil {
		return nil, err
	}
	deleteIn(doc.Values, segs)
	return encode(doc)
}

// scrubBackups removes the path from every retained .bak.N. A backup that cannot be parsed
// cannot be proven clean, so it is deleted rather than left holding a retired secret.
func (s *Store) scrubBackups(segs []string) error {
	for i := 1; i <= backups; i++ {
		name := s.backupName(i)
		data, err := s.fs.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("settings: read backup %s: %w", name, err)
		}
		clean, err := s.scrubRaw(data, segs)
		if err != nil {
			if err := s.fs.Remove(name); err != nil {
				return fmt.Errorf("settings: remove unreadable backup %s: %w", name, err)
			}
			continue
		}
		if err := s.replaceFile(name, clean); err != nil {
			return err
		}
	}
	if err := s.fs.SyncDir(s.dir); err != nil {
		return fmt.Errorf("settings: sync %s: %w", s.dir, err)
	}
	return nil
}

// Close releases the directory lock. Writes fail with ErrClosed afterwards; reads keep working
// on the last state. Close is idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close() // closing the descriptor drops the flock
	s.lock = nil
	return err
}
