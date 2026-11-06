package credentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newGrant stores a grant of user created at created and returns its refresh token.
func newGrant(t *testing.T, s *Store, user string, created time.Time) (string, Grant, error) {
	t.Helper()
	id, err := NewGrantID()
	must(t, err)
	rt, hash, err := NewRefreshToken(id)
	must(t, err)
	g := Grant{ID: id, User: user, ClientID: "https://app.example.com/client.json", ClientName: "App", ClientKind: "metadata",
		Audience: "mcp", Scopes: []string{"read"}, Groups: []string{"ops"}, RefreshHash: hash,
		CreatedAt: created, ExpiresAt: created.Add(GrantLifetime), RefreshedAt: created}
	return rt, g, s.AddGrant(g)
}

func TestRefreshTokenFormat(t *testing.T) {
	re := regexp.MustCompile(`^dash_rt_[a-z2-7]{10}_[A-Za-z0-9_-]{43}$`)
	id, err := NewGrantID()
	must(t, err)
	token, hash, err := NewRefreshToken(id)
	must(t, err)
	if !re.MatchString(token) || hash != HashToken(token) {
		t.Fatalf("token %q hash %q", token, hash)
	}
	if got, ok := ParseRefreshToken(token); !ok || got != id {
		t.Fatalf("ParseRefreshToken = %q, %v; want %q", got, ok, id)
	}
	for _, bad := range []string{"", token + "x", token[:len(token)-1], "dash_at_" + token[8:], strings.ToUpper(token[:18]) + token[18:], token[:len(token)-1] + "."} {
		if _, ok := ParseRefreshToken(bad); ok {
			t.Errorf("ParseRefreshToken accepted %q", bad)
		}
	}
}

// Every refresh rotates the token: the new one works, and the old one presented again revokes
// the grant, so the new one dies with it (OAuth 2.1 4.3.1).
func TestRefreshRotationAndReuse(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	rt1, g, err := newGrant(t, s, "alice", t0)
	must(t, err)
	rt2, h2, err := NewRefreshToken(g.ID)
	must(t, err)
	got, err := s.RotateRefresh(rt1, h2, t0.Add(time.Hour))
	if err != nil || got.RefreshHash != h2 || got.LastUsedAt == nil || !got.RefreshedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("rotate = %+v, %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	must(t, err)
	if strings.Contains(string(data), rt1) || strings.Contains(string(data), rt2) || !strings.Contains(string(data), h2) || !strings.Contains(string(data), "schemaVersion: 2") {
		t.Errorf("credentials.yaml holds a secret, or not the hash and version 2:\n%s", data)
	}
	// A forged secret under the grant's ID is unknown and revokes nothing.
	forged := rt2[:len(rt2)-4] + "AAAA"
	if forged == rt2 {
		forged = rt2[:len(rt2)-4] + "BBBB"
	}
	if _, err := s.RotateRefresh(forged, h2, t0.Add(time.Hour)); !errors.Is(err, ErrTokenUnknown) {
		t.Errorf("forged: %v", err)
	}
	if _, ok := s.GrantByID(g.ID); !ok {
		t.Fatal("a forged token revoked the grant")
	}
	_, h3, _ := NewRefreshToken(g.ID)
	if _, err := s.RotateRefresh(rt1, h3, t0.Add(2*time.Hour)); !errors.Is(err, ErrRefreshReuse) {
		t.Fatalf("reuse: %v, want ErrRefreshReuse", err)
	}
	if _, ok := s.GrantByID(g.ID); ok {
		t.Fatal("reuse left the grant")
	}
	if _, err := s.RotateRefresh(rt2, h3, t0.Add(2*time.Hour)); !errors.Is(err, ErrTokenUnknown) {
		t.Errorf("the current token after reuse: %v, want ErrTokenUnknown", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, FileName))
	must(t, err)
	if !strings.Contains(string(data), "schemaVersion: 1") || strings.Contains(string(data), "grants:") {
		t.Errorf("after the last grant went:\n%s", data)
	}
}

// A grant ends 30 days after its last refresh, and 180 days after the consent however often it
// was refreshed.
func TestGrantIdleAndAbsoluteExpiry(t *testing.T) {
	s := open(t, t.TempDir())
	rt, g, err := newGrant(t, s, "alice", t0)
	must(t, err)
	_, h, _ := NewRefreshToken(g.ID)
	if _, err := s.RotateRefresh(rt, h, t0.Add(GrantIdle)); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("idle: %v, want ErrTokenExpired", err)
	}
	now := t0
	for now.Add(GrantIdle - time.Hour).Before(t0.Add(GrantLifetime)) {
		now = now.Add(GrantIdle - time.Hour)
		next, h, _ := NewRefreshToken(g.ID)
		if _, err := s.RotateRefresh(rt, h, now); err != nil {
			t.Fatalf("refresh at %v: %v", now, err)
		}
		rt = next
	}
	_, h, _ = NewRefreshToken(g.ID)
	if _, err := s.RotateRefresh(rt, h, t0.Add(GrantLifetime)); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("past the absolute expiry: %v, want ErrTokenExpired", err)
	}
	if n, err := s.PruneExpired(t0.Add(GrantLifetime)); err != nil || n != 1 {
		t.Errorf("prune = %d, %v; want the expired grant deleted", n, err)
	}
}

