package config

import (
	"strings"
	"testing"
)

// auth.tokens is checked at load (design M.5): a static mcp token cannot run actions, no static
// token holds approvals or push, actions are an administrator's, and the shape is exact. No
// message repeats a value.
func TestStaticTokensAreValidated(t *testing.T) {
	hash := "sha256:" + strings.Repeat("0f", 32)
	ok := StaticToken{Name: "ci", Role: "admin", Audience: "api", Scopes: []string{"read", "logs", "actions:kubernetes"}, Hash: hash}
	valid := DefaultAuth()
	valid.Tokens = []StaticToken{ok}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a valid token was refused: %v", err)
	}
	for name, tc := range map[string]struct {
		mod  func(*StaticToken)
		want string
	}{
		"mcp with actions":   {func(s *StaticToken) { s.Audience = "mcp" }, "which an mcp token cannot"},
		"approvals":          {func(s *StaticToken) { s.Scopes = []string{"approvals"} }, "only for the first-party app"},
		"push":               {func(s *StaticToken) { s.Scopes = []string{"read", "push"} }, "only for the first-party app"},
		"viewer with action": {func(s *StaticToken) { s.Role = "viewer" }, "needs role admin"},
		"unknown scope":      {func(s *StaticToken) { s.Scopes = []string{"write-everything"} }, "is not a scope"},
		"no scope":           {func(s *StaticToken) { s.Scopes = nil }, "at least one scope"},
		"bad role":           {func(s *StaticToken) { s.Role = "root" }, "role viewer or admin"},
		"bad audience":       {func(s *StaticToken) { s.Audience = "web" }, "audience api or mcp"},
		"bad name":           {func(s *StaticToken) { s.Name = "a b" }, "needs a name"},
		"plain hash":         {func(s *StaticToken) { s.Hash = "s3cret-plain-token" }, "sha256:<64"},
	} {
		t.Run(name, func(t *testing.T) {
			tok := ok
			tok.Scopes = append([]string(nil), ok.Scopes...)
			tc.mod(&tok)
			a := DefaultAuth()
			a.Tokens = []StaticToken{tok}
			err := a.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "tokens") {
				t.Fatalf("err = %v, want one at tokens containing %q", err, tc.want)
			}
			for _, v := range []string{"s3cret-plain-token", "write-everything", "root"} {
				if strings.Contains(err.Error(), v) {
					t.Errorf("the message repeats a value: %v", err)
				}
			}
		})
	}
	dup := DefaultAuth()
	dup.Tokens = []StaticToken{ok, ok}
	if err := dup.Validate(); err == nil || !strings.Contains(err.Error(), "repeats the name") || !strings.Contains(err.Error(), "repeats the hash") {
		t.Errorf("duplicates: %v", err)
	}
}
