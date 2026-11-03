package settings

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestApplyIsOneCommitAndOneRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	replaced := 0
	fsys := &faultFS{renameErr: func(_, newpath string) error {
		if newpath == path {
			replaced++
		}
		return nil
	}}
	s := open(t, dir, Options{fs: fsys})
	must(t, s.Set("plugins.proxmox.node", "pve"))
	must(t, s.Set("old", 1))
	before, rev := s.Values(), s.Revision()
	replaced = 0

	sets := map[string]any{
		"plugins.proxmox.node": "pve2",
		"plugins.proxmox.url":  "https://pve.example.com",
		"plugins.gatus.url":    "https://gatus.example.com",
	}
	_, err := s.Apply(sets, []string{"old"}, nil)
	must(t, err)

	if replaced != 1 {
		t.Errorf("the store file was replaced %d times, want once for the whole write", replaced)
	}
	if s.Revision() != rev+1 {
		t.Errorf("Revision = %d, want %d: one write is one revision", s.Revision(), rev+1)
	}
	want := map[string]any{"plugins": map[string]any{
		"proxmox": map[string]any{"node": "pve2", "url": "https://pve.example.com"},
		"gatus":   map[string]any{"url": "https://gatus.example.com"},
	}}
	if got := diskValues(t, path); !reflect.DeepEqual(got, want) {
		t.Errorf("file holds %v, want %v", got, want)
	}
	if got := s.Values(); !reflect.DeepEqual(got, want) {
		t.Errorf("Values = %v, want %v", got, want)
	}
	// The previous state is one backup, not a series of half-applied ones.
	if got := diskValues(t, path+".bak.1"); !reflect.DeepEqual(got, before) {
		t.Errorf(".bak.1 = %v, want the state before the write %v", got, before)
	}

	// Writing the same values again changes nothing, so it writes nothing.
	files, rev := allFiles(t, dir), s.Revision()
	replaced = 0
	_, err = s.Apply(sets, []string{"old"}, nil)
	must(t, err)
	if replaced != 0 || s.Revision() != rev || !maps.Equal(allFiles(t, dir), files) {
		t.Errorf("a write that changes nothing replaced the file %d times, revision %d -> %d", replaced, rev, s.Revision())
	}
}

func TestApplyFailsAtomically(t *testing.T) {
	boom := errors.New("injected")
	cases := []struct {
		name    string
		sets    map[string]any
		deletes []string
		scrub   []string
		fail    bool // the file write fails
	}{
		{name: "invalid set path", sets: map[string]any{"ok.k": 1, "a..c": 1}},
		{name: "invalid delete path", sets: map[string]any{"ok.k": 1}, deletes: []string{"."}},
		{name: "invalid scrub path", sets: map[string]any{"ok.k": 1}, scrub: []string{"x."}},
		{name: "nil value", sets: map[string]any{"ok.k": 1, "n": nil}},
		{name: "unserialisable value", sets: map[string]any{"ok.k": 1, "ch": make(chan int)}},
		{name: "set and delete", sets: map[string]any{"ok.k": 1}, deletes: []string{"ok.k"}},
		{name: "deleted twice", deletes: []string{"keep", "keep"}},
		{name: "section and its key", sets: map[string]any{"ok": map[string]any{"k": 1}, "ok.k": 2}},
		{name: "deleted section of a set key", sets: map[string]any{"ok.k": 1}, deletes: []string{"ok"}},
		{name: "key under a stored scalar", sets: map[string]any{"ok.k": 1, "a.b.c": 2}},
		{name: "file write fails", sets: map[string]any{"ok.k": 1}, deletes: []string{"keep"}, fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fsys := &faultFS{}
			s := open(t, dir, Options{fs: fsys})
			must(t, s.Set("a.b", 1))
			must(t, s.Set("keep", "x"))
			files, rev, values := allFiles(t, dir), s.Revision(), s.Values()
			if tc.fail {
				fsys.writeErr = boom
			}

			prev, err := s.Apply(tc.sets, tc.deletes, tc.scrub)
			if err == nil {
				t.Fatal("Apply accepted the write")
			}
			if !reflect.DeepEqual(prev, Snapshot{}) {
				t.Errorf("a refused write returned a snapshot: %+v", prev)
			}
			if s.Revision() != rev || !reflect.DeepEqual(s.Values(), values) {
				t.Errorf("store changed: revision %d -> %d, values %v", rev, s.Revision(), s.Values())
			}
			if got := allFiles(t, dir); !maps.Equal(got, files) {
				t.Errorf("files changed:\n%v\nwant\n%v", got, files)
			}
		})
	}
}

