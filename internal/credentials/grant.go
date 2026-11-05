package credentials

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"time"
)

// RefreshPrefix starts every OAuth refresh token (design M.6): dash_rt_<grantID>_<secret>. The
// scrubber redacts the whole token by this prefix (internal/secret).
const RefreshPrefix = "dash_rt_"

const (
	grantIDLen = 10 // base32 characters, as a PAT's ID
	refreshLen = len(RefreshPrefix) + grantIDLen + 1 + patSecretLen
	// maxPrevRefresh is how many rotated refresh tokens a grant remembers: presenting one of
	// them again is reuse, which revokes the grant (OAuth 2.1 4.3.1). An older one is unknown.
	maxPrevRefresh = 16
)

// GrantIdle is how long a grant lives without a refresh; GrantLifetime how long it lives at
// most, however often its client refreshes (design M.6).
const (
	GrantIdle     = 30 * 24 * time.Hour
	GrantLifetime = 180 * 24 * time.Hour
)

// MaxActiveGrants is how many unexpired grants one user may hold (contract G7-R3).
const MaxActiveGrants = 20

var (
	// ErrTooManyGrants refuses a grant past MaxActiveGrants for its user.
	ErrTooManyGrants = errors.New("credentials: the user holds the most grants allowed")
	// ErrRefreshReuse is a rotated refresh token presented again: the grant is revoked.
	ErrRefreshReuse = errors.New("credentials: a rotated refresh token was used again; the grant is revoked")
)

// Grant is a user's consent to one OAuth client (design M.6). Its refresh token is stored as
// a hash and rotated on every use; the access tokens are sealed and name the grant, which must
// still exist when one is used.
type Grant struct {
	ID         string   `json:"id"`
	User       string   `json:"user"`
	ClientID   string   `json:"clientID"`
	ClientName string   `json:"clientName"`
	ClientKind string   `json:"clientKind"` // builtin, metadata or config
	Audience   string   `json:"audience"`   // api or mcp
	Scopes     []string `json:"scopes"`
	// Groups and RoleClaim are what the owner's identity carried when dash last saw them
	// interactively: the role is derived from them and the current mapping on every request.
	Groups    []string `json:"groups,omitempty"`
	RoleClaim []string `json:"roleClaim,omitempty"`
	// RefreshHash is the sha256: hash of the current refresh token; PrevRefresh the hashes of
	// the ones rotated away, newest last.
	RefreshHash string    `json:"refreshHash"`
	PrevRefresh []string  `json:"prevRefresh,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"` // CreatedAt + GrantLifetime
	// RefreshedAt is the consent or the last refresh: the grant ends GrantIdle after it.
	RefreshedAt time.Time  `json:"refreshedAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"` // the last refresh; nil before one
}

// Active reports whether the grant has neither reached its absolute expiry nor been idle for
// GrantIdle at now.
func (g Grant) Active(now time.Time) bool {
	return now.Before(g.ExpiresAt) && now.Before(g.RefreshedAt.Add(GrantIdle))
}

func (g Grant) clone() Grant {
	g.Scopes = slices.Clone(g.Scopes)
	g.Groups = slices.Clone(g.Groups)
	g.RoleClaim = slices.Clone(g.RoleClaim)
	g.PrevRefresh = slices.Clone(g.PrevRefresh)
	if g.LastUsedAt != nil {
		t := *g.LastUsedAt
		g.LastUsedAt = &t
	}
	return g
}

func cloneGrants(m map[string]Grant) map[string]Grant {
	if m == nil {
		return nil
	}
	out := make(map[string]Grant, len(m))
	for id, g := range m {
		out[id] = g.clone()
	}
	return out
}

// validGrant is the shape decode and AddGrant require of a record.
func validGrant(id string, g Grant) error {
	switch {
	case g.ID == "" || g.ID != id || !validGrantID(g.ID):
		return errors.New("grants: a grant's key must be its id")
	case g.User == "" || g.ClientID == "":
		return errors.New("grants: a grant needs a user and a client")
	case g.Audience != "api" && g.Audience != "mcp":
		return errors.New("grants: a grant needs audience api or mcp")
	case !validHash(g.RefreshHash):
		return errors.New("grants: a grant needs a sha256: refresh hash")
	case g.ExpiresAt.IsZero() || g.RefreshedAt.IsZero():
		return errors.New("grants: a grant needs its expiry and its last refresh")
	}
	return nil
}

