package mcp

import (
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
)

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