func TestApplyScrubsBackups(t *testing.T) {
	const other = "plugins.grafana.token"
	dir := t.TempDir()
	s := open(t, dir, Options{})
	seedBackups(t, s)
	must(t, s.Set(other, "enc:v1:OTHEROLD"))
	rev := s.Revision()

	_, err := s.Apply(map[string]any{secretPath: "enc:v1:NEWSECRET", "plugins.proxmox.node": "pve2"}, []string{other}, []string{secretPath, other})
	must(t, err)

	assertAbsent(t, dir, "OLDSECRET")
	assertAbsent(t, dir, "OTHEROLD")
	if s.Revision() != rev+1 {
		t.Errorf("Revision = %d, want %d", s.Revision(), rev+1)
	}
	if !strings.Contains(read(t, filepath.Join(dir, fileName)), "NEWSECRET") {
		t.Error("new value not on disk")
	}
	if bak1 := diskValues(t, filepath.Join(dir, fileName+".bak.1")); bak1["a"] != float64(1) || bak1["b"] != float64(2) {
		t.Errorf(".bak.1 lost unrelated values: %v", bak1)
	}

	// Scrubbing an unchanged value reaches the backups without a new revision.
	must(t, s.Set(secretPath, "enc:v1:THIRDSECRET")) // plain: NEWSECRET stays in .bak.1
	rev = s.Revision()
	_, err = s.Apply(map[string]any{secretPath: "enc:v1:THIRDSECRET"}, nil, []string{secretPath})
	must(t, err)
	assertAbsent(t, dir, "NEWSECRET")
	if s.Revision() != rev {
		t.Errorf("a scrub of an unchanged store bumped the revision to %d", s.Revision())
	}
}

func TestApplyOnReadOnlyStoreRefuses(t *testing.T) {
	dir := t.TempDir()
	w := open(t, dir, Options{})
	must(t, w.Set("a", 1))
	must(t, w.Set(secretPath, "enc:v1:OLDSECRET"))
	must(t, w.Set("b", 2)) // a backup holds the secret
	must(t, w.Close())

	ro := open(t, dir, Options{ReadOnly: true})
	files := allFiles(t, dir)
	for name, write := range map[string]func() error{
		"set":        func() error { _, err := ro.Apply(map[string]any{"a": 2}, nil, nil); return err },
		"delete":     func() error { _, err := ro.Apply(nil, []string{"a"}, nil); return err },
		"scrub only": func() error { _, err := ro.Apply(nil, nil, []string{secretPath}); return err },
		"no-op":      func() error { _, err := ro.Apply(nil, nil, nil); return err },
		"revert":     func() error { return ro.Revert(Snapshot{}) },
	} {
		if err := write(); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s on a read-only store = %v, want ErrReadOnly", name, err)
		}
	}
	if got := allFiles(t, dir); !maps.Equal(got, files) {
		t.Errorf("a read-only store wrote:\n%v", got)
	}
	if v, _ := ro.Get("a"); v != float64(1) {
		t.Errorf("a = %v", v)
	}

	closed := open(t, t.TempDir(), Options{})
	must(t, closed.Close())
	if _, err := closed.Apply(map[string]any{"a": 1}, nil, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("Apply after Close = %v, want ErrClosed", err)
	}
	if err := closed.Revert(Snapshot{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Revert after Close = %v, want ErrClosed", err)
	}
}