func validHash(h string) bool {
	return strings.HasPrefix(h, HashPrefix) && len(h) == len(HashPrefix)+2*sha256.Size
}

func validGrantID(id string) bool {
	if len(id) != grantIDLen {
		return false
	}
	for i := range len(id) {
		if strings.IndexByte(patAlphabet, id[i]) < 0 {
			return false
		}
	}
	return true
}

// NewGrantID is a random grant ID: 10 base32 characters.
func NewGrantID() (string, error) {
	raw := make([]byte, grantIDLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	b := make([]byte, grantIDLen)
	for i, c := range raw {
		b[i] = patAlphabet[c&31]
	}
	return string(b), nil
}

// NewRefreshToken makes a refresh token of grant id and the hash to store.
func NewRefreshToken(id string) (token, hash string, err error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}
	token = RefreshPrefix + id + "_" + base64.RawURLEncoding.EncodeToString(secret)
	return token, HashToken(token), nil
}

// ParseRefreshToken returns the grant ID of a token of the refresh token shape, false for
// anything else. It says nothing about whether the token is current.
func ParseRefreshToken(token string) (id string, ok bool) {
	if len(token) != refreshLen || !strings.HasPrefix(token, RefreshPrefix) || token[len(RefreshPrefix)+grantIDLen] != '_' {
		return "", false
	}
	id = token[len(RefreshPrefix) : len(RefreshPrefix)+grantIDLen]
	if !validGrantID(id) {
		return "", false
	}
	for _, c := range []byte(token[len(token)-patSecretLen:]) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	return id, true
}

// AddGrant stores a new grant. Nothing changes when the write fails; ErrTooManyGrants when its
// user already holds MaxActiveGrants active ones.
func (s *Store) AddGrant(g Grant) error {
	g = g.clone()
	if err := validGrant(g.ID, g); err != nil {
		return err
	}
	full := false
	err := s.update(false, func(d *document) bool {
		if activeGrantsOf(d.Grants, g.User, g.CreatedAt) >= MaxActiveGrants {
			full = true
			return false
		}
		if d.Grants == nil {
			d.Grants = map[string]Grant{}
		}
		d.Grants[g.ID] = g
		return true
	})
	if full {
		return ErrTooManyGrants
	}
	return err
}

// ActiveGrants is how many grants of user are active at now.
func (s *Store) ActiveGrants(user string, now time.Time) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return activeGrantsOf(s.doc.Grants, user, now)
}

func activeGrantsOf(grants map[string]Grant, user string, now time.Time) int {
	n := 0
	for _, g := range grants {
		if g.User == user && g.Active(now) {
			n++
		}
	}
	return n
}

