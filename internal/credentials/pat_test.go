package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// newPAT stores a token of user and returns it with its record.
func newPAT(t *testing.T, s *Store, user string) (string, PAT) {
	t.Helper()
	token, id, hash, err := NewPAT()
	must(t, err)
	p := PAT{ID: id, Name: "ci", User: user, Hash: hash, Audience: "api", Scopes: []string{"read"},
		Groups: []string{"ops"}, CreatedAt: t0, ExpiresAt: t0.Add(30 * 24 * time.Hour)}
	must(t, s.AddPAT(p))
	return token, p
}

func TestPATFormat(t *testing.T) {
	re := regexp.MustCompile(`^dash_pat_[a-z2-7]{10}_[A-Za-z0-9_-]{43}$`)
	seen := map[string]bool{}
	for range 50 {
		token, id, hash, err := NewPAT()
		must(t, err)
		if !re.MatchString(token) {
			t.Fatalf("token %q does not match the M.5 format", token)
		}
		if got, ok := ParsePAT(token); !ok || got != id {
			t.Fatalf("ParsePAT(%q) = %q, %v; want %q", token, got, ok, id)
		}
		if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(hash) || hash != HashToken(token) {
			t.Fatalf("hash %q", hash)
		}
		if seen[id] {
			t.Fatalf("id %q repeated", id)
		}
		seen[id] = true
	}
	token, _, _, _ := NewPAT()
	for _, bad := range []string{"", token[:len(token)-1], token + "x", "dash_at_" + token[9:], strings.Replace(token, "_", "-", 2), token[:9] + "ABCDEFGHIJ" + token[19:], token[:len(token)-1] + "."} {
		if _, ok := ParsePAT(bad); ok {
			t.Errorf("ParsePAT accepted %q", bad)
		}
	}
}

func TestPATLookupRevokeAndExpiry(t *testing.T) {
	s := open(t, t.TempDir())
	token, p := newPAT(t, s, "alice")
	got, err := s.LookupPAT(token, t0)
	if err != nil || got.ID != p.ID || got.User != "alice" || !slices.Equal(got.Groups, []string{"ops"}) {
		t.Fatalf("LookupPAT = %+v, %v", got, err)
	}
	// Another secret under the same ID is refused: the hash is compared, not just the ID.
	forged := token[:len(token)-4] + "AAAA"
	if forged == token {
		forged = token[:len(token)-4] + "BBBB"
	}
	if _, err := s.LookupPAT(forged, t0); !errors.Is(err, ErrTokenUnknown) {
		t.Errorf("a forged secret: %v, want ErrTokenUnknown", err)
	}
	if _, err := s.LookupPAT(token, p.ExpiresAt); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("at the expiry: %v, want ErrTokenExpired", err)
	}
	revoked, ok, err := s.RevokePAT(p.ID)
	if err != nil || !ok || revoked.ID != p.ID {
		t.Fatalf("RevokePAT = %+v, %v, %v", revoked, ok, err)
	}
	if _, err := s.LookupPAT(token, t0); !errors.Is(err, ErrTokenUnknown) {
		t.Errorf("after revocation: %v, want ErrTokenUnknown", err)
	}
	if _, ok, err := s.RevokePAT(p.ID); ok || err != nil {
		t.Errorf("revoking twice: %v, %v", ok, err)
	}
}

// The file holds the hash only, is written as schemaVersion 2 only while it holds a token
// (contract D4), stays 0600 and gets no backup.
func TestPATFileHoldsHashesAndVersion(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	token, p := newPAT(t, s, "alice")
	data, err := os.ReadFile(s.Path())
	must(t, err)
	if strings.Contains(string(data), token) || strings.Contains(string(data), token[len(PATPrefix)+patIDLen+1:]) {
		t.Fatal("credentials.yaml holds the token's secret")
	}
	for _, want := range []string{"schemaVersion: 2", "pats:", p.Hash, "revoke every token"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("credentials.yaml lacks %q:\n%s", want, data)
		}
	}
	fi, err := os.Stat(s.Path())
	must(t, err)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if got, want := dirNames(t, dir), []string{LockName, FileName}; !slices.Equal(got, want) {
		t.Errorf("data dir holds %q, want only %q", got, want)
	}

	// It survives a reopen.
	must(t, s.Close())
	s = open(t, dir)
	if _, err := s.LookupPAT(token, t0); err != nil {
		t.Fatalf("after a reopen: %v", err)
	}
	// Without a token the file is version 1 again, which an older dash reads.
	_, _, err = s.RevokePAT(p.ID)
	must(t, err)
	data, err = os.ReadFile(s.Path())
	must(t, err)
	if !strings.Contains(string(data), "schemaVersion: 1") || strings.Contains(string(data), "pats:") {
		t.Errorf("after the last revocation:\n%s", data)
	}
}

