package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// recorder notes the callbacks.
type recorder struct {
	mu      sync.Mutex
	changed []string
	expired []string
}

func (r *recorder) options(c *clock) Options {
	return Options{
		Now: c.now,
		Changed: func(user string) {
			r.mu.Lock()
			r.changed = append(r.changed, user)
			r.mu.Unlock()
		},
		Expired: func(info model.ApprovalInfo) {
			r.mu.Lock()
			r.expired = append(r.expired, info.ID)
			r.mu.Unlock()
		},
	}
}

func (r *recorder) take() (changed, expired []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed, expired = r.changed, r.expired
	r.changed, r.expired = nil, nil
	return changed, expired
}

func request(user, token, input string) Request {
	return Request{
		User: user, TokenID: token, Plugin: "kubernetes", Action: "restart", Tool: "kubernetes_restart",
		Summary: "Restart Deployment apps/web", Target: model.ApprovalTarget{Kind: "Deployment", Namespace: "apps", Name: "web"},
		Input: json.RawMessage(input), InputSHA256: "sha-" + input, Destructive: true, Confirm: model.ConfirmDialog,
	}
}

func TestCreateMakesAPendingRequestAndNotifiesItsUser(t *testing.T) {
	c, rec := newClock(), &recorder{}
	s := New(rec.options(c))
	info, created, err := s.Create(request("alice", "t1", `{"a":1}`))
	if err != nil || !created {
		t.Fatalf("Create = %v, %v", created, err)
	}
	if !WellFormed(info.ID) || info.State != model.ApprovalStatePending || info.User != "alice" ||
		!info.CreatedAt.Equal(c.now()) || !info.ExpiresAt.Equal(c.now().Add(10*time.Minute)) ||
		info.DecidedAt != nil || info.Outcome != nil || string(info.Input) != `{"a":1}` {
		t.Errorf("info = %+v", info)
	}
	if changed, _ := rec.take(); !slices.Equal(changed, []string{"alice"}) {
		t.Errorf("changed = %v", changed)
	}
	if got := s.Pending("alice"); len(got) != 1 || got[0].ID != info.ID {
		t.Errorf("Pending = %+v", got)
	}
	if got := s.Pending("bob"); got == nil || len(got) != 0 {
		t.Errorf("bob's Pending = %#v, want an empty list", got)
	}
}

func TestWellFormed(t *testing.T) {
	s := New(Options{})
	info, _, _ := s.Create(request("alice", "t1", "{}"))
	for id, want := range map[string]bool{
		info.ID:                           true,
		"appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ": true,
		"appr_abcdefghijklmnopqrstuvwxyz": false,
		"appr_ABCDEFGHIJKLMNOPQRSTUVWXY1": false,
		"appr_ABC":                        false,
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ":      false,
		"":                                false,
	} {
		if got := WellFormed(id); got != want {
			t.Errorf("WellFormed(%q) = %v, want %v", id, got, want)
		}
	}
}

// The same credential asking for the same action with the same input gets its pending request
// back; another input, another credential or another action is a request of its own.
func TestDuplicatePendingRequestsAreDeduplicated(t *testing.T) {
	s := New(Options{Now: newClock().now})
	first, _, _ := s.Create(request("alice", "t1", `{"a":1}`))
	again, created, err := s.Create(request("alice", "t1", `{"a":1}`))
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("again = %s, created %v, %v; want %s", again.ID, created, err, first.ID)
	}
	grant := request("alice", "", `{"a":1}`)
	grant.GrantID = "t1" // a grant whose id equals a token's is another credential
	other := request("alice", "t1", `{"a":1}`)
	other.Action = "scale"
	for name, r := range map[string]Request{
		"another input":      request("alice", "t1", `{"a":2}`),
		"another credential": request("alice", "t2", `{"a":1}`),
		"a grant":            grant,
		"another action":     other,
	} {
		if info, created, _ := s.Create(r); !created || info.ID == first.ID {
			t.Errorf("%s: deduplicated into %s", name, info.ID)
		}
	}
	// Once decided, the same call makes a new request.
	if _, err := s.Claim(first.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deny(first.ID, "alice", ""); err != nil {
		t.Fatal(err)
	}
	if next, created, _ := s.Create(request("alice", "t1", `{"a":1}`)); !created || next.ID == first.ID {
		t.Errorf("after the decision: %s, created %v", next.ID, created)
	}
}

