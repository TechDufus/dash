package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/svc"
)

// fluxResources maps the reconcilable Flux kinds to their resources.
var fluxResources = map[model.FluxKind]schema.GroupVersionResource{
	model.FluxKindKustomization:  {Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"},
	model.FluxKindHelmRelease:    {Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
	model.FluxKindGitRepository:  {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
	model.FluxKindOCIRepository:  {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"},
	model.FluxKindHelmRepository: {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"},
}

// writeKubeErr answers a Kubernetes API error. 403/404/409/422 keep their code and message
// verbatim so an admission denial (`homelab guardrail: ...`) reaches the UI unchanged; they
// come from the cluster's own policy, not from an upstream URL. Anything else is a 502 with
// the error category only.
func (s *Server) writeKubeErr(w http.ResponseWriter, err error) {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		switch code := int(st.Status().Code); code {
		case http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
			writeErr(w, code, kubeErrCode(code), s.d.Secrets.Scrub(err.Error()))
			return
		}
	}
	s.badGateway(w, "kubernetes", err)
}

// kubeErrCode is the error code of a Kubernetes status writeKubeErr passes through.
func kubeErrCode(status int) string {
	switch status {
	case http.StatusForbidden:
		return model.ErrCodeForbidden
	case http.StatusNotFound:
		return model.ErrCodeNotFound
	case http.StatusConflict:
		return model.ErrCodeConflict
	}
	return model.ErrCodeInvalidInput // 422
}

func accepted(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	w, done := s.d.Metrics.TrackAction(w, "flux", "reconcile")
	defer done()
	var req model.ReconcileRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	gvr, ok := fluxResources[req.Kind]
	if !ok {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "unsupported kind")
		return
	}
	if !queries.ValidDNS(req.Namespace) || !queries.ValidDNS(req.Name) {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid namespace or name")
		return
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{"reconcile.fluxcd.io/requestedAt": s.now().UTC().Format(time.RFC3339Nano)},
	}})
	pending, ok := s.begin(w, r, "reconcile", audit.Target{Kind: string(req.Kind), Namespace: req.Namespace, Name: req.Name})
	if !ok {
		return
	}
	c, ok := s.cluster()
	if !ok {
		err := errors.New("the kubernetes plugin is not running")
		pending.Outcome(err)
		writeErr(w, http.StatusServiceUnavailable, model.ErrCodeUpstreamUnavailable, "kubernetes is not available")
		return
	}
	err := c.Patch(r.Context(), gvr, req.Namespace, req.Name, patch)
	pending.Outcome(err)
	if err != nil {
		s.writeKubeErr(w, err)
		return
	}
	accepted(w)
}

// cluster is the kubernetes plugin's svc.Cluster while the plugin runs. It is looked up per
// request: the plugin restarts on a reload and its service changes with it.
func (s *Server) cluster() (svc.Cluster, bool) {
	if s.d.Cluster == nil {
		return nil, false
	}
	c, ok := s.d.Cluster()
	return c, ok && c != nil
}