// Grants lists the grants of user, or every user's for "", oldest first.
func (s *Store) Grants(user string) []Grant {
	s.mu.RLock()
	out := make([]Grant, 0, len(s.doc.Grants))
	for _, g := range s.doc.Grants {
		if user == "" || g.User == user {
			out = append(out, g.clone())
		}
	}
	s.mu.RUnlock()
	slices.SortFunc(out, func(a, b Grant) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return out
}

// GrantByID returns one grant, active or not.
func (s *Store) GrantByID(id string) (Grant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.doc.Grants[id]
	return g.clone(), ok
}

// RefreshGrant returns the grant a refresh token belongs to, current or rotated away (its
// holder may still revoke it, RFC 7009), compared in constant time; false for any other token.
func (s *Store) RefreshGrant(token string) (Grant, bool) {
	id, ok := ParseRefreshToken(token)
	if !ok {
		return Grant{}, false
	}
	hash := []byte(HashToken(token))
	g, found := s.GrantByID(id)
	if !found {
		return Grant{}, false
	}
	match := subtle.ConstantTimeCompare(hash, []byte(g.RefreshHash))
	for _, h := range g.PrevRefresh {
		match |= subtle.ConstantTimeCompare(hash, []byte(h))
	}
	return g, match == 1
}

// RotateRefresh exchanges the refresh token for one whose hash is next: the grant's current
// token becomes a rotated one, and the grant is refreshed at now. The rotation is kept only
// when it was written, so a client whose refresh failed may retry with the same token. A token
// rotated away before is reuse: the grant is revoked (even when that write fails) and the
// answer is ErrRefreshReuse. An unknown token is ErrTokenUnknown, an inactive grant
// ErrTokenExpired.
func (s *Store) RotateRefresh(token, next string, now time.Time) (Grant, error) {
	id, ok := ParseRefreshToken(token)
	if !ok || !validHash(next) {
		return Grant{}, ErrTokenUnknown
	}
	hash := []byte(HashToken(token))
	s.mu.RLock()
	g, found := s.doc.Grants[id]
	if found {
		g = g.clone()
	}
	s.mu.RUnlock()
	if !found {
		return Grant{}, ErrTokenUnknown
	}
	if subtle.ConstantTimeCompare(hash, []byte(g.RefreshHash)) != 1 {
		reused := false
		for _, h := range g.PrevRefresh {
			if subtle.ConstantTimeCompare(hash, []byte(h)) == 1 {
				reused = true
			}
		}
		if !reused {
			return Grant{}, ErrTokenUnknown
		}
		_, _, _ = s.RevokeGrant(id)
		return Grant{}, ErrRefreshReuse
	}
	if !g.Active(now) {
		return Grant{}, ErrTokenExpired
	}
	var out Grant
	stale := false
	err := s.update(false, func(d *document) bool {
		cur, ok := d.Grants[id]
		if !ok || cur.RefreshHash != g.RefreshHash { // revoked or rotated meanwhile
			stale = true
			return false
		}
		cur.PrevRefresh = append(cur.PrevRefresh, cur.RefreshHash)
		if n := len(cur.PrevRefresh); n > maxPrevRefresh {
			cur.PrevRefresh = cur.PrevRefresh[n-maxPrevRefresh:]
		}
		at := now.UTC().Truncate(time.Second)
		cur.RefreshHash, cur.RefreshedAt, cur.LastUsedAt = next, at, &at
		d.Grants[id] = cur
		out = cur.clone()
		return true
	})
	switch {
	case stale:
		return Grant{}, ErrTokenUnknown
	case err != nil:
		return Grant{}, err
	}
	return out, nil
}

// RevokeGrant deletes a grant and returns it; false when there is none. Like RevokePAT, the
// revocation applies to this process even when the write fails, and the error says so.
func (s *Store) RevokeGrant(id string) (revoked Grant, ok bool, err error) {
	err = s.update(true, func(d *document) bool {
		revoked, ok = d.Grants[id]
		if !ok {
			return false
		}
		delete(d.Grants, id)
		if len(d.Grants) == 0 {
			d.Grants = nil
		}
		return true
	})
	if ok && err == nil && s.fileChanged() {
		err = ErrFileChanged
	}
	return revoked.clone(), ok, err
}

// RevokeGrants deletes every grant of user (never all of them for an empty user) and returns
// them. It holds in this process even when the write fails, as RevokeGrant does.
func (s *Store) RevokeGrants(user string) ([]Grant, error) {
	if user == "" {
		return nil, nil
	}
	return s.revokeGrantsWhere(func(g Grant) bool { return g.User == user })
}

// RevokeAllGrants deletes every grant of every user ("Sign out all users", contract D23).
func (s *Store) RevokeAllGrants() ([]Grant, error) {
	return s.revokeGrantsWhere(func(Grant) bool { return true })
}

func (s *Store) revokeGrantsWhere(match func(Grant) bool) (revoked []Grant, err error) {
	err = s.update(true, func(d *document) bool {
		for id, g := range d.Grants {
			if match(g) {
				revoked = append(revoked, g.clone())
				delete(d.Grants, id)
			}
		}
		if len(d.Grants) == 0 {
			d.Grants = nil
		}
		return len(revoked) > 0
	})
	if len(revoked) > 0 && err == nil && s.fileChanged() {
		err = ErrFileChanged
	}
	slices.SortFunc(revoked, func(a, b Grant) int { return strings.Compare(a.ID, b.ID) })
	return revoked, err
}
