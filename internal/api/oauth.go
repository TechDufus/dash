package api

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/oauth"
)

// oauthRoutes are the authorization server's endpoints (design M.6, contract G7-R3), outside
// /api: the metadata documents and /oauth/authorize. They are Infra rows, documented in
// docs/api/README.md rather than the OpenAPI document. Without the server (auth.oauth.enabled
// off, auth.mode=none, no writable data dir) each answers a JSON 404, as the catch-alls do.
func (s *Server) oauthRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/.well-known/oauth-authorization-server", Infra: true, Handler: s.oauthMetadata},
		{Method: "GET", Path: "/.well-known/oauth-protected-resource/{audience}", Infra: true, Handler: s.oauthResourceMetadata},
		{Method: "GET", Path: "/oauth/authorize", Infra: true, Handler: s.oauthAuthorize},
		{Path: "/oauth/", Infra: true, Handler: notFound},
	}
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
}

// oauthCORSPath reports whether a listed origin may read the answer at p whoever asks (design
// L.7): the metadata documents and the token and revoke endpoints, which a browser-based client
// calls without a session. Never /oauth/authorize, which is a navigation.
func oauthCORSPath(p string) bool {
	return strings.HasPrefix(p, "/.well-known/oauth-") || p == "/oauth/token" || p == "/oauth/revoke"
}

// actionPlugins are the plugin instances with mounted actions: their actions:<id> scopes are the
// ones the metadata lists and a client may ask for.
func (s *Server) actionPlugins() []string {
	if s.d.Plugins == nil {
		return nil
	}
	var ids []string
	for _, pi := range s.d.Plugins.Infos() {
		if len(pi.Actions) > 0 {
			ids = append(ids, pi.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func (s *Server) oauthMetadata(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil {
		notFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.d.OAuth.Metadata(s.actionPlugins()))
}

func (s *Server) oauthResourceMetadata(w http.ResponseWriter, r *http.Request) {
	aud := r.PathValue("audience")
	if s.d.OAuth == nil || (aud != model.TokenAudienceAPI && aud != model.TokenAudienceMCP) {
		notFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.d.OAuth.ResourceMetadata(aud, s.actionPlugins()))
}

// oauthAuthorize answers /oauth/authorize: a valid request goes on to the SPA's consent page,
// which signs the user in first if needed; an invalid one goes back to the client with the
// error, or, when the client or its redirect URI cannot be verified, is answered here (400).
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil {
		notFound(w, r)
		return
	}
	res, err := s.d.OAuth.Authorize(r.Context(), r.URL.Query(), s.actionPlugins())
	var oe *oauth.Error
	switch {
	case errors.As(err, &oe):
		writeJSON(w, http.StatusBadRequest, oe)
	case errors.Is(err, oauth.ErrBusy):
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusServiceUnavailable, &oauth.Error{Code: "temporarily_unavailable", Description: err.Error()})
	case err != nil:
		slog.Error("oauth authorize failed", "error", err, "requestId", requestIDFrom(r.Context()))
		writeJSON(w, http.StatusInternalServerError, &oauth.Error{Code: "server_error"})
	case res.ErrorRedirect != "":
		http.Redirect(w, r, res.ErrorRedirect, http.StatusFound)
	default:
		http.Redirect(w, r, res.ConsentURL, http.StatusFound)
	}
}