func TestApplyConcurrentWithSet(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	const n = 10
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			key := fmt.Sprintf("multi.n%d", i)
			if _, err := s.Apply(map[string]any{key + ".a": i, key + ".b": i}, nil, nil); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := s.Set(fmt.Sprintf("single.n%d", i), i); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			// A reader sees both keys of an Apply or neither.
			for k, v := range asMap(s.Values()["multi"]) {
				if len(asMap(v)) != 2 {
					t.Errorf("multi.%s half applied: %v", k, v)
				}
			}
		})
	}
	wg.Wait()
	if s.Revision() != 2*n {
		t.Errorf("Revision = %d, want %d", s.Revision(), 2*n)
	}
	must(t, s.Close())
	got := open(t, dir, Options{}).Values()
	if len(asMap(got["multi"])) != n || len(asMap(got["single"])) != n {
		t.Errorf("after reopen: %v", got)
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func TestRevertRestoresPreviousValuesAndScrubsSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	s := open(t, dir, Options{})
	must(t, s.Set("plugins.proxmox.node", "pve"))
	must(t, s.Set("plugins.proxmox.url", "https://a.example.com"))
	must(t, s.SetScrubbed(secretPath, "enc:v1:OLDSECRET"))
	before, rev := s.Values(), s.Revision()

	prev, err := s.Apply(map[string]any{
		"plugins.proxmox.url": "https://b.example.com",
		secretPath:            "enc:v1:NEWSECRET",
		"plugins.gatus.url":   "https://gatus.example.com", // did not exist
	}, []string{"plugins.proxmox.node"}, []string{secretPath})
	must(t, err)
	must(t, s.Revert(prev))

	if got := s.Values(); !reflect.DeepEqual(got, before) {
		t.Errorf("Values after Revert = %v, want %v", got, before)
	}
	if got := diskValues(t, path); !reflect.DeepEqual(got, before) {
		t.Errorf("file after Revert = %v, want %v", got, before)
	}
	if s.Revision() != rev+2 {
		t.Errorf("Revision = %d, want %d: Revert is a new revision", s.Revision(), rev+2)
	}
	assertAbsent(t, dir, "NEWSECRET")
	if !strings.Contains(read(t, path), "OLDSECRET") {
		t.Error("the previous secret is not back in the live file")
	}

	// The snapshot holds copies: reverting again writes the same values and nothing else.
	must(t, s.Revert(prev))
	if s.Revision() != rev+2 || !reflect.DeepEqual(s.Values(), before) {
		t.Errorf("a second Revert changed the store: revision %d, %v", s.Revision(), s.Values())
	}
	must(t, s.Revert(Snapshot{}))
	if s.Revision() != rev+2 {
		t.Error("the zero Snapshot wrote a revision")
	}
}

// dirSyncFS fails the directory fsync that follows the rename of the store file.
type dirSyncFS struct {
	osFS
	err error
}

func (f *dirSyncFS) SyncDir(dir string) error {
	if f.err != nil {
		return f.err
	}
	return f.osFS.SyncDir(dir)
}

// A write whose rename happened but whose directory fsync failed is an error, and it landed: Apply
// returns the Snapshot with the error, so Revert can take it back, and a Revert that fails the
// same way still restores the values.
func TestApplyThatLandedWithAnErrorReturnsItsSnapshot(t *testing.T) {
	dir := t.TempDir()
	fsys := &dirSyncFS{}
	s := open(t, dir, Options{fs: fsys})
	must(t, s.Set("plugins.gatus.url", "https://a.example.com"))
	before, rev := s.Values(), s.Revision()

	eio := errors.New("EIO")
	fsys.err = eio
	prev, err := s.Apply(map[string]any{"plugins.gatus.url": "https://b.example.com"}, nil, nil)
	if !errors.Is(err, eio) {
		t.Fatalf("Apply err = %v, want the directory fsync's", err)
	}
	if s.Revision() != rev+1 {
		t.Fatalf("Revision = %d, want %d: the rename happened", s.Revision(), rev+1)
	}
	if reflect.DeepEqual(prev, Snapshot{}) {
		t.Fatal("Apply dropped the Snapshot of a write that landed")
	}

	if err := s.Revert(prev); !errors.Is(err, eio) {
		t.Fatalf("Revert err = %v, want the directory fsync's", err)
	}
	if s.Revision() != rev+2 || !reflect.DeepEqual(s.Values(), before) {
		t.Errorf("after a Revert that landed with an error: revision %d, %v; want %d, %v", s.Revision(), s.Values(), rev+2, before)
	}
	fsys.err = nil
	if got := diskValues(t, filepath.Join(dir, fileName)); !reflect.DeepEqual(got, before) {
		t.Errorf("file after Revert = %v, want %v", got, before)
	}

	// An error before the write leaves the store alone and the Snapshot zero.
	if prev, err := s.Apply(map[string]any{"a": 1, "a.b": 2}, nil, nil); err == nil || !reflect.DeepEqual(prev, Snapshot{}) {
		t.Errorf("a refused Apply: prev zero = %v, err = %v", reflect.DeepEqual(prev, Snapshot{}), err)
	}
}
