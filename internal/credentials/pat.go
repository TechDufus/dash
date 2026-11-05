package credentials

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
)

// PATPrefix starts every personal access token (design M.5): dash_pat_<id>_<secret>. The
// scrubber redacts the whole token by this prefix (internal/secret).
const PATPrefix = "dash_pat_"

// HashPrefix starts a stored token hash: sha256:<64 hex>, the form auth.tokens uses as well.
const HashPrefix = "sha256:"

const (
	patIDLen     = 10 // base32 characters
	patSecretLen = 43 // base64url characters of 32 random bytes
	patLen       = len(PATPrefix) + patIDLen + 1 + patSecretLen
	patAlphabet  = "abcdefghijklmnopqrstuvwxyz234567" // base32, lower case
)

// LastUsedFlushInterval is how often the in-memory lastUsedAt times are written to the file
// at the latest (design M.5); long-expired tokens are pruned at the same time.
const LastUsedFlushInterval = 5 * time.Minute

// MaxActivePATs is how many unexpired personal access tokens one user may hold: each one is
// rewritten with the whole file and scanned on every proxy-mode request.
const MaxActivePATs = 50

// PruneAfter is how long an expired token stays listed (marked expired) before it is deleted.
const PruneAfter = 30 * 24 * time.Hour

// Token lookup failures. Callers answer both with the same 401 invalid_token.
var (
	ErrTokenUnknown = errors.New("credentials: unknown or revoked token")
	ErrTokenExpired = errors.New("credentials: expired token")
)

// ErrTooManyTokens refuses a token past MaxActivePATs for its user.
var ErrTooManyTokens = errors.New("credentials: the user holds the most tokens allowed")

