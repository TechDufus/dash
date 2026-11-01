package api

import (
	"errors"
	"log/slog"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/upstream"
)

// errCategory reduces an error from an upstream or from the Kubernetes API to the one word a
// viewer may see (design C.6, K.3).
func errCategory(err error) string {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		return upstream.HTTPCategory(int(st.Status().Code))
	}
	return upstream.Category(err)
}

// badGateway answers 502 with what failed and its category. The detail of an upstream error
// names URLs and hosts, so it only goes to the log, scrubbed of every known secret.
func (s *Server) badGateway(w http.ResponseWriter, what string, err error) {
	cat := errCategory(err)
	slog.Warn("upstream request failed", "what", what, "category", cat, "requestId", w.Header().Get(requestIDHeader),
		"error", s.d.Secrets.Scrub(err.Error()))
	writeErr(w, http.StatusBadGateway, model.ErrCodeUpstreamUnavailable, what+": "+cat)
}
