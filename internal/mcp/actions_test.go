package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
)

// fillingBackend is an ActionBackend whose request stays pending, and whose wait fills every call
// slot of the token: other calls took them while the retry waited.
type fillingBackend struct {
	s    *Server
	key  string
	held []func()
}

func (f *fillingBackend) ActionTools() []ActionTool                 { return nil }
func (f *fillingBackend) ActionVisible(auth.Principal, string) bool { return false }
func (f *fillingBackend) CallAction(*http.Request, auth.Principal, string, string, json.RawMessage) (ActionReply, error) {
	return ActionReply{}, nil
}
func (f *fillingBackend) Approval(*http.Request, auth.Principal, string) (model.ApprovalInfo, error) {
	return model.ApprovalInfo{ID: "appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ", State: model.ApprovalStatePending, ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (f *fillingBackend) ApproveURL(id string) string {
	return "https://dash.example.com/approve/" + id
}
func (f *fillingBackend) WaitApproval(context.Context, string) bool {
	for range concurrentPerToken {
		release, refused := f.s.slots.acquire(f.key, "alice", false)
		if refused != "" {
			break
		}
		f.held = append(f.held, release)
	}
	return false
}

// A retry whose call slot was taken while it waited answers rate_limited and holds no slot: the
// token's slots are the other calls', and giving up the request frees nothing it does not hold.
func TestRetryThatCannotRetakeItsSlot(t *testing.T) {
	s := &Server{o: Options{Now: time.Now, Scrub: func(s string) string { return s }}, slots: newSlots(), stateKey: newStateKey()}
	p := auth.Principal{Kind: auth.KindToken, User: "alice", TokenID: "t1", Audience: "mcp"}
	key := tokenKey(p)
	release, refused := s.slots.acquire(key, p.User, false)
	if refused != "" {
		t.Fatal(refused)
	}
	slot := &heldSlot{slots: &s.slots, key: key, user: p.User, release: release}
	ab := &fillingBackend{s: s, key: key}
	res := s.awaitApproval(context.Background(), ab, nil, caller{p: p, slot: slot}, "aa_restart", "appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, model.ErrCodeRateLimited) {
		t.Fatalf("result = %+v", res.Content)
	}
	held := func() int { s.slots.mu.Lock(); defer s.slots.mu.Unlock(); return s.slots.calls[key] }
	if n := held(); n != concurrentPerToken {
		t.Errorf("%d slots held, want the %d the other calls took", n, concurrentPerToken)
	}
	slot.done()
	if n := held(); n != concurrentPerToken {
		t.Errorf("after the request ended: %d slots held, want %d", n, concurrentPerToken)
	}
	for _, r := range ab.held {
		r()
	}
	if n := held(); n != 0 {
		t.Errorf("after every call ended: %d", n)
	}
}

// A requestState opens only in the process that sealed it, for the same token and tool, before
// the request expires, and unaltered (contract D53).
func TestRequestState(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := &Server{o: Options{Now: func() time.Time { return now }}, stateKey: newStateKey()}
	alice := auth.Principal{Kind: auth.KindToken, User: "alice", TokenID: "t1"}
	st := s.sealState("appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ", alice, "aa_restart", now.Add(10*time.Minute))
	if id, ok := s.openState(st, alice, "aa_restart"); !ok || id != "appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		t.Fatalf("open = %q, %v", id, ok)
	}
	other := &Server{o: s.o, stateKey: newStateKey()}
	for name, open := range map[string]func() bool{
		"another token": func() bool {
			_, ok := s.openState(st, auth.Principal{Kind: auth.KindToken, User: "alice", TokenID: "t2"}, "aa_restart")
			return ok
		},
		"another tool":    func() bool { _, ok := s.openState(st, alice, "aa_scale"); return ok },
		"another process": func() bool { _, ok := other.openState(st, alice, "aa_restart"); return ok },
		"altered":         func() bool { _, ok := s.openState("x"+st, alice, "aa_restart"); return ok },
		"no signature":    func() bool { _, ok := s.openState(st[:len(st)-2], alice, "aa_restart"); return ok },
		"garbage":         func() bool { _, ok := s.openState("not-a-state", alice, "aa_restart"); return ok },
	} {
		if open() {
			t.Errorf("%s: opened", name)
		}
	}
	now = now.Add(10 * time.Minute)
	if _, ok := s.openState(st, alice, "aa_restart"); ok {
		t.Error("an expired state opened")
	}
}
