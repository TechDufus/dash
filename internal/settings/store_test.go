package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// diskValues decodes a store or backup file the way a fresh Open would.
func diskValues(t *testing.T, path string) map[string]any {
	t.Helper()
	doc, err := decode([]byte(read(t, path)), nil)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return doc.Values
}

// faultFS fails individual steps of the atomic write.
type faultFS struct {
	osFS
	renameErr func(oldpath, newpath string) error
	writeErr  error
	syncErr   error
}

func (f *faultFS) CreateTemp(dir, pattern string) (tempFile, error) {
	t, err := f.osFS.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return &faultFile{tempFile: t, fs: f}, nil
}

func (f *faultFS) Rename(oldpath, newpath string) error {
	if f.renameErr != nil {
		if err := f.renameErr(oldpath, newpath); err != nil {
			return err
		}
	}
	return f.osFS.Rename(oldpath, newpath)
}

type faultFile struct {
	tempFile
	fs *faultFS
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.fs.writeErr != nil {
		return 0, f.fs.writeErr
	}
	return f.tempFile.Write(p)
}

func (f *faultFile) Sync() error {
	if f.fs.syncErr != nil {
		return f.fs.syncErr
	}
	return f.tempFile.Sync()
}

func TestSecondInstanceIsRefused(t *testing.T) {
	dir := t.TempDir()
	first := open(t, dir, Options{})

	_, err := Open(dir, Options{})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open error = %v, want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "one dash instance") {
		t.Errorf("message does not say what to do: %v", err)
	}

	must(t, first.Close())
	again, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	again.Close()
}

func TestLockSurvivesWrites(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	before, err := os.Stat(filepath.Join(dir, lockName))
	must(t, err)
	for i := range 3 {
		must(t, s.Set("k", i))
	}
	after, err := os.Stat(filepath.Join(dir, lockName))
	must(t, err)
	if !os.SameFile(before, after) {
		t.Error(".lock was replaced; a lock on a replaced inode no longer excludes a second instance")
	}
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
		t.Errorf("Open after writes = %v, want ErrLocked", err)
	}
}

func TestReadOnlyStoresTakeNoLockAndRefuseWrites(t *testing.T) {
	t.Run("missing dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "absent")
		s := open(t, dir, Options{})
		if !s.ReadOnly() {
			t.Fatal("ReadOnly() = false for a missing dir")
		}
		if !errors.Is(s.Set("a", 1), ErrReadOnly) || !errors.Is(s.Delete("a"), ErrReadOnly) ||
			!errors.Is(s.SetScrubbed("a", 1), ErrReadOnly) || !errors.Is(s.DeleteScrubbed("a"), ErrReadOnly) {
			t.Error("a write did not return ErrReadOnly")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("read-only Open created the directory (stat err = %v)", err)
		}
	})

	t.Run("opts.ReadOnly", func(t *testing.T) {
		dir := t.TempDir()
		w := open(t, dir, Options{})
		must(t, w.Set("plugins.proxmox.node", "pve"))
		before := read(t, filepath.Join(dir, fileName))

		// The writer still holds the lock: a read-only store must not need it.
		s, err := Open(dir, Options{ReadOnly: true})
		if err != nil {
			t.Fatalf("read-only Open beside a writer: %v", err)
		}
		defer s.Close()
		if !s.ReadOnly() {
			t.Fatal("ReadOnly() = false")
		}
		if v, _ := s.Get("plugins.proxmox.node"); v != "pve" {
			t.Errorf("read-only store did not load the file: %v", v)
		}
		if err := s.Set("plugins.proxmox.node", "other"); !errors.Is(err, ErrReadOnly) {
			t.Errorf("Set error = %v, want ErrReadOnly", err)
		}
		if got := read(t, filepath.Join(dir, fileName)); got != before {
			t.Error("a read-only store changed the file")
		}
	})

	t.Run("opts.ReadOnly creates nothing", func(t *testing.T) {
		dir := t.TempDir()
		s := open(t, dir, Options{ReadOnly: true})
		if !s.ReadOnly() || len(names(t, dir)) != 0 {
			t.Errorf("ReadOnly=%v, dir has %v", s.ReadOnly(), names(t, dir))
		}
	})

	t.Run("unwritable dir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir := t.TempDir()
		w := open(t, dir, Options{})
		must(t, w.Set("a", "x"))
		must(t, w.Close())
		must(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { os.Chmod(dir, 0o700) })

		s := open(t, dir, Options{})
		if !s.ReadOnly() {
			t.Fatal("ReadOnly() = false for an unwritable dir")
		}
		if v, _ := s.Get("a"); v != "x" {
			t.Errorf("unwritable dir: stored value not loaded: %v", v)
		}
		if err := s.Set("a", "y"); !errors.Is(err, ErrReadOnly) {
			t.Errorf("Set error = %v, want ErrReadOnly", err)
		}
	})
}

