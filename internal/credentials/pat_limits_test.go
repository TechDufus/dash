package credentials

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// addAt stores a token of user created at created that expires after life.
func addAt(t *testing.T, s *Store, user string, created time.Time, life time.Duration) (PAT, error) {
	t.Helper()
	_, id, hash, err := NewPAT()
	must(t, err)
	p := PAT{ID: id, Name: "t", User: user, Hash: hash, Audience: "api", Scopes: []string{"read"}, CreatedAt: created, ExpiresAt: created.Add(life)}
	return p, s.AddPAT(p)
}

// A user holds at most MaxActivePATs unexpired tokens; an expired one does not count, and
// another user is not affected.
func TestActiveTokensPerUserAreCapped(t *testing.T) {
	s := open(t, t.TempDir())
	day := 24 * time.Hour
	if _, err := addAt(t, s, "alice", t0.Add(-10*day), day); err != nil { // expired at t0
		t.Fatal(err)
	}
	for i := range MaxActivePATs {
		if _, err := addAt(t, s, "alice", t0, day); err != nil {
			t.Fatalf("token %d: %v", i+1, err)
		}
	}
	if s.ActivePATs("alice", t0) != MaxActivePATs {
		t.Fatalf("ActivePATs = %d", s.ActivePATs("alice", t0))
	}
	p, err := addAt(t, s, "alice", t0, day)
	if !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("token %d: %v, want ErrTooManyTokens", MaxActivePATs+1, err)
	}
	if _, ok := s.PATByID(p.ID); ok {
		t.Error("the refused token was stored")
	}
	if _, err := addAt(t, s, "bob", t0, day); err != nil {
		t.Errorf("another user: %v", err)
	}
}

// Tokens that expired more than PruneAfter ago are deleted, the others stay; with none left the
// file falls back to schemaVersion 1. Nothing to prune writes nothing.
func TestLongExpiredTokensArePruned(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	day := 24 * time.Hour
	old, err := addAt(t, s, "alice", t0.Add(-60*day), day) // expired 59 days before t0
	must(t, err)
	recent, err := addAt(t, s, "alice", t0.Add(-20*day), day) // expired 19 days before t0
	must(t, err)

	n, err := s.PruneExpired(t0)
	if err != nil || n != 1 {
		t.Fatalf("PruneExpired = %d, %v; want 1", n, err)
	}
	if _, ok := s.PATByID(old.ID); ok {
		t.Error("the long-expired token is still stored")
	}
	if _, ok := s.PATByID(recent.ID); !ok {
		t.Error("a recently expired token was pruned")
	}
	before, err := os.Stat(s.Path())
	must(t, err)
	if n, err := s.PruneExpired(t0); n != 0 || err != nil {
		t.Errorf("second prune = %d, %v", n, err)
	}
	if after, _ := os.Stat(s.Path()); !os.SameFile(before, after) {
		t.Error("a prune with nothing to delete wrote the file")
	}
	if n, _ := s.PruneExpired(t0.Add(40 * day)); n != 1 {
		t.Fatalf("pruning the rest = %d", n)
	}
	data, err := os.ReadFile(s.Path())
	must(t, err)
	if !strings.Contains(string(data), "schemaVersion: 1") {
		t.Errorf("after the last prune:\n%s", data)
	}
}

// A revocation while credentials.yaml was replaced holds in this process but reports
// ErrFileChanged: a restart would read a file that still holds the token.
func TestRevokeReportsAReplacedFile(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	token, p := newPAT(t, s, "alice")
	data, err := os.ReadFile(s.Path())
	must(t, err)
	must(t, os.Remove(s.Path()))
	must(t, os.WriteFile(s.Path(), data, 0o600)) // the same content, another file
	if _, ok, err := s.RevokePAT(p.ID); !ok || !errors.Is(err, ErrFileChanged) {
		t.Fatalf("RevokePAT = %v, %v; want the revocation and ErrFileChanged", ok, err)
	}
	if _, err := s.LookupPAT(token, t0); !errors.Is(err, ErrTokenUnknown) {
		t.Errorf("the revoked token still resolves: %v", err)
	}
}
