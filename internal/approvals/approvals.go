// Package approvals keeps the approval requests of agent actions (design O, contract D50–D52):
// an AI agent asked to run a plugin action, and the same user approves or denies it. The store
// knows the requests' lifecycle (pending, then approved, denied or expired) and who may see and
// decide them; internal/api checks, audits and runs the action.
//
// Requests live in memory only: a restart forgets them, and a well-formed id the store does not
// know answers ErrGone, as an expired one does.
package approvals

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// IDPrefix starts every request id: appr_ and 26 base32 characters (crypto/rand.Text).
const IDPrefix = "appr_"

// Expiry is how long a request waits for a decision; Retention is how long a decided or expired
// request stays readable after its expiry.
const (
	Expiry    = model.ApprovalExpiryMinutes * time.Minute
	Retention = Expiry
)

// Errors of the store. The API answers them 410 approval_expired, 403 forbidden, 409 conflict
// and 429 rate_limited.
var (
	ErrGone       = errors.New("the approval request expired or does not exist")
	ErrForbidden  = errors.New("the approval request belongs to another user")
	ErrNotPending = errors.New("the approval request is no longer pending")
	ErrTooMany    = errors.New("too many pending approval requests")
)

// Request is one approval request. The caller fills the descriptive fields; the store sets ID,
// CreatedAt and ExpiresAt, and keeps the state.
type Request struct {
	ID             string
	User           string
	TokenID        string // the agent's personal access token, or
	GrantID        string // its OAuth grant
	CredentialName string
	ClientName     string
	Plugin         string // the plugin instance
	Action         string
	Title          string
	Tool           string
	Summary        string
	Target         model.ApprovalTarget
	Input          json.RawMessage // canonical, without confirm
	InputSHA256    string
	Destructive    bool
	Confirm        model.Confirm
	CreatedAt      time.Time
	ExpiresAt      time.Time

	state     string
	claimed   bool // its owner's decision is under way: nobody else can decide it
	decidedAt *time.Time
	decidedBy string
	reason    string
	outcome   *model.ApprovalOutcome
	done      chan struct{} // closed when it leaves pending
}

// Info is the request as the API shows it.
func (r *Request) Info() model.ApprovalInfo {
	info := model.ApprovalInfo{
		ID: r.ID, State: cmp.Or(r.state, model.ApprovalStatePending), User: r.User, TokenID: r.TokenID, GrantID: r.GrantID,
		CredentialName: r.CredentialName, ClientName: r.ClientName, Plugin: r.Plugin, Action: r.Action, Title: r.Title,
		Tool: r.Tool, Summary: r.Summary, Target: r.Target, Input: slices.Clone(r.Input), InputSHA256: r.InputSHA256,
		Destructive: r.Destructive, Confirm: r.Confirm, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		DecidedBy: r.decidedBy, Reason: r.reason,
	}
	if r.decidedAt != nil {
		t := *r.decidedAt
		info.DecidedAt = &t
	}
	if r.outcome != nil {
		o := *r.outcome
		info.Outcome = &o
	}
	if info.Input == nil {
		info.Input = json.RawMessage("null")
	}
	return info
}

// credential is the key of the agent's credential: its token or its grant.
func (r *Request) credential() string {
	if r.TokenID != "" {
		return "token:" + r.TokenID
	}
	return "grant:" + r.GrantID
}

// Options configure a Store.
type Options struct {
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Changed is called after any transition of a user's requests (created, claimed back,
	// decided, expired), outside the store's lock: the API publishes that user's core.approvals.
	Changed func(user string)
	// Expired is called once for each request that expired undecided, outside the lock: the API
	// records the event.
	Expired func(model.ApprovalInfo)
}

// Store keeps the requests.
type Store struct {
	o    Options
	mu   sync.Mutex
	byID map[string]*Request
}

// New is an empty store.
func New(o Options) *Store {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Store{o: o, byID: map[string]*Request{}}
}

