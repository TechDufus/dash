package credentials

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/secret"
)

// Push storage (design P.2, contract D39-D41): the VAPID key pair and the push subscriptions.
// The VAPID private key and each subscription's endpoint, p256dh and auth are enc:v1-sealed
// with DASH_SECRET_KEY, bound to their path; the endpoint is a capability URL (anyone holding it
// can post to the device), so it is never stored, logged or audited in clear.

// Push subscription kinds, states and attention thresholds, as the API names them.
const (
	PushKindWebPush     = "webpush"
	PushKindUnifiedPush = "unifiedpush"

	PushPending  = "pending"
	PushVerified = "verified"
	PushStale    = "stale"
)

// Push limits (contract D40, D41).
const (
	// MaxPushSubscriptions is how many subscriptions one user may hold, in any state.
	MaxPushSubscriptions = 10
	// PushVerifyWindow is how long a subscription may stay pending before the sweep deletes it.
	PushVerifyWindow = 10 * time.Minute
	// PushVerifyAttempts is how many wrong codes delete a subscription.
	PushVerifyAttempts = 5
	// PushSweepInterval is how often the sweep runs.
	PushSweepInterval = time.Minute
	// PushCodeLen is the length of a verification code, in Crockford base32 characters.
	PushCodeLen = 8
)

// The sealing paths (the additional data of enc:v1).
const pushVAPIDPath = "credentials.push.vapid.privateKey"

func pushFieldPath(id, field string) string {
	return "credentials.push.subscriptions." + id + "." + field
}

// Push errors.
var (
	// ErrPushNoKey means the store has no DASH_SECRET_KEY to seal push data with (OpenPush was
	// not called, or called without a key).
	ErrPushNoKey = errors.New("credentials: push needs DASH_SECRET_KEY")
	// ErrPushEndpointTaken refuses an endpoint another user registered.
	ErrPushEndpointTaken = errors.New("credentials: another user holds this push endpoint")
	// ErrTooManyPushSubscriptions refuses a subscription past MaxPushSubscriptions.
	ErrTooManyPushSubscriptions = errors.New("credentials: the user holds the most push subscriptions allowed")
	// ErrPushNotFound: no such subscription (or not the user's).
	ErrPushNotFound = errors.New("credentials: no such push subscription")
	// ErrPushNotPending: the subscription is not waiting for its code.
	ErrPushNotPending = errors.New("credentials: the push subscription is not waiting for a code")
	// ErrPushWrongCode: the code does not match; the subscription stays, with fewer attempts left.
	ErrPushWrongCode = errors.New("credentials: wrong verification code")
	// ErrPushCodeExhausted: the last allowed wrong code; the subscription was deleted.
	ErrPushCodeExhausted = errors.New("credentials: too many wrong verification codes; the subscription was deleted")
)

// pushDoc is the push: section of credentials.yaml.
type pushDoc struct {
	VAPID         *vapidDoc                   `json:"vapid,omitempty"`
	Subscriptions map[string]pushSubscription `json:"subscriptions,omitempty"`
}

// vapidDoc is the VAPID key pair: the public key in clear (unpadded base64url of the
// uncompressed point), the 32-byte private scalar sealed.
type vapidDoc struct {
	PublicKey  string    `json:"publicKey"`
	PrivateKey string    `json:"privateKey"`
	CreatedAt  time.Time `json:"createdAt"`
}