func TestWriteInterruptedBeforeRenameLeavesOldFile(t *testing.T) {
	boom := errors.New("injected")
	cases := []struct {
		name  string
		fault func(f *faultFS, path string)
	}{
		{"write", func(f *faultFS, _ string) { f.writeErr = boom }},
		{"fsync", func(f *faultFS, _ string) { f.syncErr = boom }},
		{"rename", func(f *faultFS, path string) {
			f.renameErr = func(_, newpath string) error {
				if newpath == path {
					return boom
				}
				return nil
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ffs := &faultFS{}
			s := open(t, dir, Options{fs: ffs})
			must(t, s.Set("a", 1))
			must(t, s.Set("b", 2))
			path := filepath.Join(dir, fileName)
			old := read(t, path)
			rev := s.Revision()

			tc.fault(ffs, path)
			if err := s.Set("c", 3); !errors.Is(err, boom) {
				t.Fatalf("Set error = %v, want the injected fault", err)
			}
			if got := read(t, path); got != old {
				t.Errorf("file changed by an interrupted write:\n%s", got)
			}
			for _, n := range names(t, dir) {
				if strings.Contains(n, ".tmp-") {
					t.Errorf("temp file %s left behind", n)
				}
			}
			if _, ok := s.Get("c"); ok || s.Revision() != rev {
				t.Error("in-memory state advanced although the write failed")
			}

			// A new process sees the old state, and the store recovers once the fault clears.
			must(t, s.Close())
			fresh := open(t, dir, Options{})
			if _, ok := fresh.Get("c"); ok || fresh.Revision() != rev {
				t.Error("restart did not see the old file")
			}
			must(t, fresh.Set("c", 3))
			if v, _ := fresh.Get("c"); v != float64(3) {
				t.Errorf("retry Set: c = %v", v)
			}
		})
	}
}

func TestOlderSchemaIsMigratedOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	v1 := "schemaVersion: 1\nrevision: 7\nvalues:\n  proxmox:\n    node: pve\n"
	must(t, os.WriteFile(path, []byte(v1), 0o600))

	// Fake schema 2 moves proxmox under plugins, as a real rename would.
	calls := 0
	ms := []migration{func(doc map[string]any) error {
		calls++
		vals := doc["values"].(map[string]any)
		vals["plugins"] = map[string]any{"proxmox": vals["proxmox"]}
		delete(vals, "proxmox")
		return nil
	}}
	s := open(t, dir, Options{migrations: ms})

	if v, ok := s.Get("plugins.proxmox.node"); !ok || v != "pve" {
		t.Fatalf("migrated value = %v, %v", v, ok)
	}
	if _, ok := s.Get("proxmox.node"); ok {
		t.Error("old location still present after migration")
	}
	if s.Revision() != 7 {
		t.Errorf("Revision = %d, want 7 (kept across migration)", s.Revision())
	}
	if read(t, path) != v1 {
		t.Error("loading rewrote the file; migration must only be persisted by a real write")
	}

	must(t, s.Set("plugins.proxmox.url", "https://pve.example.com:8006"))
	disk := read(t, path)
	if !strings.Contains(disk, "schemaVersion: 2") || strings.Contains(disk, "\n  proxmox:") {
		t.Errorf("file not rewritten at schema 2:\n%s", disk)
	}
	if read(t, path+".bak.1") != v1 {
		t.Error("the pre-migration file was not kept as .bak.1")
	}

	must(t, s.Close())
	s = open(t, dir, Options{migrations: ms})
	if calls != 1 {
		t.Errorf("migration ran %d times, want once (a v2 file must not be migrated again)", calls)
	}
	if v, _ := s.Get("plugins.proxmox.node"); v != "pve" {
		t.Errorf("after reload: %v", v)
	}

	// A scrub reaches a backup still written at the old schema.
	must(t, s.DeleteScrubbed("plugins.proxmox.node"))
	bak := read(t, path+".bak.2") // the v1 file, rotated down by this write
	if strings.Contains(bak, "node:") {
		t.Errorf("old-schema backup not scrubbed:\n%s", bak)
	}
	if !strings.Contains(bak, "schemaVersion: 2") {
		t.Errorf("scrubbed backup was not migrated to the current schema:\n%s", bak)
	}
}