// A grant whose code was never exchanged is pruned once the code has expired, so a restart that
// forgot the code strands nothing; an exchanged one stays, and the exchange is written.
func TestUnexchangedGrantsArePruned(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	_, idle, err := newGrant(t, s, "alice", t0)
	must(t, err)
	_, used, err := newGrant(t, s, "alice", t0)
	must(t, err)
	if g, ok, err := s.ExchangeGrant(used.ID, t0.Add(30*time.Second)); err != nil || !ok || g.LastUsedAt == nil || !g.LastUsedAt.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("exchange = %+v, %v, %v", g, ok, err)
	}
	if n, err := s.PruneExpired(t0.Add(CodeTTL)); err != nil || n != 0 {
		t.Errorf("prune while the code may still live = %d, %v", n, err)
	}
	if n, err := s.PruneExpired(t0.Add(CodeTTL + time.Second)); err != nil || n != 1 {
		t.Errorf("prune after the code expired = %d, %v; want the unexchanged grant deleted", n, err)
	}
	if _, ok := s.GrantByID(idle.ID); ok {
		t.Error("the unexchanged grant is still stored")
	}
	must(t, s.Close())
	s = open(t, dir)
	if g, ok := s.GrantByID(used.ID); !ok || g.LastUsedAt == nil {
		t.Errorf("after a reopen the exchanged grant = %+v, %v", g, ok)
	}
	if _, ok, err := s.ExchangeGrant(idle.ID, t0); ok || err != nil {
		t.Errorf("exchanging a pruned grant = %v, %v", ok, err)
	}
}

