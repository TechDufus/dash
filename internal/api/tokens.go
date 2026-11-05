package api

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
)

// The audit actions of the token routes.
const (
	auditTokenCreate = "token.create"
	auditTokenRevoke = "token.revoke"
)

// maxActionNamespaces caps a token's actionNamespaces list.
const maxActionNamespaces = 32

// The creation limit: a user may create this many tokens in any window (each creation rewrites
// credentials.yaml and writes two audit records).
const (
	tokenCreateLimit  = 10
	tokenCreateWindow = time.Minute
)

// actionNamespaceRE is an exact Kubernetes namespace name, or a prefix of one followed by *.
var actionNamespaceRE = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?|[a-z0-9][-a-z0-9]{0,62}\*|\*)$`)

// tokenRoutes are the personal access token routes (design M.5, contract G7-R2): for people
// only (Interactive: a token gets 403), CSRF-checked like every write, and audited fail-closed.
func (s *Server) tokenRoutes() []Route {
	row := func(r Route) Route {
		r.Role, r.Interactive, r.Tag = auth.RoleViewer, true, "Tokens"
		return r
	}
	return []Route{
		row(Route{
			ID: "listTokens", Method: "GET", Path: "/api/v1/tokens",
			Summary:     "List personal access tokens",
			Description: "The caller's own tokens; an administrator may list one user's (?user=<name>) or everyone's (?user=*). Expired tokens stay listed for 30 days.",
			Query:       []plugin.Param{{Name: "user", Doc: "Administrators only: a user name, or * for every user. Absent or empty: the caller's own."}},
			Response:    model.TokenList{}, Handler: s.listTokens,
		}),
		row(Route{
			ID: "createToken", Method: "POST", Path: "/api/v1/tokens",
			Summary: "Create a personal access token",
			Description: "The answer holds the token, shown this once. 400 invalid_input with field errors; 403 for actions:* by a viewer " +
				"and for approvals or push; 409 token_limit past 50 unexpired tokens, 409 tokens_unavailable when nothing would keep it; " +
				"429 past 10 creations a minute; 503 audit_unavailable when the audit log cannot be written.",
			Request: model.TokenCreateRequest{}, Response: model.TokenCreated{}, Status: http.StatusCreated, Handler: s.createToken,
		}),
		row(Route{
			ID: "revokeToken", Method: "DELETE", Path: "/api/v1/tokens/{id}",
			Summary: "Revoke a personal access token",
			Description: "Applies to the next request: one's own token, or (administrators) anyone's. 404 for a token that is not the " +
				"caller's; 500 when credentials.yaml cannot be written (the token stays revoked until dash restarts).",
			PathParams: []plugin.Param{{Name: "id", Doc: "The token's ID (the 10 characters after dash_pat_).", Required: true}},
			Status:     http.StatusNoContent, Handler: s.revokeToken,
		}),
	}
}

// tokensReady answers for the token routes what this process cannot do: 404 where no token is
// resolved (auth.mode=none), 409 tokens_unavailable where nothing keeps a token (contract D1).
func (s *Server) tokensReady(w http.ResponseWriter) bool {
	if s.d.Tokens == nil || s.d.Credentials == nil {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		return false
	}
	if !s.tokensAvailable() {
		writeErr(w, http.StatusConflict, model.ErrCodeTokensUnavailable, "tokens need a writable data dir (DASH_DATA_DIR): without one they would be lost at restart")
		return false
	}
	return true
}

// tokenInfo is the wire form of a stored token.
func tokenInfo(p credentials.PAT) model.TokenInfo {
	return model.TokenInfo{
		ID: p.ID, Name: p.Name, User: p.User, Audience: p.Audience,
		Scopes: append([]string{}, p.Scopes...), ActionNamespaces: append([]string{}, p.ActionNamespaces...),
		CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt, LastUsedAt: p.LastUsedAt,
	}
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	if !s.tokensReady(w) {
		return
	}
	p := principalFrom(r.Context())
	user := p.User
	if u := strings.Join(r.URL.Query()["user"], ","); u != "" { // an empty ?user= is the caller's own
		if u != p.User && !p.Role.AtLeast(auth.RoleAdmin) {
			s.deny(r, obs.AuthForbidden)
			writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "only an administrator lists another user's tokens")
			return
		}
		user = u
		if u == "*" {
			user = "" // every user's
		}
	}
	list := model.TokenList{Tokens: []model.TokenInfo{}}
	for _, t := range s.d.Credentials.PATs(user) {
		list.Tokens = append(list.Tokens, tokenInfo(t))
	}
	writeJSON(w, http.StatusOK, list)
}

// tokenRequestProblems checks a create request. forbidden names a scope the caller may never put
// on a token (403: approvals and push for everyone, actions:* for a viewer); fields are the 400
// field errors. No message repeats a value.
func tokenRequestProblems(req model.TokenCreateRequest, role auth.Role, hasActions func(plugin string) bool) (forbidden string, fields []model.SettingsFieldError) {
	add := func(path, msg string) { fields = append(fields, model.SettingsFieldError{Path: path, Message: msg}) }
	if !validTokenName(req.Name) {
		add("name", "must be 1 to 64 characters with at least one visible one, without control or format characters or surrounding spaces")
	}
	if req.Audience != model.TokenAudienceAPI && req.Audience != model.TokenAudienceMCP {
		add("audience", "must be api or mcp")
	}
	actions := false
	if len(req.Scopes) == 0 {
		add("scopes", "must hold at least one scope")
	}
	seen := map[string]bool{}
	for _, sc := range req.Scopes {
		switch {
		case sc == "approvals" || sc == "push":
			forbidden = "approvals and push are only for the first-party app: a personal access token never holds them"
		case strings.HasPrefix(sc, model.TokenScopeActionsPrefix) && config.TokenScopeProblem(sc) == "":
			actions = true
			if !role.AtLeast(auth.RoleAdmin) && forbidden == "" {
				forbidden = "only an administrator may put actions:* on a token"
			}
			if !hasActions(strings.TrimPrefix(sc, model.TokenScopeActionsPrefix)) {
				add("scopes", "an actions:<plugin> scope must name a plugin whose actions are enabled")
			}
		}
		if config.TokenScopeProblem(sc) != "" && sc != "approvals" && sc != "push" {
			add("scopes", "each scope must be read, logs or actions:<plugin>")
		}
		if seen[sc] {
			add("scopes", "lists a scope twice")
		}
		seen[sc] = true
	}
	if len(req.ActionNamespaces) > 0 && !actions {
		add("actionNamespaces", "only applies to a token with an actions:<plugin> scope")
	}
	if len(req.ActionNamespaces) > maxActionNamespaces {
		add("actionNamespaces", "holds at most 32 entries")
	}
	for _, ns := range req.ActionNamespaces {
		if !actionNamespaceRE.MatchString(ns) {
			add("actionNamespaces", "each entry must be a namespace name or a prefix followed by *")
			break
		}
	}
	if d := req.ExpiresInDays; d != nil && (*d < 1 || *d > model.TokenExpiryMaxDays) {
		add("expiresInDays", "must be 1 to 365, or absent for 30")
	}
	return forbidden, fields
}

// validTokenName is a name of 1 to 64 characters, at least one of them visible, without control
// or format characters (a right-to-left override, a zero-width space) and without surrounding
// space, so the token list shows what was typed.
func validTokenName(name string) bool {
	n := utf8.RuneCountInString(name)
	if n == 0 || n > model.TokenNameMaxLen || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	visible := false
	for _, c := range name {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return false
		}
		if unicode.IsGraphic(c) && !unicode.IsSpace(c) {
			visible = true
		}
	}
	return visible
}

// pluginHasActions reports whether plugin id runs with mounted actions: the only plugins an
// actions:<id> scope may name.
func (s *Server) pluginHasActions(id string) bool {
	if s.d.Plugins == nil {
		return false
	}
	for _, pi := range s.d.Plugins.Infos() {
		if pi.ID == id && len(pi.Actions) > 0 {
			return true
		}
	}
	return false
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if !s.tokensReady(w) {
		return
	}
	p := principalFrom(r.Context())
	var body model.TokenCreateRequest
	if !s.decodeStrict(w, r, &body) {
		return
	}
	forbidden, fields := tokenRequestProblems(body, p.Role, s.pluginHasActions)
	if forbidden != "" {
		s.deny(r, obs.AuthForbidden)
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, forbidden)
		return
	}
	if len(fields) > 0 {
		writeErrDetails(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid token request", &model.SettingsErrorDetails{Fields: fields})
		return
	}
	if s.d.Credentials.ActivePATs(p.User, s.now()) >= credentials.MaxActivePATs {
		writeTokenLimit(w)
		return
	}
	// Only a request that would create a token counts against the creation limit.
	if ok, retry := s.createLimit.allow(p.User, s.now()); !ok {
		s.deny(r, obs.AuthRateLimit)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
		writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "too many tokens created; try again later")
		return
	}
	token, id, hash, err := credentials.NewPAT()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "internal error")
		return
	}
	days := model.TokenExpiryDefaultDays
	if body.ExpiresInDays != nil {
		days = *body.ExpiresInDays
	}
	now := s.now().UTC().Truncate(time.Second)
	pat := credentials.PAT{
		ID: id, Name: body.Name, User: p.User, Hash: hash, Audience: body.Audience,
		Scopes: slices.Clone(body.Scopes), ActionNamespaces: slices.Clone(body.ActionNamespaces),
		Groups: slices.Clone(p.Groups), RoleClaim: slices.Clone(p.RoleClaim),
		CreatedAt: now, ExpiresAt: now.AddDate(0, 0, days),
	}
	// Intent, then the change, then the outcome (contract D6): an intent that cannot be written
	// is 503 and nothing is created.
	rec := s.auditRecord(r, auditTokenCreate, audit.Target{Kind: "token", Namespace: p.User, Name: id})
	pending, ok := s.beginRecord(w, rec)
	if !ok {
		return
	}
	err = s.d.Credentials.AddPAT(pat)
	pending.Outcome(err)
	switch {
	case errors.Is(err, credentials.ErrTooManyTokens): // a concurrent create took the last place
		writeTokenLimit(w)
		return
	case err != nil:
		slog.Error("token create: the credential store refused the write", "error", err, "requestId", requestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the token could not be stored")
		return
	}
	writeJSON(w, http.StatusCreated, model.TokenCreated{Token: token, Info: tokenInfo(pat)})
}

func writeTokenLimit(w http.ResponseWriter) {
	writeErr(w, http.StatusConflict, model.ErrCodeTokenLimit, "you hold the most tokens allowed (50 that have not expired); revoke one first")
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if !s.tokensReady(w) {
		return
	}
	p := principalFrom(r.Context())
	id := r.PathValue("id")
	pat, found := s.d.Credentials.PATByID(id)
	// Another user's token is as unknown as a missing one to a viewer: its ID tells nothing.
	if !found || (pat.User != p.User && !p.Role.AtLeast(auth.RoleAdmin)) {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such token")
		return
	}
	rec := s.auditRecord(r, auditTokenRevoke, audit.Target{Kind: "token", Namespace: pat.User, Name: pat.ID})
	pending, ok := s.beginRecord(w, rec)
	if !ok {
		return
	}
	_, _, err := s.d.Credentials.RevokePAT(id)
	pending.Outcome(err)
	if err != nil {
		// The revocation holds in this process (the store keeps it in memory); the file did not
		// get it, so a restart would bring the token back.
		slog.Error("token revoke: credentials.yaml could not be written; the token is revoked until dash restarts", "error", err, "requestId", requestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the token is revoked until dash restarts: credentials.yaml could not be written")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