// A user has at most 20 pending requests: the 21st is refused, while another user and a
// duplicate of a pending one are not; a decision makes room.
func TestTheTwentyFirstPendingRequestIsRefused(t *testing.T) {
	s := New(Options{Now: newClock().now})
	var ids []string
	for i := range model.ApprovalsPendingMax {
		info, created, err := s.Create(request("alice", "t1", fmt.Sprintf(`{"n":%d}`, i)))
		if err != nil || !created {
			t.Fatalf("request %d: %v, %v", i+1, created, err)
		}
		ids = append(ids, info.ID)
	}
	if _, _, err := s.Create(request("alice", "t2", `{"n":"21st"}`)); !errors.Is(err, ErrTooMany) {
		t.Fatalf("21st = %v, want ErrTooMany", err)
	}
	if _, created, err := s.Create(request("alice", "t1", `{"n":0}`)); err != nil || created {
		t.Errorf("a duplicate of a pending one = %v, %v; want it back", created, err)
	}
	if _, _, err := s.Create(request("bob", "t3", `{"n":0}`)); err != nil {
		t.Errorf("another user = %v", err)
	}
	if _, err := s.Claim(ids[0], "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deny(ids[0], "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(request("alice", "t2", `{"n":"21st"}`)); err != nil {
		t.Errorf("after a decision = %v", err)
	}
}

// Only the owner sees or decides a request; an unknown id is gone.
func TestOnlyTheOwnerSeesAndClaims(t *testing.T) {
	s := New(Options{Now: newClock().now})
	info, _, _ := s.Create(request("alice", "t1", "{}"))
	if _, err := s.Get(info.ID, "bob"); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob's Get = %v", err)
	}
	if _, err := s.Claim(info.ID, "bob"); !errors.Is(err, ErrForbidden) {
		t.Errorf("bob's Claim = %v", err)
	}
	if _, err := s.Get("appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "alice"); !errors.Is(err, ErrGone) {
		t.Errorf("unknown Get = %v", err)
	}
	if _, err := s.Claim("appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "alice"); !errors.Is(err, ErrGone) {
		t.Errorf("unknown Claim = %v", err)
	}
	if got, err := s.Get(info.ID, "alice"); err != nil || got.ID != info.ID {
		t.Errorf("alice's Get = %+v, %v", got, err)
	}
}

