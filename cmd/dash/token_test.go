package main

import (
	"bytes"
	"regexp"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/tokens"
)

// dash token generate prints a dash_st_ token and the sha256: hash auth.tokens takes for it: the
// pair resolves as that static token, and the log scrubber redacts the token whole.
func TestTokenGeneratePrintsATokenAndItsHash(t *testing.T) {
	var out bytes.Buffer
	if err := command([]string{"token", "generate"}, &out); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^token: (dash_st_[A-Za-z0-9_-]{43})\nhash:  (sha256:[0-9a-f]{64})$`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("output:\n%s", out.String())
	}
	if credentials.HashToken(m[1]) != m[2] {
		t.Errorf("token %q and hash %q do not belong together", m[1], m[2])
	}
	r := tokens.New(nil, nil, []config.StaticToken{{Name: "ci", Role: "viewer", Audience: "api", Scopes: []string{"read"}, Hash: m[2]}}, nil)
	if p, err := r.Resolve(m[1], "api"); err != nil || p.User != "token:ci" {
		t.Errorf("the generated token resolves as %+v, %v", p, err)
	}
	if got := (*secret.Registry)(nil).Scrub("log " + m[1] + " end"); got != "log [redacted] end" {
		t.Errorf("the scrubber left %q", got)
	}
	if err := command([]string{"token"}, &out); err == nil || exitCode(err) != 2 {
		t.Errorf("no subcommand: %v", err)
	}
}
