package credentials

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	must(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// skipIfRoot skips tests that rely on a directory's mode bits refusing writes: root ignores them.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
}

func TestCredentialsFileIsPrivateAndHasNoBackup(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if !s.Persistent() || s.Path() != filepath.Join(dir, FileName) {
		t.Fatalf("Persistent=%v Path=%q, want a persistent store at %s", s.Persistent(), s.Path(), FileName)
	}
	const hash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"
	must(t, s.SetAdmin(Admin{User: "alice", Hash: hash, CreatedAt: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}))
	for range 3 { // several writes: a .bak rotation would show by now
		must(t, s.BumpEpoch("alice"))
	}
	must(t, s.BumpGlobal())

	fi, err := os.Stat(s.Path())
	must(t, err)
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode %v, want 0600", got)
	}
	data, err := os.ReadFile(s.Path())
	must(t, err)
	for _, want := range []string{"schemaVersion: 1", "epochs:", "alice: 3", "globalEpoch: 1", "sessionSalt:", "user: alice"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("credentials.yaml lacks %q:\n%s", want, data)
		}
	}
	if got, want := dirNames(t, dir), []string{LockName, FileName}; !slices.Equal(got, want) {
		t.Errorf("data dir holds %q, want only %q (no .bak, no temp, no probe)", got, want)
	}
}

func TestStoreSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	created := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	must(t, s.SetAdmin(Admin{User: "alice", Hash: "h1", CreatedAt: created}))
	must(t, s.BumpEpoch("alice"))
	must(t, s.BumpEpoch("bob"))
	must(t, s.BumpEpoch("bob"))
	must(t, s.BumpGlobal())
	salt := bytes.Clone(s.SessionSalt())
	must(t, s.Close())

	s = open(t, dir)
	if a, ok := s.Admin(); !ok || a.User != "alice" || a.Hash != "h1" || !a.CreatedAt.Equal(created) {
		t.Errorf("Admin() = %+v, %v", a, ok)
	}
	if s.Epoch("alice") != 1 || s.Epoch("bob") != 2 || s.Epoch("carol") != 0 || s.GlobalEpoch() != 1 {
		t.Errorf("epochs alice=%d bob=%d carol=%d global=%d, want 1 2 0 1", s.Epoch("alice"), s.Epoch("bob"), s.Epoch("carol"), s.GlobalEpoch())
	}
	if !bytes.Equal(s.SessionSalt(), salt) {
		t.Error("the session salt changed across a reopen: every session would die at restart")
	}
}

func TestNewFileGetsAFreshSalt(t *testing.T) {
	a, b := open(t, t.TempDir()), open(t, t.TempDir())
	if len(a.SessionSalt()) != SaltSize || len(b.SessionSalt()) != SaltSize {
		t.Fatalf("salt sizes %d, %d, want %d", len(a.SessionSalt()), len(b.SessionSalt()), SaltSize)
	}
	if bytes.Equal(a.SessionSalt(), b.SessionSalt()) {
		t.Error("two new stores share a salt")
	}
	// Deleting the file gives a new salt at the next Open (TestDeletingCredentialsFileKillsEverySession
	// in session proves that this ends every session).
	dir := t.TempDir()
	s := open(t, dir)
	old := bytes.Clone(s.SessionSalt())
	must(t, s.Close())
	must(t, os.Remove(filepath.Join(dir, FileName)))
	if s = open(t, dir); bytes.Equal(s.SessionSalt(), old) {
		t.Error("a deleted credentials.yaml came back with its old salt")
	}
}

func TestSecondOpenIsLocked(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open: %v, want ErrLocked", err)
	}
	must(t, s.Close())
	open(t, dir) // the lock went with Close
}