// WellFormed reports whether id has the shape of a request id; any other id is not one dash
// could have made.
func WellFormed(id string) bool {
	rest, ok := strings.CutPrefix(id, IDPrefix)
	if !ok || len(rest) != 26 {
		return false
	}
	for _, c := range rest {
		if !(c >= 'A' && c <= 'Z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// Create adds r as a pending request and returns it with created true. A pending request of the
// same credential, plugin, action and input hash is returned instead (created false), unchanged.
// A user who already has model.ApprovalsPendingMax pending requests gets ErrTooMany.
func (s *Store) Create(r Request) (info model.ApprovalInfo, created bool, err error) {
	s.mu.Lock()
	expired := s.sweepLocked()
	pending := 0
	for _, o := range s.byID {
		if o.state != model.ApprovalStatePending {
			continue
		}
		if o.credential() == r.credential() && o.Plugin == r.Plugin && o.Action == r.Action && o.InputSHA256 == r.InputSHA256 {
			info = o.Info()
			s.mu.Unlock()
			s.notify(expired, "")
			return info, false, nil
		}
		if o.User == r.User {
			pending++
		}
	}
	if pending >= model.ApprovalsPendingMax {
		s.mu.Unlock()
		s.notify(expired, "")
		return model.ApprovalInfo{}, false, ErrTooMany
	}
	now := s.o.Now()
	r.ID = IDPrefix + rand.Text()
	r.CreatedAt, r.ExpiresAt = now, now.Add(Expiry)
	r.Input = slices.Clone(r.Input)
	r.state, r.claimed, r.decidedAt, r.decidedBy, r.reason, r.outcome = model.ApprovalStatePending, false, nil, "", "", nil
	r.done = make(chan struct{})
	s.byID[r.ID] = &r
	info = r.Info()
	s.mu.Unlock()
	s.notify(expired, r.User)
	return info, true, nil
}

// Get is request id for user, its owner: ErrForbidden for another user's, ErrGone for one the
// store does not know (expired and forgotten, never made, or made before a restart).
func (s *Store) Get(id, user string) (model.ApprovalInfo, error) {
	s.mu.Lock()
	expired := s.sweepLocked()
	r, err := s.ownedLocked(id, user)
	var info model.ApprovalInfo
	if err == nil {
		info = r.Info()
	}
	s.mu.Unlock()
	s.notify(expired, "")
	return info, err
}

// Claim takes request id for its owner's decision: the request stays pending, but nobody can
// claim it again until Release, Deny or Approve. It returns a copy of the request. ErrGone for an
// unknown or expired request, ErrForbidden for another user's, ErrNotPending for one decided or
// already claimed. Claim is what makes an approval run once.
func (s *Store) Claim(id, user string) (Request, error) {
	s.mu.Lock()
	expired := s.sweepLocked()
	r, err := s.ownedLocked(id, user)
	switch {
	case err != nil:
	case r.state == model.ApprovalStateExpired:
		err = ErrGone
	case r.state != model.ApprovalStatePending || r.claimed:
		err = ErrNotPending
	}
	var out Request
	if err == nil {
		r.claimed = true
		out = *r
		out.Input = slices.Clone(r.Input)
	}
	s.mu.Unlock()
	s.notify(expired, "")
	return out, err
}

// Release gives a claimed request back to pending (its decision could not be recorded). One
// that expired meanwhile expires now.
func (s *Store) Release(id string) {
	s.mu.Lock()
	user := ""
	if r := s.byID[id]; r != nil && r.claimed {
		r.claimed = false
		user = r.User
	}
	expired := s.sweepLocked()
	s.mu.Unlock()
	s.notify(expired, user)
}

// Deny ends a claimed request as denied: by its owner (by, reason "") or by dash, which names
// the reason (the requester lost a permission) and no decider.
func (s *Store) Deny(id, by, reason string) (model.ApprovalInfo, error) {
	return s.finish(id, model.ApprovalStateDenied, by, reason, nil)
}

// Approve ends a claimed request as approved by by, with the action's outcome.
func (s *Store) Approve(id, by string, outcome model.ApprovalOutcome) (model.ApprovalInfo, error) {
	return s.finish(id, model.ApprovalStateApproved, by, "", &outcome)
}

func (s *Store) finish(id, state, by, reason string, outcome *model.ApprovalOutcome) (model.ApprovalInfo, error) {
	s.mu.Lock()
	r := s.byID[id]
	if r == nil || !r.claimed {
		s.mu.Unlock()
		return model.ApprovalInfo{}, ErrNotPending
	}
	now := s.o.Now()
	r.claimed, r.state, r.reason, r.outcome = false, state, reason, outcome
	if by != "" {
		r.decidedAt, r.decidedBy = &now, by
	}
	close(r.done)
	info := r.Info()
	expired := s.sweepLocked()
	s.mu.Unlock()
	s.notify(expired, r.User)
	return info, nil
}

// Pending is user's pending requests, oldest first.
func (s *Store) Pending(user string) []model.ApprovalInfo {
	s.mu.Lock()
	expired := s.sweepLocked()
	out := []model.ApprovalInfo{}
	for _, r := range s.byID {
		if r.User == user && r.state == model.ApprovalStatePending {
			out = append(out, r.Info())
		}
	}
	s.mu.Unlock()
	s.notify(expired, "")
	slices.SortFunc(out, func(a, b model.ApprovalInfo) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return out
}

// Wait blocks until request id leaves pending (decided or expired), ctx ends, or the request is
// unknown, and reports whether it left pending. The caller reads it again with Get.
func (s *Store) Wait(ctx context.Context, id string) bool {
	s.mu.Lock()
	r := s.byID[id]
	s.mu.Unlock()
	if r == nil {
		return false
	}
	// An undecided request expires at ExpiresAt; nothing else would wake the waiter then.
	expiry := time.NewTimer(max(r.ExpiresAt.Sub(s.o.Now()), 0))
	defer expiry.Stop()
	select {
	case <-r.done:
		return true
	case <-expiry.C:
		s.Sweep()
		select {
		case <-r.done:
			return true
		default:
			return false
		}
	case <-ctx.Done():
		return false
	}
}

// Sweep expires the requests whose time is up and forgets those past their retention. The store
// sweeps on every call; Run sweeps when nobody calls, so an expiry is published and recorded.
func (s *Store) Sweep() {
	s.mu.Lock()
	expired := s.sweepLocked()
	s.mu.Unlock()
	s.notify(expired, "")
}

// Run sweeps every interval until ctx ends.
func (s *Store) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep()
		}
	}
}

// ownedLocked is request id if user owns it.
func (s *Store) ownedLocked(id, user string) (*Request, error) {
	r := s.byID[id]
	switch {
	case r == nil:
		return nil, ErrGone
	case r.User != user:
		return nil, ErrForbidden
	}
	return r, nil
}

// sweepLocked expires the undecided requests whose time is up (a claimed one is being decided
// and is left to finish) and forgets every request past its retention. It returns the expired
// ones, for notify.
func (s *Store) sweepLocked() []model.ApprovalInfo {
	now := s.o.Now()
	var expired []model.ApprovalInfo
	for id, r := range s.byID {
		if r.state == model.ApprovalStatePending && !r.claimed && !now.Before(r.ExpiresAt) {
			r.state = model.ApprovalStateExpired
			close(r.done)
			expired = append(expired, r.Info())
		}
		if r.state != model.ApprovalStatePending && !now.Before(r.ExpiresAt.Add(Retention)) {
			delete(s.byID, id)
		}
	}
	return expired
}

// notify runs the callbacks for the expired requests and for user ("" for none), each user once.
func (s *Store) notify(expired []model.ApprovalInfo, user string) {
	users := map[string]bool{}
	for _, info := range expired {
		if s.o.Expired != nil {
			s.o.Expired(info)
		}
		users[info.User] = true
	}
	if user != "" {
		users[user] = true
	}
	if s.o.Changed == nil {
		return
	}
	for u := range users {
		s.o.Changed(u)
	}
}