func TestUnreadableFilesAreErrorsNotOverwritten(t *testing.T) {
	cases := map[string]string{
		"newer schema":   "schemaVersion: 99\nvalues: {}\n",
		"no schema":      "values:\n  a: 1\n",
		"zero schema":    "schemaVersion: 0\n",
		"fractional":     "schemaVersion: 1.5\n",
		"not yaml":       "schemaVersion: [\n",
		"values not map": "schemaVersion: 1\nvalues: 3\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, fileName)
			must(t, os.WriteFile(path, []byte(content), 0o600))
			_, err := Open(dir, Options{})
			if err == nil || !strings.Contains(err.Error(), fileName) {
				t.Fatalf("Open error = %v", err)
			}
			if read(t, path) != content {
				t.Error("file was modified")
			}
			// The failed Open must release the lock.
			must(t, os.WriteFile(path, []byte("schemaVersion: 1\n"), 0o600))
			open(t, dir, Options{})
		})
	}
}

func TestEmptyFileIsEmptyStore(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, fileName), nil, 0o600))
	s := open(t, dir, Options{})
	must(t, s.Set("a", "b"))
	if v, _ := s.Get("a"); v != "b" {
		t.Errorf("a = %v", v)
	}
}

func TestBackupsRotateAndDropOldest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	s := open(t, dir, Options{})
	bak := func(n int) string { return fmt.Sprintf("%s.bak.%d", path, n) }

	must(t, s.Set("k", 1))
	if _, err := os.Stat(bak(1)); !os.IsNotExist(err) {
		t.Error("the first write has nothing to back up")
	}
	for i := 2; i <= 6; i++ {
		must(t, s.Set("k", i))
	}
	// Six writes: .bak.1 is the newest previous state (5), .bak.5 the oldest (1).
	for n := 1; n <= 5; n++ {
		if got := diskValues(t, bak(n))["k"]; got != float64(6-n) {
			t.Errorf("after 6 writes .bak.%d has k=%v, want %d", n, got, 6-n)
		}
	}

	must(t, s.Set("k", 7))
	for n := 1; n <= 5; n++ {
		if got := diskValues(t, bak(n))["k"]; got != float64(7-n) {
			t.Errorf("after 7 writes .bak.%d has k=%v, want %d", n, got, 7-n)
		}
	}
	if _, err := os.Stat(bak(6)); !os.IsNotExist(err) {
		t.Error(".bak.6 exists; only five backups are kept")
	}
	for _, n := range names(t, dir) {
		if strings.Contains(n, ".tmp-") {
			t.Errorf("temp file %s left behind", n)
		}
	}
}

func TestSetWritesOnlyThatKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	s := open(t, dir, Options{})

	must(t, s.Set("plugins.proxmox.node", "pve"))
	want := map[string]any{"plugins": map[string]any{"proxmox": map[string]any{"node": "pve"}}}
	if got := diskValues(t, path); !reflect.DeepEqual(got, want) {
		t.Errorf("file holds %v, want only the saved key %v", got, want)
	}

	must(t, s.Set("server.listen", "unused"))
	must(t, s.Set("plugins.proxmox.url", "https://pve.example.com"))
	if got := diskValues(t, path); !reflect.DeepEqual(got, map[string]any{
		"plugins": map[string]any{"proxmox": map[string]any{"node": "pve", "url": "https://pve.example.com"}},
		"server":  map[string]any{"listen": "unused"},
	}) {
		t.Errorf("sibling keys disturbed: %v", got)
	}

	must(t, s.Delete("plugins.proxmox.node"))
	must(t, s.Delete("server.listen"))
	got := diskValues(t, path)
	if _, ok := got["server"]; ok {
		t.Errorf("deleting the last key left an empty section: %v", got)
	}
	if !reflect.DeepEqual(got, map[string]any{"plugins": map[string]any{"proxmox": map[string]any{"url": "https://pve.example.com"}}}) {
		t.Errorf("Delete removed more than the key: %v", got)
	}
	must(t, s.Delete("plugins.proxmox.url"))
	if got := diskValues(t, path); len(got) != 0 {
		t.Errorf("store not empty after deleting every key: %v", got)
	}
}