func TestConcurrentOpenTakesTheLockOnce(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	var (
		wg     sync.WaitGroup
		stores = make([]*Store, n)
		errs   = make([]error, n)
	)
	for i := range n {
		wg.Go(func() { stores[i], errs[i] = Open(dir) })
	}
	wg.Wait()
	won := 0
	for i := range n {
		switch {
		case errs[i] == nil:
			won++
			defer stores[i].Close()
		case !errors.Is(errs[i], ErrLocked):
			t.Errorf("Open %d: %v, want nil or ErrLocked", i, errs[i])
		}
	}
	if won != 1 {
		t.Fatalf("%d concurrent Opens took the lock, want exactly 1", won)
	}
}

func TestUnwritableOrMissingDirIsInMemory(t *testing.T) {
	readOnly := t.TempDir()
	must(t, os.Chmod(readOnly, 0o500))
	t.Cleanup(func() { os.Chmod(readOnly, 0o700) })
	cases := map[string]string{
		"empty":   "",
		"missing": filepath.Join(t.TempDir(), "nope"),
	}
	if os.Geteuid() != 0 {
		cases["read-only"] = readOnly
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			s := open(t, dir)
			if s.Persistent() || s.Path() != "" {
				t.Fatalf("Persistent=%v Path=%q, want in memory", s.Persistent(), s.Path())
			}
			if len(s.SessionSalt()) != SaltSize {
				t.Fatalf("salt is %d bytes", len(s.SessionSalt()))
			}
			must(t, s.SetAdmin(Admin{User: "alice", Hash: "h"}))
			must(t, s.BumpEpoch("alice"))
			must(t, s.BumpGlobal())
			if _, ok := s.Admin(); !ok || s.Epoch("alice") != 1 || s.GlobalEpoch() != 1 {
				t.Error("the in-memory store lost a write")
			}
			if dir == "" {
				return
			}
			if _, err := os.Stat(dir); err == nil {
				if names := dirNames(t, dir); len(names) != 0 {
					t.Errorf("an in-memory store wrote %q", names)
				}
			}
		})
	}
	// Two in-memory stores never share a salt: sessions end with the process.
	a, b := open(t, ""), open(t, "")
	if bytes.Equal(a.SessionSalt(), b.SessionSalt()) {
		t.Error("two in-memory stores share a salt")
	}
}

func TestUnreadableFileIsAnErrorAndIsKept(t *testing.T) {
	salt := strings.Repeat("A", 43) // 32 zero bytes, base64url
	for name, body := range map[string]string{
		"newer schema":   "schemaVersion: 2\nepochs: {}\nglobalEpoch: 0\nsessionSalt: " + salt + "\n",
		"no schema":      "epochs: {}\n",
		"not yaml":       "schemaVersion: [\n",
		"bad salt":       "schemaVersion: 1\nsessionSalt: short\n",
		"no salt":        "schemaVersion: 1\nepochs: {}\n",
		"admin w/o hash": "schemaVersion: 1\nsessionSalt: " + salt + "\nadmin: {user: alice}\n",
		"zero schema":    "schemaVersion: 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			must(t, os.WriteFile(path, []byte(body), 0o600))
			if s, err := Open(dir); err == nil {
				s.Close()
				t.Fatal("Open accepted an unreadable credentials.yaml")
			}
			got, err := os.ReadFile(path)
			must(t, err)
			if string(got) != body {
				t.Error("Open rewrote a file it could not read")
			}
			l, err := acquireLock(dir) // the failed Open released the lock
			must(t, err)
			l.Close()
		})
	}
}

func TestWideFileModeIsNarrowedAtOpen(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	must(t, s.Close())
	must(t, os.Chmod(filepath.Join(dir, FileName), 0o644))
	open(t, dir)
	fi, err := os.Stat(filepath.Join(dir, FileName))
	must(t, err)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v after Open, want 0600", fi.Mode().Perm())
	}
}

func TestStaleTempFilesAreRemovedAtOpen(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".credentials.yaml.tmp-12345")
	must(t, os.WriteFile(stale, []byte("half written"), 0o600))
	open(t, dir)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp file kept: %v", err)
	}
}