// Of many concurrent claims of one request exactly one wins; the others see it is no longer
// pending, until the winner releases it.
func TestClaimIsExactlyOnce(t *testing.T) {
	s := New(Options{Now: newClock().now})
	info, _, _ := s.Create(request("alice", "t1", "{}"))
	var won, lost atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			switch _, err := s.Claim(info.ID, "alice"); {
			case err == nil:
				won.Add(1)
			case errors.Is(err, ErrNotPending):
				lost.Add(1)
			default:
				t.Errorf("Claim = %v", err)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 || lost.Load() != 31 {
		t.Fatalf("won %d, lost %d", won.Load(), lost.Load())
	}
	if got := s.Pending("alice"); len(got) != 1 {
		t.Error("a claimed request left the pending list")
	}
	s.Release(info.ID)
	if _, err := s.Claim(info.ID, "alice"); err != nil {
		t.Errorf("after Release: %v", err)
	}
	out, err := s.Approve(info.ID, "alice", model.ApprovalOutcome{OK: true, Result: `{"ok":true}`})
	if err != nil || out.State != model.ApprovalStateApproved || out.DecidedBy != "alice" || out.DecidedAt == nil || out.Outcome == nil || !out.Outcome.OK {
		t.Fatalf("Approve = %+v, %v", out, err)
	}
	if _, err := s.Claim(info.ID, "alice"); !errors.Is(err, ErrNotPending) {
		t.Errorf("Claim of a decided request = %v", err)
	}
	if _, err := s.Approve(info.ID, "alice", model.ApprovalOutcome{}); !errors.Is(err, ErrNotPending) {
		t.Errorf("a second Approve = %v", err)
	}
	if got := s.Pending("alice"); len(got) != 0 {
		t.Errorf("Pending after the decision = %+v", got)
	}
}

// dash's own denial (the requester lost a permission) names the reason and no decider.
func TestDenyByDash(t *testing.T) {
	s := New(Options{Now: newClock().now})
	info, _, _ := s.Create(request("alice", "t1", "{}"))
	if _, err := s.Claim(info.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	out, err := s.Deny(info.ID, "", "the token was revoked")
	if err != nil || out.State != model.ApprovalStateDenied || out.Reason != "the token was revoked" || out.DecidedBy != "" || out.DecidedAt != nil {
		t.Errorf("Deny = %+v, %v", out, err)
	}
}

// An undecided request expires after 10 minutes: it leaves the pending list, its owner is
// notified, the expiry is reported once, and a decision is refused as gone. It stays readable for
// 10 more minutes, then is forgotten.
func TestExpiry(t *testing.T) {
	c, rec := newClock(), &recorder{}
	s := New(rec.options(c))
	info, _, _ := s.Create(request("alice", "t1", "{}"))
	rec.take()
	c.add(10*time.Minute - time.Second)
	s.Sweep()
	if changed, expired := rec.take(); len(changed)+len(expired) != 0 {
		t.Fatalf("before expiry: changed %v, expired %v", changed, expired)
	}
	c.add(time.Second)
	s.Sweep()
	s.Sweep()
	if changed, expired := rec.take(); !slices.Equal(changed, []string{"alice"}) || !slices.Equal(expired, []string{info.ID}) {
		t.Errorf("at expiry: changed %v, expired %v", changed, expired)
	}
	if got := s.Pending("alice"); len(got) != 0 {
		t.Errorf("Pending = %+v", got)
	}
	if _, err := s.Claim(info.ID, "alice"); !errors.Is(err, ErrGone) {
		t.Errorf("Claim of an expired request = %v", err)
	}
	if got, err := s.Get(info.ID, "alice"); err != nil || got.State != model.ApprovalStateExpired {
		t.Errorf("Get = %+v, %v", got, err)
	}
	c.add(10 * time.Minute)
	if _, err := s.Get(info.ID, "alice"); !errors.Is(err, ErrGone) {
		t.Errorf("after the retention: %v", err)
	}
}

// A claimed request is being decided: it does not expire under its decider.
func TestAClaimedRequestDoesNotExpire(t *testing.T) {
	c := newClock()
	s := New(Options{Now: c.now})
	info, _, _ := s.Create(request("alice", "t1", "{}"))
	if _, err := s.Claim(info.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	c.add(11 * time.Minute)
	s.Sweep()
	if out, err := s.Approve(info.ID, "alice", model.ApprovalOutcome{OK: true}); err != nil || out.State != model.ApprovalStateApproved {
		t.Errorf("Approve after the expiry time = %+v, %v", out, err)
	}
}

// Wait returns when the request is decided or expires, or when its context ends.
func TestWait(t *testing.T) {
	c := newClock()
	s := New(Options{Now: c.now})
	info, _, _ := s.Create(request("alice", "t1", "{}"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.Wait(ctx, info.ID) {
		t.Error("Wait on a pending request reported a decision")
	}

	done := make(chan bool)
	go func() { done <- s.Wait(context.Background(), info.ID) }()
	if _, err := s.Claim(info.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deny(info.ID, "alice", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Error("Wait after the decision = false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the decision")
	}

	if s.Wait(context.Background(), "appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Error("Wait on an unknown request = true")
	}
	// A request whose time is up wakes its waiter as expired.
	late, _, _ := s.Create(request("alice", "t1", `{"late":1}`))
	c.add(10 * time.Minute)
	if !s.Wait(context.Background(), late.ID) {
		t.Error("Wait on an expired request = false")
	}
}
