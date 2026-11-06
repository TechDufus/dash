package oauth

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
)

// Consent errors. The API answers ErrConsentNotFound with 404 whether the request never
// existed, expired, or belongs to another person: its ID tells nothing.
var (
	ErrConsentNotFound = errors.New("oauth: no such authorization request")
	// ErrNothingToGrant: every scope the client asked for is beyond the person's role.
	ErrNothingToGrant = errors.New("oauth: you cannot grant any scope this application asks for")
	// ErrScopes: the approved scopes are empty or not a subset of the offered ones.
	ErrScopes = errors.New("oauth: approve a non-empty subset of the offered scopes")
)

// offered are the requested scopes p may grant: actions:<plugin> only an administrator.
func offered(req []string, p auth.Principal) []string {
	var out []string
	for _, sc := range req {
		if strings.HasPrefix(sc, model.TokenScopeActionsPrefix) && !p.Role.AtLeast(auth.RoleAdmin) {
			continue
		}
		out = append(out, sc)
	}
	return out
}

// claimLocked returns the pending request id for p: only the person who sent it sees it, and it
// is unknown to everyone else. s.mu is held; the caller defers revokeExpired.
func (s *Server) claimLocked(id string, p auth.Principal, now time.Time) (*pending, error) {
	s.sweepLocked(now)
	pr, ok := s.pending[id]
	if !ok || pr.busy || p.User == "" || pr.user != p.User {
		return nil, ErrConsentNotFound
	}
	return pr, nil
}

// Consent is the authorization request id as the consent page shows it to p. Only read is
// ticked in advance. ErrNothingToGrant when p may grant none of the scopes asked for.
func (s *Server) Consent(id string, p auth.Principal) (model.OAuthConsent, error) {
	defer s.revokeExpired()
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, err := s.claimLocked(id, p, s.now())
	if err != nil {
		return model.OAuthConsent{}, err
	}
	scopes := offered(pr.scopes, p)
	if len(scopes) == 0 {
		return model.OAuthConsent{}, ErrNothingToGrant
	}
	defaults := []string{}
	if slices.Contains(scopes, model.TokenScopeRead) {
		defaults = append(defaults, model.TokenScopeRead)
	}
	return model.OAuthConsent{
		ID: pr.id, Client: pr.client.info(), RedirectURI: pr.redirectURI, Audience: pr.audience,
		Scopes: scopes, DefaultScopes: defaults, ExpiresAt: pr.expires,
	}, nil
}

// Approve grants the client of request id the scopes p ticked: it creates the grant and an
// authorization code, and returns the redirect that carries code, state and iss (RFC 9207).
// before runs with the grant ahead of the write (the API's audit intent); its error aborts and
// changes nothing. credentials.ErrTooManyGrants past the limit; the request stays pending then.
func (s *Server) Approve(id string, p auth.Principal, scopes []string, before func(credentials.Grant) error) (string, error) {
	now := s.now()
	defer s.revokeExpired()
	s.mu.Lock()
	pr, err := s.claimLocked(id, p, now)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	allowed := offered(pr.scopes, p)
	if len(scopes) == 0 || slices.ContainsFunc(scopes, func(sc string) bool { return !slices.Contains(allowed, sc) }) {
		s.mu.Unlock()
		return "", ErrScopes
	}
	if len(s.codes) >= maxCodes {
		s.mu.Unlock()
		return "", ErrBusy
	}
	// One approval at a time: a second one finds no request until this one failed.
	pr.busy = true
	req := *pr
	s.mu.Unlock()
	done := false
	defer func() {
		if !done {
			s.mu.Lock()
			pr.busy = false
			s.mu.Unlock()
		}
	}()

	gid, err := credentials.NewGrantID()
	if err != nil {
		return "", err
	}
	refresh, hash, err := credentials.NewRefreshToken(gid)
	if err != nil {
		return "", err
	}
	codeValue, err := randomID()
	if err != nil {
		return "", err
	}
	at := now.UTC().Truncate(time.Second)
	granted := slices.Clone(scopes)
	slices.SortStableFunc(granted, func(a, b string) int { return slices.Index(req.scopes, a) - slices.Index(req.scopes, b) })
	g := credentials.Grant{
		ID: gid, User: p.User, ClientID: req.client.ID, ClientName: req.client.Name, ClientKind: req.client.Kind,
		Audience: req.audience, Scopes: granted, Groups: slices.Clone(p.Groups), RoleClaim: slices.Clone(p.RoleClaim),
		RefreshHash: hash, CreatedAt: at, ExpiresAt: at.Add(credentials.GrantLifetime), RefreshedAt: at,
	}
	if before != nil {
		if err := before(g); err != nil {
			return "", err
		}
	}
	if err := s.store.AddGrant(g); err != nil {
		return "", err
	}

	done = true
	s.mu.Lock()
	defer s.mu.Unlock()
	// The request is used up; a second approval of it finds nothing.
	delete(s.pending, id)
	s.codes[codeValue] = &code{
		grantID: gid, refresh: refresh, clientID: req.client.ID, redirectURI: req.redirectURI,
		challenge: req.challenge, audience: req.audience, scopes: granted, expires: now.Add(CodeTTL),
	}
	return withParams(req.redirectURI, url.Values{"code": {codeValue}, "state": {req.state}, "iss": {s.base}}), nil
}

// Deny refuses request id for p: the client gets error=access_denied with state and iss.
func (s *Server) Deny(id string, p auth.Principal) (string, error) {
	defer s.revokeExpired()
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, err := s.claimLocked(id, p, s.now())
	if err != nil {
		return "", err
	}
	delete(s.pending, id)
	return withParams(pr.redirectURI, url.Values{
		"error": {"access_denied"}, "error_description": {"the user denied the request"}, "state": {pr.state}, "iss": {s.base},
	}), nil
}