func TestNoteEnvHashBumpsOnlyOnAChange(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	const h1, h2 = "$argon2id$v=19$m=65536,t=3,p=4$Zmlyc3Q$Zmlyc3RoYXNo", "$argon2id$v=19$m=65536,t=3,p=4$c2Vjb25k$c2Vjb25kaGFzaA"
	step := func(hash string, wantChanged bool, wantEpoch uint64) {
		t.Helper()
		changed, err := s.NoteEnvHash("alice", hash)
		must(t, err)
		if changed != wantChanged || s.Epoch("alice") != wantEpoch {
			t.Fatalf("NoteEnvHash: changed=%v epoch=%d, want %v %d", changed, s.Epoch("alice"), wantChanged, wantEpoch)
		}
	}
	step(h1, false, 0) // the first fingerprint bumps nothing
	step(h1, false, 0) // the same hash at the next start
	must(t, s.Close())
	s = open(t, dir) // the fingerprint survives a restart
	step(h2, true, 1)
	step(h2, false, 1)
	if s.Epoch("bob") != 0 {
		t.Error("another user's epoch moved")
	}
	data, err := os.ReadFile(s.Path())
	must(t, err)
	for _, h := range []string{h1, h2, "Zmlyc3Q", "c2Vjb25k"} {
		if strings.Contains(string(data), h) {
			t.Errorf("credentials.yaml holds the env hash (or a part of it) %q, want only its fingerprint", h)
		}
	}
	if _, err := s.NoteEnvHash("alice", ""); err == nil {
		t.Error("NoteEnvHash accepted an empty hash")
	}
}

func TestSetAdminNeedsAUserAndAHash(t *testing.T) {
	s := open(t, t.TempDir())
	for _, a := range []Admin{{User: "alice"}, {Hash: "h"}} {
		if err := s.SetAdmin(a); err == nil {
			t.Errorf("SetAdmin(%+v) accepted", a)
		}
	}
	if _, ok := s.Admin(); ok {
		t.Error("a refused SetAdmin stored an account")
	}
}

func TestClosedStoreRefusesWrites(t *testing.T) {
	s := open(t, t.TempDir())
	must(t, s.Close())
	for name, err := range map[string]error{
		"SetAdmin":   s.SetAdmin(Admin{User: "alice", Hash: "h"}),
		"BumpEpoch":  s.BumpEpoch("alice"),
		"BumpGlobal": s.BumpGlobal(),
	} {
		if err == nil {
			t.Errorf("%s after Close succeeded", name)
		}
	}
}

// A failed write keeps an epoch bump in memory (revocation must hold in this process) but never
// an admin account that did not reach the disk.
func TestFailedWriteKeepsBumpsButNotTheAccount(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	s := open(t, dir)
	must(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := s.BumpEpoch("alice"); err == nil {
		t.Fatal("BumpEpoch reported success on a read-only directory")
	}
	if err := s.BumpGlobal(); err == nil {
		t.Fatal("BumpGlobal reported success on a read-only directory")
	}
	if s.Epoch("alice") != 1 || s.GlobalEpoch() != 1 {
		t.Errorf("epochs alice=%d global=%d after a failed write, want 1 1 (revocation holds in memory)", s.Epoch("alice"), s.GlobalEpoch())
	}
	if err := s.SetAdmin(Admin{User: "alice", Hash: "h"}); err == nil {
		t.Fatal("SetAdmin reported success on a read-only directory")
	}
	if _, ok := s.Admin(); ok {
		t.Error("an account that never reached the disk is served")
	}
}

func TestConcurrentBumpsAreAllCounted(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	const n = 20
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if err := s.BumpEpoch("alice"); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := s.BumpGlobal(); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() { _ = s.Epoch("alice") + s.GlobalEpoch() })
	}
	wg.Wait()
	if s.Epoch("alice") != n || s.GlobalEpoch() != n {
		t.Fatalf("alice=%d global=%d, want %d each", s.Epoch("alice"), s.GlobalEpoch(), n)
	}
	must(t, s.Close())
	if s = open(t, dir); s.Epoch("alice") != n || s.GlobalEpoch() != n {
		t.Fatalf("on disk alice=%d global=%d, want %d each", s.Epoch("alice"), s.GlobalEpoch(), n)
	}
}