func TestRevisionCountsWritesAndPersists(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	if s.Revision() != 0 {
		t.Fatalf("fresh Revision = %d", s.Revision())
	}
	must(t, s.Set("a", 1))
	must(t, s.Set("a", 2))
	must(t, s.Delete("a"))
	if s.Revision() != 3 {
		t.Errorf("Revision = %d, want 3", s.Revision())
	}

	// Writes that change nothing are not writes: the ETag must not move.
	must(t, s.Delete("a"))
	must(t, s.Set("b", []string{"x"}))
	must(t, s.Set("b", []string{"x"}))
	if s.Revision() != 4 {
		t.Errorf("Revision = %d after no-op writes, want 4", s.Revision())
	}

	must(t, s.Close())
	s = open(t, dir, Options{})
	if s.Revision() != 4 {
		t.Errorf("Revision after reopen = %d, want 4", s.Revision())
	}
}

func TestValuesAndGetAreDeepCopies(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	in := map[string]any{"hosts": []any{map[string]any{"nodename": "pve"}}}
	must(t, s.Set("plugins.hardware", in))
	in["hosts"].([]any)[0].(map[string]any)["nodename"] = "mutated-input"

	vals := s.Values()
	vals["plugins"].(map[string]any)["hardware"].(map[string]any)["hosts"].([]any)[0].(map[string]any)["nodename"] = "mutated-values"
	vals["injected"] = true

	got, ok := s.Get("plugins.hardware")
	if !ok {
		t.Fatal("plugins.hardware missing")
	}
	got.(map[string]any)["hosts"].([]any)[0].(map[string]any)["nodename"] = "mutated-get"

	want := map[string]any{"plugins": map[string]any{"hardware": map[string]any{"hosts": []any{map[string]any{"nodename": "pve"}}}}}
	if !reflect.DeepEqual(s.Values(), want) {
		t.Errorf("store aliased caller memory: %v", s.Values())
	}
}

func TestValuesRoundTripThroughDisk(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	must(t, s.Set("n", 8080))
	must(t, s.Set("b", true))
	must(t, s.Set("list", []string{"a", "b"}))
	before := s.Values()
	must(t, s.Close())
	if after := open(t, dir, Options{}).Values(); !reflect.DeepEqual(before, after) {
		t.Errorf("values changed across restart:\n%v\n%v", before, after)
	}
}

func TestInvalidWrites(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	for _, p := range []string{"", ".", "a..b", ".a", "a."} {
		if err := s.Set(p, 1); err == nil {
			t.Errorf("Set(%q) accepted an invalid path", p)
		}
		if err := s.Delete(p); err == nil {
			t.Errorf("Delete(%q) accepted an invalid path", p)
		}
		if _, ok := s.Get(p); ok {
			t.Errorf("Get(%q) found a value", p)
		}
	}
	if err := s.Set("a", nil); err == nil {
		t.Error("Set(nil) accepted; Delete is the way to reset")
	}
	if err := s.Set("a", make(chan int)); err == nil {
		t.Error("Set accepted an unserialisable value")
	}
	must(t, s.Set("a.b", 1))
	if err := s.Set("a.b.c", 1); err == nil {
		t.Error("Set under a scalar silently replaced it")
	}
	if v, _ := s.Get("a.b"); v != float64(1) {
		t.Errorf("a.b = %v after the refused write", v)
	}
}

func TestWritesAfterCloseFail(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	must(t, s.Set("a", 1))
	must(t, s.Close())
	must(t, s.Close())
	if err := s.Set("a", 2); !errors.Is(err, ErrClosed) {
		t.Errorf("Set after Close = %v, want ErrClosed", err)
	}
	if v, _ := s.Get("a"); v != float64(1) {
		t.Errorf("reads after Close: %v", v)
	}
}

func TestFileIsPrivateAndStaleTempsAreRemoved(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".ui-settings.yaml.tmp-12345")
	must(t, os.WriteFile(stale, []byte("half written"), 0o600))
	s := open(t, dir, Options{})
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a crashed write's temp file survived Open")
	}
	must(t, s.Set("a", 1))
	must(t, s.Set("a", 2))
	for _, n := range []string{fileName, fileName + ".bak.1"} {
		fi, err := os.Stat(filepath.Join(dir, n))
		must(t, err)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600: it holds encrypted secrets", n, fi.Mode().Perm())
		}
	}
}

func TestConcurrentWritesAreSerialised(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := s.Set(fmt.Sprintf("k.n%d", i), i); err != nil {
				t.Error(err)
			}
			s.Values()
		})
	}
	wg.Wait()
	if s.Revision() != 20 {
		t.Errorf("Revision = %d, want 20", s.Revision())
	}
	must(t, s.Close())
	if got := open(t, dir, Options{}).Values()["k"].(map[string]any); len(got) != 20 {
		t.Errorf("%d keys survived, want 20", len(got))
	}
}