// PAT is a stored personal access token. The secret is never stored: Hash is the SHA-256 of
// the whole token.
type PAT struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	User     string   `json:"user"`
	Hash     string   `json:"hash"`     // sha256:<hex> of the whole token
	Audience string   `json:"audience"` // api or mcp
	Scopes   []string `json:"scopes"`
	// ActionNamespaces are exact names or prefix* globs; empty means no restriction.
	ActionNamespaces []string `json:"actionNamespaces,omitempty"`
	// Groups and RoleClaim are what the owner's identity carried when dash last saw them
	// interactively: the role is derived from them and the current mapping on every request.
	Groups     []string   `json:"groups,omitempty"`
	RoleClaim  []string   `json:"roleClaim,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// clone is a deep copy: a PAT handed out or put into a new document shares nothing.
func (p PAT) clone() PAT {
	p.Scopes = slices.Clone(p.Scopes)
	p.ActionNamespaces = slices.Clone(p.ActionNamespaces)
	p.Groups = slices.Clone(p.Groups)
	p.RoleClaim = slices.Clone(p.RoleClaim)
	if p.LastUsedAt != nil {
		t := *p.LastUsedAt
		p.LastUsedAt = &t
	}
	return p
}

func clonePATs(m map[string]PAT) map[string]PAT {
	if m == nil {
		return nil
	}
	out := make(map[string]PAT, len(m))
	for id, p := range m {
		out[id] = p.clone()
	}
	return out
}

// NewPAT makes a personal access token: the token, shown once, its ID and the hash to store.
func NewPAT() (token, id, hash string, err error) {
	raw := make([]byte, patIDLen+32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	var b strings.Builder
	b.Grow(patLen)
	b.WriteString(PATPrefix)
	for _, c := range raw[:patIDLen] {
		b.WriteByte(patAlphabet[c&31])
	}
	b.WriteByte('_')
	b.WriteString(base64.RawURLEncoding.EncodeToString(raw[patIDLen:]))
	token = b.String()
	return token, token[len(PATPrefix) : len(PATPrefix)+patIDLen], HashToken(token), nil
}

// ParsePAT returns the ID of a token of the personal access token shape, false for anything
// else. It says nothing about whether the token exists.
func ParsePAT(token string) (id string, ok bool) {
	if len(token) != patLen || !strings.HasPrefix(token, PATPrefix) || token[len(PATPrefix)+patIDLen] != '_' {
		return "", false
	}
	id = token[len(PATPrefix) : len(PATPrefix)+patIDLen]
	for i := range len(id) {
		if strings.IndexByte(patAlphabet, id[i]) < 0 {
			return "", false
		}
	}
	for _, c := range []byte(token[len(token)-patSecretLen:]) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	return id, true
}

// HashToken is the stored form of a token: sha256:<hex>.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return HashPrefix + hex.EncodeToString(sum[:])
}

// validPAT is the shape decode and AddPAT require of a record.
func validPAT(id string, p PAT) error {
	switch {
	case p.ID == "" || p.ID != id:
		return errors.New("pats: a token's key must be its id")
	case p.User == "" || p.Name == "":
		return errors.New("pats: a token needs a user and a name")
	case !strings.HasPrefix(p.Hash, HashPrefix) || len(p.Hash) != len(HashPrefix)+2*sha256.Size:
		return errors.New("pats: a token needs a sha256: hash")
	case p.ExpiresAt.IsZero():
		return errors.New("pats: a token needs an expiry")
	}
	return nil
}

// writtenVersion is the schemaVersion a document is written with (contract D4): 2 once it holds
// a personal access token, 1 before, so an install that never made one can still roll back.
func writtenVersion(d document) int {
	if len(d.PATs) > 0 {
		return 2
	}
	return 1
}

// AddPAT stores a new personal access token. Nothing changes when the write fails.
func (s *Store) AddPAT(p PAT) error {
	p = p.clone()
	if err := validPAT(p.ID, p); err != nil {
		return err
	}
	full := false
	err := s.update(false, func(d *document) bool {
		if activeOf(d.PATs, p.User, p.CreatedAt) >= MaxActivePATs {
			full = true
			return false
		}
		if d.PATs == nil {
			d.PATs = map[string]PAT{}
		}
		d.PATs[p.ID] = p
		return true
	})
	if full {
		return ErrTooManyTokens
	}
	return err
}

// ActivePATs is how many personal access tokens of user have not expired at now.
func (s *Store) ActivePATs(user string, now time.Time) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return activeOf(s.doc.PATs, user, now)
}

func activeOf(pats map[string]PAT, user string, now time.Time) int {
	n := 0
	for _, p := range pats {
		if p.User == user && now.Before(p.ExpiresAt) {
			n++
		}
	}
	return n
}

// PruneExpired deletes the tokens that expired more than PruneAfter before now: past that they
// only grow the file and keep it at schemaVersion 2. It reports how many it deleted; like a
// revocation, the deletion holds in this process even when the write fails.
func (s *Store) PruneExpired(now time.Time) (int, error) {
	cut := now.Add(-PruneAfter)
	stale := func(p PAT) bool { return p.ExpiresAt.Before(cut) }
	s.mu.RLock()
	found := slices.ContainsFunc(slices.Collect(maps.Values(s.doc.PATs)), stale)
	s.mu.RUnlock()
	if !found {
		return 0, nil
	}
	n := 0
	err := s.update(true, func(d *document) bool {
		for id, p := range d.PATs {
			if stale(p) {
				delete(d.PATs, id)
				n++
			}
		}
		if len(d.PATs) == 0 {
			d.PATs = nil
		}
		return n > 0
	})
	return n, err
}

// PATs lists the personal access tokens of user, or every user's for "", oldest first, with
// the lastUsedAt times not flushed yet.
func (s *Store) PATs(user string) []PAT {
	s.mu.RLock()
	out := make([]PAT, 0, len(s.doc.PATs))
	for _, p := range s.doc.PATs {
		if user == "" || p.User == user {
			out = append(out, p.clone())
		}
	}
	s.mu.RUnlock()
	s.usedMu.Lock()
	for i := range out {
		if t, ok := s.used[out[i].ID]; ok && (out[i].LastUsedAt == nil || t.After(*out[i].LastUsedAt)) {
			out[i].LastUsedAt = &t
		}
	}
	s.usedMu.Unlock()
	slices.SortFunc(out, func(a, b PAT) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return out
}

// PATByID returns one personal access token.
func (s *Store) PATByID(id string) (PAT, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.doc.PATs[id]
	return p.clone(), ok
}

// RevokePAT deletes a personal access token and returns it; false when there is none. The
// revocation applies to this process even when the write fails, as an epoch bump does, but the
// error says so: ErrFileChanged too when credentials.yaml was removed or replaced while dash runs,
// because a restart would then read a file that still holds the token.
func (s *Store) RevokePAT(id string) (revoked PAT, ok bool, err error) {
	err = s.update(true, func(d *document) bool {
		revoked, ok = d.PATs[id]
		if !ok {
			return false
		}
		delete(d.PATs, id)
		if len(d.PATs) == 0 {
			d.PATs = nil
		}
		return true
	})
	if ok {
		s.usedMu.Lock()
		delete(s.used, id)
		s.usedMu.Unlock()
		if err == nil && s.fileChanged() {
			err = ErrFileChanged
		}
	}
	return revoked.clone(), ok, err
}

// fileChanged reports whether a persistent store's file was removed or replaced since dash
// read or last wrote it, which update then no longer writes.
func (s *Store) fileChanged() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path != "" && s.changedOnDisk()
}

// LookupPAT finds the personal access token a bearer credential presents: the hash is compared
// in constant time, and an expired token is refused. It does not note a use: the caller does,
// with NoteUsed, once every other check of the request passed.
func (s *Store) LookupPAT(token string, now time.Time) (PAT, error) {
	id, ok := ParsePAT(token)
	if !ok {
		return PAT{}, ErrTokenUnknown
	}
	hash := HashToken(token)
	s.mu.RLock()
	p, found := s.doc.PATs[id]
	if found {
		p = p.clone()
	}
	s.mu.RUnlock()
	if !found || subtle.ConstantTimeCompare([]byte(hash), []byte(p.Hash)) != 1 {
		return PAT{}, ErrTokenUnknown
	}
	if !now.Before(p.ExpiresAt) {
		return PAT{}, ErrTokenExpired
	}
	return p, nil
}

// NoteUsed records in memory that token id authenticated a request at now, in UTC to the
// second like createdAt; FlushLastUsed writes it.
func (s *Store) NoteUsed(id string, now time.Time) {
	s.usedMu.Lock()
	defer s.usedMu.Unlock()
	if s.used == nil {
		s.used = map[string]time.Time{}
	}
	s.used[id] = now.UTC().Truncate(time.Second)
}

// FlushLastUsed writes the lastUsedAt times noted since the last flush. No use, no write.
func (s *Store) FlushLastUsed() error {
	s.usedMu.Lock()
	pending := make(map[string]time.Time, len(s.used))
	for id, t := range s.used {
		pending[id] = t
	}
	s.usedMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	err := s.update(false, func(d *document) bool {
		changed := false
		for id, t := range pending {
			p, ok := d.PATs[id]
			if !ok || (p.LastUsedAt != nil && !t.After(*p.LastUsedAt)) {
				continue
			}
			p.LastUsedAt = &t
			d.PATs[id] = p
			changed = true
		}
		return changed
	})
	if err != nil {
		return err
	}
	// Forget what was written, unless a newer use came in meanwhile.
	s.usedMu.Lock()
	for id, t := range pending {
		if s.used[id].Equal(t) {
			delete(s.used, id)
		}
	}
	s.usedMu.Unlock()
	return nil
}

// RunLastUsedFlush flushes the lastUsedAt times every interval until ctx ends. Close flushes
// the last ones.
func (s *Store) RunLastUsedFlush(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := s.FlushLastUsed(); err != nil {
				slog.Warn("credentials: writing the tokens' last use failed", "err", err)
			}
			if _, err := s.PruneExpired(now); err != nil {
				slog.Warn("credentials: removing long-expired tokens failed", "err", err)
			}
		}
	}
}
