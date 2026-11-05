package config

import (
	"regexp"
	"strings"

	"github.com/TechDufus/dash/internal/auth"
)

// StaticToken is one entry of auth.tokens (design M.5): a bearer token for GitOps installs,
// configured by its hash. The principal's user is token:<name>.
type StaticToken struct {
	Name     string   `json:"name" doc:"The token's name: letters, digits, '.', '_' and '-', at most 64. The principal is token:<name>."`
	Role     string   `json:"role" doc:"viewer or admin."`
	Audience string   `json:"audience" doc:"api (/api/v1) or mcp (/mcp)."`
	Scopes   []string `json:"scopes" doc:"read, logs and actions:<plugin> (admin only, never with audience mcp)."`
	Hash     string   `json:"hash" doc:"sha256:<64 hex digits> of the token, as dash token generate prints it."`
}

var (
	staticTokenName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	tokenHash       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// actionScope is actions:<plugin>, the plugin's instance ID (plugin.ID's rule).
	actionScope = regexp.MustCompile(`^actions:[a-z][a-z0-9]{1,31}$`)
)

// TokenScopeProblem is why scope cannot be on a personal access token or a static token, or ""
// when it can (design M.3, M.5): read, logs and actions:<plugin>, never approvals or push. The
// role and audience rules are the caller's. The message never repeats the scope.
func TokenScopeProblem(scope string) string {
	switch {
	case scope == "read" || scope == "logs" || actionScope.MatchString(scope):
		return ""
	case scope == "approvals" || scope == "push":
		return "is only for the first-party app: a personal access token or a static token never holds it"
	}
	return "is not a scope: read, logs or actions:<plugin>"
}

// validateTokens reports unusable auth.tokens entries by position, never by value.
func validateTokens(tokens []StaticToken, add func(path, format string, args ...any)) {
	names, hashes := map[string]bool{}, map[string]bool{}
	for i, t := range tokens {
		n := i + 1
		if !staticTokenName.MatchString(t.Name) {
			add("tokens", "entry %d needs a name of letters, digits, '.', '_' and '-' (at most 64)", n)
		} else if names[t.Name] {
			add("tokens", "entry %d repeats the name of an earlier entry", n)
		}
		names[t.Name] = true
		role := auth.Role(t.Role)
		if role != auth.RoleViewer && role != auth.RoleAdmin {
			add("tokens", "entry %d needs role viewer or admin", n)
		}
		if t.Audience != "api" && t.Audience != "mcp" {
			add("tokens", "entry %d needs audience api or mcp", n)
		}
		if len(t.Scopes) == 0 {
			add("tokens", "entry %d needs at least one scope", n)
		}
		for j, s := range t.Scopes {
			if problem := TokenScopeProblem(s); problem != "" {
				add("tokens", "entry %d scope %d %s", n, j+1, problem)
				continue
			}
			if !strings.HasPrefix(s, "actions:") {
				continue
			}
			if role != auth.RoleAdmin {
				add("tokens", "entry %d scope %d runs actions, which needs role admin", n, j+1)
			}
			if t.Audience == "mcp" {
				add("tokens", "entry %d scope %d runs actions, which an mcp token cannot: its actions wait for the owning user's approval, and a static token has no owner; use a personal access token", n, j+1)
			}
		}
		if !tokenHash.MatchString(t.Hash) {
			add("tokens", "entry %d needs a hash of the form sha256:<64 lower-case hex digits> (dash token generate prints one)", n)
		} else if hashes[t.Hash] {
			add("tokens", "entry %d repeats the hash of an earlier entry", n)
		}
		hashes[t.Hash] = true
	}
}