// pushSubscription is one stored subscription.
type pushSubscription struct {
	User       string `json:"user"`
	Kind       string `json:"kind"`
	DeviceName string `json:"deviceName"`
	Approvals  bool   `json:"approvals"`
	Attention  string `json:"attention"`
	// Endpoint, P256dh and Auth are sealed. EndpointHash (sha256 hex of the endpoint as
	// endpointKey writes it) finds a re-registration without opening every row; KeysHash says
	// whether the keys changed.
	Endpoint     string `json:"endpoint"`
	P256dh       string `json:"p256dh"`
	Auth         string `json:"auth"`
	EndpointHash string `json:"endpointHash"`
	KeysHash     string `json:"keysHash"`
	// EndpointHost is the push service's host, what the API and the audit may show.
	EndpointHost string `json:"endpointHost"`
	State        string `json:"state"`
	// Code is the pending code, sealed: verification opens it and compares in constant time, and
	// a registration again pushes the same one. Attempts counts wrong codes. Both are cleared on
	// verification.
	Code         string     `json:"code,omitempty"`
	Attempts     int        `json:"attempts,omitempty"`
	PendingSince *time.Time `json:"pendingSince,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	VerifiedAt   *time.Time `json:"verifiedAt,omitempty"`
	// Epoch and GlobalEpoch are the user's session epoch and the global epoch when the row was
	// registered (any bump since turned it stale, so a verified row holds them too). A bump of
	// either (a logout, "Log out everywhere", "Sign out all users") turns the row stale: a
	// removed or signed-out user's device gets nothing.
	Epoch       uint64 `json:"epoch,omitempty"`
	GlobalEpoch uint64 `json:"globalEpoch,omitempty"`
}

// stalePushByEpoch turns stale every subscription whose recorded epochs are no longer the
// document's.
func stalePushByEpoch(d *document) (n int) {
	if d.Push == nil {
		return 0
	}
	for id, sub := range d.Push.Subscriptions {
		if sub.State == PushStale || sub.Epoch == d.Epochs[sub.User] && sub.GlobalEpoch == d.GlobalEpoch {
			continue
		}
		sub.State, sub.Code, sub.Attempts, sub.PendingSince = PushStale, "", 0, nil
		d.Push.Subscriptions[id] = sub
		n++
	}
	return n
}

func (p pushSubscription) clone() pushSubscription {
	if p.PendingSince != nil {
		t := *p.PendingSince
		p.PendingSince = &t
	}
	if p.VerifiedAt != nil {
		t := *p.VerifiedAt
		p.VerifiedAt = &t
	}
	return p
}

// clonePush is a deep copy for update.
func clonePush(p *pushDoc) *pushDoc {
	if p == nil {
		return nil
	}
	out := &pushDoc{}
	if p.VAPID != nil {
		v := *p.VAPID
		out.VAPID = &v
	}
	if p.Subscriptions != nil {
		out.Subscriptions = make(map[string]pushSubscription, len(p.Subscriptions))
		for id, s := range p.Subscriptions {
			out.Subscriptions[id] = s.clone()
		}
	}
	return out
}

// holdsPush reports whether the document carries push data, which keeps it at schemaVersion 2.
func holdsPush(p *pushDoc) bool {
	return p != nil && (p.VAPID != nil || len(p.Subscriptions) > 0)
}

// validPushSubscription is the shape decode requires of a stored subscription.
func validPushSubscription(id string, p pushSubscription) error {
	if !validPushID(id) || p.User == "" || !secret.IsEncrypted(p.Endpoint) || !secret.IsEncrypted(p.P256dh) ||
		!secret.IsEncrypted(p.Auth) || p.EndpointHash == "" || !slices.Contains([]string{PushPending, PushVerified, PushStale}, p.State) ||
		!slices.Contains([]string{PushKindWebPush, PushKindUnifiedPush}, p.Kind) || p.Code != "" && !secret.IsEncrypted(p.Code) {
		return fmt.Errorf("push subscription %q is malformed", id)
	}
	return nil
}

// PushSubscription is a subscription as dash shows it: never the endpoint or the keys.
type PushSubscription struct {
	ID           string
	User         string
	Kind         string // webpush or unifiedpush
	DeviceName   string
	Approvals    bool
	Attention    string // crit, warn or off
	State        string // pending, verified or stale
	EndpointHost string
	CreatedAt    time.Time
	VerifiedAt   *time.Time
}

func (p pushSubscription) view(id string) PushSubscription {
	p = p.clone()
	return PushSubscription{
		ID: id, User: p.User, Kind: p.Kind, DeviceName: p.DeviceName, Approvals: p.Approvals, Attention: p.Attention,
		State: p.State, EndpointHost: p.EndpointHost, CreatedAt: p.CreatedAt, VerifiedAt: p.VerifiedAt,
	}
}

// PushTarget is what the sender needs of a subscription: its view and the opened secrets.
type PushTarget struct {
	PushSubscription
	Endpoint string
	P256dh   string // unpadded base64url, as registered
	Auth     string
}

// PushInput is a subscription to register. ID is the ID a new row takes (NewPushID; empty: one
// is made); a replaced row keeps its own.
type PushInput struct {
	ID                     string
	User, Kind, DeviceName string
	Approvals              bool
	Attention              string
	Endpoint               string // https URL; checked by the caller
	EndpointHost           string
	P256dh, Auth           string // checked by the caller (push.DecodeKeys)
}

// PushOpen is what OpenPush found.
type PushOpen struct {
	// Lost: the stored VAPID key could not be opened (DASH_SECRET_KEY rotated without
	// DASH_SECRET_KEY_PREVIOUS) and was dropped; PushVAPIDKey makes a new one.
	Lost bool
	// Stale is how many subscriptions turned stale: every web-push one when the VAPID key was
	// lost (they are bound to it), and every one whose own sealed fields no longer open.
	Stale int
	// Resealed is how many sealed values moved from DASH_SECRET_KEY_PREVIOUS to DASH_SECRET_KEY.
	Resealed int
	// SignedOut is how many subscriptions turned stale because their owner's session epoch or
	// the global one moved on since they were registered (a bump this file missed).
	SignedOut int
}

// OpenPush readies the store for push with keys (contract D39): it takes keys for every later
// push call and re-seals the values sealed with the previous key. A VAPID key that cannot be
// opened is dropped, and every web-push subscription, bound to it, turns stale; a UnifiedPush
// one keeps working when its own fields still open (VAPID is optional there). A subscription
// whose sealed fields cannot be opened turns stale too: it stays listed, so its device is asked
// to subscribe again, and registering the same endpoint replaces it. OpenPush generates no key:
// PushVAPIDKey does at first need, so an install that never uses push keeps a schemaVersion 1
// file. The caller logs what PushOpen reports. Nothing is written when nothing changed.
func (s *Store) OpenPush(keys *secret.Keyring) (PushOpen, error) {
	if keys == nil {
		return PushOpen{}, ErrPushNoKey
	}
	s.mu.Lock()
	s.pushKeys = keys
	s.mu.Unlock()
	var res PushOpen
	err := s.update(false, func(d *document) bool {
		res = PushOpen{}
		p := d.Push
		if p == nil {
			return false
		}
		changed := false
		// open checks that v opens at path and re-seals it under the current key when it was
		// sealed with the previous one.
		open := func(path string, v *string) bool {
			plain, err := keys.Decrypt(path, *v)
			if err != nil {
				return false
			}
			if keys.NeedsRekey(*v) {
				if *v, err = keys.Encrypt(path, plain); err != nil {
					return false
				}
				res.Resealed++
				changed = true
			}
			return true
		}
		if p.VAPID != nil && !open(pushVAPIDPath, &p.VAPID.PrivateKey) {
			p.VAPID, res.Lost, changed = nil, true, true
		}
		for id, sub := range p.Subscriptions {
			ok := open(pushFieldPath(id, "endpoint"), &sub.Endpoint)
			ok = open(pushFieldPath(id, "p256dh"), &sub.P256dh) && ok
			ok = open(pushFieldPath(id, "auth"), &sub.Auth) && ok
			if sub.Code != "" {
				ok = open(pushFieldPath(id, "code"), &sub.Code) && ok
			}
			if sub.State != PushStale && (!ok || res.Lost && sub.Kind == PushKindWebPush) {
				sub.State, sub.Code, sub.Attempts, sub.PendingSince = PushStale, "", 0, nil
				res.Stale++
				changed = true
			}
			p.Subscriptions[id] = sub
		}
		if res.SignedOut = stalePushByEpoch(d); res.SignedOut > 0 {
			changed = true
		}
		return changed
	})
	if err != nil {
		return PushOpen{}, err
	}
	return res, nil
}

// PushVAPIDKey is the VAPID key pair, the 32-byte private scalar and the 65-byte uncompressed
// public point, generated and stored at the first call (contract D39). Nothing is stored when
// the write fails.
func (s *Store) PushVAPIDKey(now time.Time) (priv, pub []byte, err error) {
	keys, err := s.pushKeyring()
	if err != nil {
		return nil, nil, err
	}
	var v *vapidDoc
	var genErr error
	err = s.update(false, func(d *document) bool {
		if d.Push != nil && d.Push.VAPID != nil {
			v = d.Push.VAPID
			return false
		}
		if v, genErr = newVAPID(keys, now); genErr != nil {
			return false
		}
		if d.Push == nil {
			d.Push = &pushDoc{}
		}
		d.Push.VAPID = v
		return true
	})
	if genErr != nil {
		return nil, nil, genErr
	}
	if err != nil {
		return nil, nil, err
	}
	plain, err := keys.Decrypt(pushVAPIDPath, v.PrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("credentials: the VAPID key: %w", err)
	}
	if priv, err = base64.RawURLEncoding.DecodeString(plain); err != nil {
		return nil, nil, err
	}
	if pub, err = base64.RawURLEncoding.DecodeString(v.PublicKey); err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

// newVAPID generates a VAPID key pair with its private key sealed.
func newVAPID(keys *secret.Keyring, now time.Time) (*vapidDoc, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	priv, err := k.Bytes()
	if err != nil {
		return nil, err
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	sealed, err := keys.Encrypt(pushVAPIDPath, base64.RawURLEncoding.EncodeToString(priv))
	if err != nil {
		return nil, err
	}
	return &vapidDoc{PublicKey: base64.RawURLEncoding.EncodeToString(pub), PrivateKey: sealed, CreatedAt: now.UTC().Truncate(time.Second)}, nil
}

// pushKeyring is the keyring OpenPush took, or ErrPushNoKey.
func (s *Store) pushKeyring() (*secret.Keyring, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pushKeys == nil {
		return nil, ErrPushNoKey
	}
	return s.pushKeys, nil
}

// pushIDLen is the base32 length after sub_.
const pushIDLen = 26

// PushIDPrefix starts a subscription ID: sub_<26 base32>.
const PushIDPrefix = "sub_"

// NewPushID makes a subscription ID: sub_<26 base32>.
func NewPushID() (string, error) {
	raw := make([]byte, pushIDLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	b := []byte(PushIDPrefix)
	for _, c := range raw {
		b = append(b, patAlphabet[c&31])
	}
	return string(b), nil
}

func validPushID(id string) bool {
	rest, ok := strings.CutPrefix(id, PushIDPrefix)
	return ok && len(rest) == pushIDLen && strings.Trim(rest, patAlphabet) == ""
}

// crockford is the Crockford base32 alphabet of the verification codes.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewPushCode makes a verification code: PushCodeLen Crockford base32 characters (40 bits).
func NewPushCode() (string, error) {
	raw := make([]byte, PushCodeLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i, c := range raw {
		raw[i] = crockford[c&31]
	}
	return string(raw), nil
}

// normalizeCode reads a code as Crockford base32 does: case-insensitive, I and L as 1, O as 0,
// hyphens and spaces ignored.
func normalizeCode(code string) string {
	r := strings.NewReplacer("-", "", " ", "", "I", "1", "L", "1", "O", "0")
	return r.Replace(strings.ToUpper(code))
}

func hashHex(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// endpointKey is the endpoint as dash compares it: the scheme (url.Parse lowercases it) and the
// host lowercased, the host without a trailing dot, no :443 on https, the dot segments of the
// path resolved. Two spellings of one URL are one endpoint.
func endpointKey(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	host, port := strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" && !(port == "443" && u.Scheme == "https") {
		host += ":" + port
	}
	u.Host = host
	return u.ResolveReference(u).String()
}

// AddPushSubscription registers a subscription and reports the code the caller must push to it
// as its only push, or "" when it needs none.
//
// The same endpoint for the same user replaces that row and keeps its ID. A verified row stays
// verified when the keys are unchanged (only the name, kind and notify settings change); a
// pending row stays pending with its code, its wrong attempts and its ten minutes, and that
// code is pushed again; any other row is pending again with code. The same endpoint for another
// user is ErrPushEndpointTaken; a new row past MaxPushSubscriptions is
// ErrTooManyPushSubscriptions. Nothing changes when the write fails.
func (s *Store) AddPushSubscription(in PushInput, code string, now time.Time) (sub PushSubscription, sendCode string, err error) {
	keys, err := s.pushKeyring()
	if err != nil {
		return PushSubscription{}, "", err
	}
	now = now.UTC().Truncate(time.Second)
	endpointHash, keysHash := hashHex(endpointKey(in.Endpoint)), hashHex(in.P256dh, in.Auth)
	newID := in.ID
	if newID == "" {
		if newID, err = NewPushID(); err != nil {
			return PushSubscription{}, "", err
		}
	} else if !validPushID(newID) {
		return PushSubscription{}, "", fmt.Errorf("credentials: %q is not a push subscription ID", newID)
	}
	var refusal error
	var id string
	var row pushSubscription
	err = s.update(false, func(d *document) bool {
		if d.Push == nil {
			d.Push = &pushDoc{}
		}
		subs := d.Push.Subscriptions
		id, sendCode = "", ""
		count := 0
		for sid, sub := range subs {
			if sub.EndpointHash == endpointHash {
				if sub.User != in.User {
					refusal = ErrPushEndpointTaken
					return false
				}
				id = sid
			}
			if sub.User == in.User {
				count++
			}
		}
		prev, replacing := subs[id]
		if !replacing {
			if count >= MaxPushSubscriptions {
				refusal = ErrTooManyPushSubscriptions
				return false
			}
			id = newID
		}
		row = pushSubscription{
			User: in.User, Kind: in.Kind, DeviceName: in.DeviceName, Approvals: in.Approvals, Attention: in.Attention,
			EndpointHash: endpointHash, KeysHash: keysHash, EndpointHost: in.EndpointHost, CreatedAt: now,
			Epoch: d.Epochs[in.User], GlobalEpoch: d.GlobalEpoch,
		}
		if replacing {
			row.CreatedAt = prev.CreatedAt
		}
		switch {
		case replacing && prev.State == PushVerified && prev.KeysHash == keysHash:
			row.State, row.VerifiedAt = PushVerified, prev.VerifiedAt
		case replacing && prev.State == PushPending && prev.Code != "":
			current, oerr := keys.Decrypt(pushFieldPath(id, "code"), prev.Code)
			if oerr == nil {
				row.State, row.Code, row.Attempts, row.PendingSince = PushPending, prev.Code, prev.Attempts, prev.PendingSince
				sendCode = current
				break
			}
			fallthrough
		default:
			sealed, serr := keys.Encrypt(pushFieldPath(id, "code"), code)
			if serr != nil {
				refusal = serr
				return false
			}
			t := now
			row.State, row.Code, row.PendingSince = PushPending, sealed, &t
			if replacing && prev.State == PushPending {
				row.Attempts = prev.Attempts
			}
			sendCode = code
		}
		for field, v := range map[string]struct {
			dst   *string
			plain string
		}{"endpoint": {&row.Endpoint, in.Endpoint}, "p256dh": {&row.P256dh, in.P256dh}, "auth": {&row.Auth, in.Auth}} {
			sealed, serr := keys.Encrypt(pushFieldPath(id, field), v.plain)
			if serr != nil {
				refusal = serr
				return false
			}
			*v.dst = sealed
		}
		if d.Push.Subscriptions == nil {
			d.Push.Subscriptions = map[string]pushSubscription{}
		}
		d.Push.Subscriptions[id] = row
		return true
	})
	if refusal != nil {
		return PushSubscription{}, "", refusal
	}
	if err != nil {
		return PushSubscription{}, "", err
	}
	return row.view(id), sendCode, nil
}

// PushSubscriptionByEndpoint returns the subscription registered with endpoint, whoever holds it.
func (s *Store) PushSubscriptionByEndpoint(endpoint string) (PushSubscription, bool) {
	h := hashHex(endpointKey(endpoint))
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.doc.Push != nil {
		for id, sub := range s.doc.Push.Subscriptions {
			if sub.EndpointHash == h {
				return sub.view(id), true
			}
		}
	}
	return PushSubscription{}, false
}

// VerifyPushSubscription checks code against the pending subscription id of user, in constant
// time. A match verifies it; a miss is ErrPushWrongCode, and the PushVerifyAttempts-th miss
// deletes it (ErrPushCodeExhausted). Another user's subscription is ErrPushNotFound.
func (s *Store) VerifyPushSubscription(user, id, code string, now time.Time) (PushSubscription, error) {
	keys, err := s.pushKeyring()
	if err != nil {
		return PushSubscription{}, err
	}
	var out PushSubscription
	var result error
	err = s.update(false, func(d *document) bool {
		sub, ok := pushRow(d, id)
		switch {
		case !ok || sub.User != user:
			result = ErrPushNotFound
			return false
		case sub.State != PushPending:
			result = ErrPushNotPending
			return false
		}
		want, oerr := keys.Decrypt(pushFieldPath(id, "code"), sub.Code)
		if oerr != nil || subtle.ConstantTimeCompare([]byte(normalizeCode(want)), []byte(normalizeCode(code))) != 1 {
			sub.Attempts++
			if sub.Attempts >= PushVerifyAttempts {
				deletePushRow(d, id)
				result = ErrPushCodeExhausted
				return true
			}
			d.Push.Subscriptions[id] = sub
			result = ErrPushWrongCode
			return true
		}
		t := now.UTC().Truncate(time.Second)
		sub.State, sub.VerifiedAt, sub.Code, sub.Attempts, sub.PendingSince = PushVerified, &t, "", 0, nil
		d.Push.Subscriptions[id] = sub
		out = sub.view(id)
		return true
	})
	if err != nil {
		return PushSubscription{}, err
	}
	return out, result
}

func pushRow(d *document, id string) (pushSubscription, bool) {
	if d.Push == nil {
		return pushSubscription{}, false
	}
	sub, ok := d.Push.Subscriptions[id]
	return sub.clone(), ok
}

func deletePushRow(d *document, id string) {
	delete(d.Push.Subscriptions, id)
	if len(d.Push.Subscriptions) == 0 {
		d.Push.Subscriptions = nil
	}
}

// PushSubscriptions lists the subscriptions of user, or everyone's for "", oldest first.
func (s *Store) PushSubscriptions(user string) []PushSubscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PushSubscription
	if s.doc.Push != nil {
		for id, sub := range s.doc.Push.Subscriptions {
			if user == "" || sub.User == user {
				out = append(out, sub.view(id))
			}
		}
	}
	slices.SortFunc(out, func(a, b PushSubscription) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// PushSubscriptionByID returns one subscription.
func (s *Store) PushSubscriptionByID(id string) (PushSubscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := pushRow(&s.doc, id)
	if !ok {
		return PushSubscription{}, false
	}
	return sub.view(id), true
}

// PushTarget opens subscription id for sending: its endpoint and keys.
func (s *Store) PushTarget(id string) (PushTarget, error) {
	keys, err := s.pushKeyring()
	if err != nil {
		return PushTarget{}, err
	}
	s.mu.RLock()
	sub, ok := pushRow(&s.doc, id)
	s.mu.RUnlock()
	if !ok {
		return PushTarget{}, ErrPushNotFound
	}
	t := PushTarget{PushSubscription: sub.view(id)}
	for field, v := range map[string]struct {
		dst    *string
		sealed string
	}{"endpoint": {&t.Endpoint, sub.Endpoint}, "p256dh": {&t.P256dh, sub.P256dh}, "auth": {&t.Auth, sub.Auth}} {
		if *v.dst, err = keys.Decrypt(pushFieldPath(id, field), v.sealed); err != nil {
			return PushTarget{}, fmt.Errorf("credentials: push subscription %s: %w", id, err)
		}
	}
	return t, nil
}

// DeletePushSubscription deletes a subscription and returns it; false when there is none. Like
// a token revocation, the deletion holds in this process even when the write fails.
func (s *Store) DeletePushSubscription(id string) (deleted PushSubscription, ok bool, err error) {
	err = s.update(true, func(d *document) bool {
		var sub pushSubscription
		if sub, ok = pushRow(d, id); !ok {
			return false
		}
		deleted = sub.view(id)
		deletePushRow(d, id)
		return true
	})
	if ok && err == nil && s.fileChanged() {
		err = ErrFileChanged
	}
	return deleted, ok, err
}

// SweepPushSubscriptions deletes the subscriptions pending for PushVerifyWindow or longer at now
// and returns them; the deletion holds in this process even when the write fails.
func (s *Store) SweepPushSubscriptions(now time.Time) ([]PushSubscription, error) {
	var swept []PushSubscription
	err := s.update(true, func(d *document) bool {
		swept = nil
		if d.Push == nil {
			return false
		}
		for id, sub := range d.Push.Subscriptions {
			if sub.State == PushPending && sub.PendingSince != nil && !now.Before(sub.PendingSince.Add(PushVerifyWindow)) {
				swept = append(swept, sub.view(id))
				deletePushRow(d, id)
			}
		}
		return len(swept) > 0
	})
	return swept, err
}

// RunPushSweep sweeps every interval until ctx ends; onSwept hears of each run that deleted
// something (or failed to write it). now is the clock.
func (s *Store) RunPushSweep(ctx context.Context, every time.Duration, now func() time.Time, onSwept func([]PushSubscription, error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if swept, err := s.SweepPushSubscriptions(now()); (len(swept) > 0 || err != nil) && onSwept != nil {
				onSwept(swept, err)
			}
		}
	}
}