// The flush loop prunes once as it starts: what expired while dash was stopped goes at once.
func TestRunLastUsedFlushPrunesAtStart(t *testing.T) {
	s := open(t, t.TempDir())
	_, g, err := newGrant(t, s, "alice", time.Now().UTC().Truncate(time.Second).Add(-time.Hour))
	must(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.RunLastUsedFlush(ctx, time.Hour)
	if _, ok := s.GrantByID(g.ID); ok {
		t.Error("a grant never exchanged survived the start")
	}
}

// A user holds at most MaxActiveGrants active grants; the refused one is not stored.
func TestGrantsPerUserAreCapped(t *testing.T) {
	s := open(t, t.TempDir())
	if _, _, err := newGrant(t, s, "alice", t0.Add(-GrantLifetime)); err != nil { // inactive at t0
		t.Fatal(err)
	}
	for range MaxActiveGrants {
		if _, _, err := newGrant(t, s, "alice", t0); err != nil {
			t.Fatal(err)
		}
	}
	_, g, err := newGrant(t, s, "alice", t0)
	if !errors.Is(err, ErrTooManyGrants) {
		t.Fatalf("grant %d: %v, want ErrTooManyGrants", MaxActiveGrants+1, err)
	}
	if _, ok := s.GrantByID(g.ID); ok {
		t.Error("the refused grant was stored")
	}
	if _, _, err := newGrant(t, s, "bob", t0); err != nil {
		t.Errorf("another user: %v", err)
	}
}

// RevokeGrants takes one user's grants (never everyone's for an empty name); RevokeAllGrants
// takes every grant. Personal access tokens are never touched.
func TestRevokeGrantsByUser(t *testing.T) {
	s := open(t, t.TempDir())
	_, _, err := newGrant(t, s, "alice", t0)
	must(t, err)
	_, _, err = newGrant(t, s, "alice", t0)
	must(t, err)
	_, bob, err := newGrant(t, s, "bob", t0)
	must(t, err)
	_, pat := newPAT(t, s, "alice")
	if got, err := s.RevokeGrants(""); err != nil || len(got) != 0 || len(s.Grants("")) != 3 {
		t.Fatalf("empty user revoked %d", len(got))
	}
	got, err := s.RevokeGrants("alice")
	if err != nil || len(got) != 2 || len(s.Grants("alice")) != 0 {
		t.Fatalf("RevokeGrants(alice) = %d, %v", len(got), err)
	}
	if left := s.Grants(""); len(left) != 1 || left[0].ID != bob.ID {
		t.Errorf("left %+v, want bob's", left)
	}
	if got, err := s.RevokeAllGrants(); err != nil || len(got) != 1 || len(s.Grants("")) != 0 {
		t.Errorf("RevokeAllGrants = %d, %v", len(got), err)
	}
	if _, ok := s.PATByID(pat.ID); !ok {
		t.Error("revoking grants took a personal access token")
	}
}

// A group change seen interactively reaches the user's grants; an unchanged set writes nothing.
func TestSyncIdentityReachesGrants(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	_, g, err := newGrant(t, s, "alice", t0)
	must(t, err)
	before, err := os.Stat(filepath.Join(dir, FileName))
	must(t, err)
	if changed, err := s.SyncIdentity("alice", []string{"ops"}, nil); changed || err != nil {
		t.Fatalf("unchanged: %v, %v", changed, err)
	}
	if after, _ := os.Stat(filepath.Join(dir, FileName)); !os.SameFile(before, after) {
		t.Error("an unchanged set rewrote the file")
	}
	if changed, err := s.SyncIdentity("alice", []string{"family"}, []string{"admin"}); !changed || err != nil {
		t.Fatalf("changed: %v, %v", changed, err)
	}
	got, _ := s.GrantByID(g.ID)
	if len(got.Groups) != 1 || got.Groups[0] != "family" || len(got.RoleClaim) != 1 {
		t.Errorf("grant = %+v", got)
	}
	// The change is on disk.
	s.Close()
	got, _ = open(t, dir).GrantByID(g.ID)
	if len(got.Groups) != 1 || got.Groups[0] != "family" {
		t.Errorf("after reopen: %+v", got)
	}
}

// A failed write stores no grant and keeps a refresh token valid, so the client can retry.
func TestGrantWritesFailClosed(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	rt, g, err := newGrant(t, s, "alice", t0)
	must(t, err)
	must(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, _, err := newGrant(t, s, "alice", t0); err == nil {
		t.Fatal("a grant was stored without a write")
	}
	if n := len(s.Grants("alice")); n != 1 {
		t.Errorf("grants after a failed add: %d", n)
	}
	_, h, _ := NewRefreshToken(g.ID)
	if _, err := s.RotateRefresh(rt, h, t0.Add(time.Minute)); err == nil {
		t.Fatal("a rotation succeeded without a write")
	}
	must(t, os.Chmod(dir, 0o700))
	if _, err := s.RotateRefresh(rt, h, t0.Add(time.Minute)); err != nil {
		t.Errorf("the retry: %v", err)
	}
}

// An access token's use of a grant is noted in memory (NoteGrantUsed, UTC to the second),
// listed by Grants at once and written by FlushLastUsed and Close, like a personal access
// token's; noting writes nothing, and an older use never replaces a newer one.
func TestGrantLastUsedIsFlushed(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	_, g, err := newGrant(t, s, "alice", t0)
	must(t, err)
	before, err := os.Stat(s.Path())
	must(t, err)
	used := t0.Add(time.Hour)
	s.NoteGrantUsed(g.ID, used.In(time.FixedZone("x", 3600)).Add(400*time.Millisecond))
	if after, _ := os.Stat(s.Path()); !os.SameFile(before, after) {
		t.Error("noting a use wrote the file")
	}
	if got := s.Grants("alice"); got[0].LastUsedAt == nil || !got[0].LastUsedAt.Equal(used) || got[0].LastUsedAt.Location() != time.UTC {
		t.Errorf("Grants lastUsedAt = %v, want %v in UTC to the second", got[0].LastUsedAt, used)
	}
	must(t, s.FlushLastUsed())
	if got, _ := s.GrantByID(g.ID); got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
		t.Errorf("after the flush lastUsedAt = %v, want %v", got.LastUsedAt, used)
	}
	s.NoteGrantUsed(g.ID, t0)
	must(t, s.FlushLastUsed())
	if got, _ := s.GrantByID(g.ID); !got.LastUsedAt.Equal(used) {
		t.Errorf("an older use replaced the newer one: %v", got.LastUsedAt)
	}
	later := used.Add(time.Hour)
	s.NoteGrantUsed(g.ID, later)
	must(t, s.Close())
	s = open(t, dir)
	if got, _ := s.GrantByID(g.ID); got.LastUsedAt == nil || !got.LastUsedAt.Equal(later) {
		t.Errorf("after Close and a reopen lastUsedAt = %v, want %v", got.LastUsedAt, later)
	}
}