func TestPATListFiltersByUser(t *testing.T) {
	s := open(t, t.TempDir())
	_, a := newPAT(t, s, "alice")
	_, b := newPAT(t, s, "bob")
	if got := s.PATs("alice"); len(got) != 1 || got[0].ID != a.ID {
		t.Errorf("PATs(alice) = %+v", got)
	}
	if got := s.PATs(""); len(got) != 2 {
		t.Errorf("PATs(\"\") = %d tokens, want 2", len(got))
	}
	if got, ok := s.PATByID(b.ID); !ok || got.User != "bob" {
		t.Errorf("PATByID = %+v, %v", got, ok)
	}
	// What a caller gets is a copy.
	got := s.PATs("alice")
	got[0].Scopes[0] = "logs"
	if again := s.PATs("alice"); again[0].Scopes[0] != "read" {
		t.Error("PATs handed out the stored slice")
	}
}

// A change applied to the copy in update and then not written leaves the store as it was, even
// for the slices inside a token.
func TestFailedWriteLeavesTokensUnchanged(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	s := open(t, dir)
	token, p := newPAT(t, s, "alice")
	must(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err := s.update(false, func(d *document) bool {
		q := d.PATs[p.ID]
		q.Scopes[0] = "logs"
		q.Groups[0] = "admins"
		return true
	})
	if err == nil {
		t.Fatal("the write succeeded on a read-only directory")
	}
	if got, _ := s.PATByID(p.ID); got.Scopes[0] != "read" || got.Groups[0] != "ops" {
		t.Errorf("a failed write changed the token: %+v", got)
	}
	_, other, _, _ := NewPAT()
	if err := s.AddPAT(PAT{ID: other, Name: "x", User: "bob", Hash: p.Hash, ExpiresAt: p.ExpiresAt}); err == nil {
		t.Fatal("AddPAT reported success on a read-only directory")
	}
	if _, ok := s.PATByID(other); ok {
		t.Error("a token that never reached the disk is served")
	}
	// A revocation holds in this process even when the file cannot be written.
	if _, ok, err := s.RevokePAT(p.ID); !ok || err == nil {
		t.Fatalf("RevokePAT = %v, %v; want the revocation and the write error", ok, err)
	}
	if _, err := s.LookupPAT(token, t0); !errors.Is(err, ErrTokenUnknown) {
		t.Errorf("a revoked token still signs in after a failed write: %v", err)
	}
}

// lastUsedAt is kept in memory (NoteUsed, in UTC to the second), shown by PATs at once, and
// written by FlushLastUsed and Close; a lookup alone notes nothing, and a flush without a use
// writes nothing.
func TestLastUsedIsFlushed(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	token, p := newPAT(t, s, "alice")
	before, err := os.Stat(s.Path())
	must(t, err)
	must(t, s.FlushLastUsed())
	if after, _ := os.Stat(s.Path()); !os.SameFile(before, after) {
		t.Error("a flush without a use wrote the file")
	}
	if _, err := s.LookupPAT(token, t0); err != nil {
		t.Fatal(err)
	}
	if got := s.PATs("alice"); got[0].LastUsedAt != nil {
		t.Errorf("a lookup noted a use: %v", got[0].LastUsedAt)
	}

	used := t0.Add(time.Hour)
	s.NoteUsed(p.ID, used.In(time.FixedZone("x", 3600)).Add(400*time.Millisecond))
	if after, _ := os.Stat(s.Path()); !os.SameFile(before, after) {
		t.Error("noting a use wrote the file")
	}
	if got := s.PATs("alice"); got[0].LastUsedAt == nil || !got[0].LastUsedAt.Equal(used) || got[0].LastUsedAt.Location() != time.UTC {
		t.Errorf("PATs lastUsedAt = %v, want %v in UTC to the second", got[0].LastUsedAt, used)
	}
	must(t, s.FlushLastUsed())
	data, err := os.ReadFile(s.Path())
	must(t, err)
	if !strings.Contains(string(data), "lastUsedAt: \""+used.Format(time.RFC3339)+"\"") {
		t.Errorf("the flush did not write lastUsedAt in UTC to the second:\n%s", data)
	}

	later := used.Add(time.Hour)
	s.NoteUsed(p.ID, later)
	must(t, s.Close())
	s = open(t, dir)
	if got, _ := s.PATByID(p.ID); got.LastUsedAt == nil || !got.LastUsedAt.Equal(later) {
		t.Errorf("after Close and a reopen lastUsedAt = %v, want %v", got.LastUsedAt, later)
	}
}

func TestMalformedTokenRecordIsRefused(t *testing.T) {
	salt := strings.Repeat("A", 43)
	dir := t.TempDir()
	body := "schemaVersion: 2\nepochs: {}\nglobalEpoch: 0\nsessionSalt: " + salt + "\npats:\n  abc:\n    id: abc\n    user: alice\n    name: ci\n    hash: plain\n"
	must(t, os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600))
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("Open accepted a token without a sha256 hash")
	}
}

func TestMemoryStoreKeepsTokensInProcess(t *testing.T) {
	s, err := OpenMemory()
	must(t, err)
	token, _ := newPAT(t, s, "dev")
	if _, err := s.LookupPAT(token, t0); err != nil || s.Persistent() {
		t.Fatalf("in memory: %v, Persistent %v", err, s.Persistent())
	}
	must(t, s.FlushLastUsed())
}