// allFiles returns the content of every file in dir but the lock.
func allFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, n := range names(t, dir) {
		if n != lockName {
			out[n] = read(t, filepath.Join(dir, n))
		}
	}
	return out
}

func backupNames(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, n := range names(t, dir) {
		if strings.Contains(n, ".bak.") {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func assertAbsent(t *testing.T, dir, needle string) {
	t.Helper()
	for n, c := range allFiles(t, dir) {
		if strings.Contains(c, needle) {
			t.Errorf("%s still contains %q:\n%s", n, needle, c)
		}
	}
}

const secretPath = "plugins.proxmox.tokenSecret"

// seedBackups saves a secret, then four more writes, so the old secret sits in the live file and
// in three backups.
func seedBackups(t *testing.T, s *Store) {
	t.Helper()
	must(t, s.Set(secretPath, "enc:v1:OLDSECRET"))
	must(t, s.Set("plugins.proxmox.node", "pve"))
	must(t, s.Set("a", 1))
	must(t, s.Set("b", 2))
}

func TestPlainSetLeavesOldValueInBackups(t *testing.T) {
	// The control for the scrub tests: without them a backup restores a replaced secret.
	dir := t.TempDir()
	s := open(t, dir, Options{})
	seedBackups(t, s)
	must(t, s.Set(secretPath, "enc:v1:NEWSECRET"))
	found := false
	for n, c := range allFiles(t, dir) {
		if strings.Contains(n, ".bak.") && strings.Contains(c, "OLDSECRET") {
			found = true
		}
	}
	if !found {
		t.Fatal("no backup holds the old value; the scrub tests below would prove nothing")
	}
}

func TestSetScrubbedRemovesOldValueFromEveryBackup(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	seedBackups(t, s)
	// A backup that cannot be parsed cannot be proven clean.
	must(t, os.WriteFile(filepath.Join(dir, fileName+".bak.5"), []byte("garbage: [\nenc:v1:OLDSECRET"), 0o600))

	must(t, s.SetScrubbed(secretPath, "enc:v1:NEWSECRET"))

	assertAbsent(t, dir, "OLDSECRET")
	if v, _ := s.Get(secretPath); v != "enc:v1:NEWSECRET" {
		t.Errorf("live value = %v", v)
	}
	if !strings.Contains(read(t, filepath.Join(dir, fileName)), "NEWSECRET") {
		t.Error("new value not on disk")
	}

	// Backups still serve their purpose for everything else.
	if got := backupNames(t, dir); !reflect.DeepEqual(got, []string{fileName + ".bak.1", fileName + ".bak.2", fileName + ".bak.3", fileName + ".bak.4"}) {
		t.Errorf("backups = %v (the corrupt .bak.5 should have been removed, the rest kept)", got)
	}
	bak1 := diskValues(t, filepath.Join(dir, fileName+".bak.1"))
	if bak1["a"] != float64(1) || bak1["b"] != float64(2) {
		t.Errorf(".bak.1 lost unrelated values: %v", bak1)
	}
	if _, ok := bak1["plugins"].(map[string]any)["proxmox"].(map[string]any)["node"]; !ok {
		t.Errorf(".bak.1 lost the sibling key: %v", bak1)
	}
}

func TestDeleteScrubbedRemovesOldValueFromEveryBackup(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	seedBackups(t, s)

	must(t, s.DeleteScrubbed(secretPath))

	assertAbsent(t, dir, "OLDSECRET")
	if _, ok := s.Get(secretPath); ok {
		t.Error("secret still set")
	}
	if len(backupNames(t, dir)) != 4 {
		t.Errorf("backups = %v", backupNames(t, dir))
	}
}

func TestDeleteScrubbedReachesBackupsWhenLiveFileLacksTheKey(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	seedBackups(t, s)
	must(t, s.Delete(secretPath)) // plain: the old value stays in backups
	rev := s.Revision()

	must(t, s.DeleteScrubbed(secretPath))

	assertAbsent(t, dir, "OLDSECRET")
	if s.Revision() != rev {
		t.Error("scrubbing backups bumped the revision although the live file did not change")
	}
}

func TestSetScrubbedWithUnchangedValueStillScrubs(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	seedBackups(t, s)
	must(t, s.SetScrubbed(secretPath, "enc:v1:OLDSECRET"))
	// The same ciphertext is still in the live file by definition, but no backup may keep a copy.
	for n, c := range allFiles(t, dir) {
		if strings.Contains(n, ".bak.") && strings.Contains(c, "OLDSECRET") {
			t.Errorf("%s still holds the secret", n)
		}
	}
}
