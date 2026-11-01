package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
)

const fieldManager = "dash"

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

func (s *Server) restart(w http.ResponseWriter, r *http.Request) {
	var req model.RestartRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	switch req.Kind {
	case model.WorkloadKindDeployment, model.WorkloadKindStatefulSet, model.WorkloadKindDaemonSet:
	default:
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "unsupported kind")
		return
	}
	if !queries.ValidDNS(req.Namespace) || !queries.ValidDNS(req.Name) {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid namespace or name")
		return
	}
	kind := string(req.Kind)
	pending, ok := s.begin(w, r, "restart", audit.Target{Kind: kind, Namespace: req.Namespace, Name: req.Name})
	if !ok {
		return
	}
	ctx := r.Context()

	ns, err := s.d.Kube.CoreV1().Namespaces().Get(ctx, req.Namespace, metav1.GetOptions{})
	if err != nil {
		pending.Outcome(err)
		s.writeKubeErr(w, err)
		return
	}
	// An unconfigured label never matches: without a protected label no namespace asks for the name.
	if label := s.d.Cfg.Conventions.ProtectedLabel; label != "" && ns.Labels[label] == "true" && req.Confirm != req.Name {
		err := errors.New("type the workload name to confirm")
		pending.Outcome(err)
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, err.Error())
		return
	}

	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{"kubectl.kubernetes.io/restartedAt": s.now().UTC().Format(time.RFC3339)},
	}}}})
	opts := metav1.PatchOptions{FieldManager: fieldManager}
	switch req.Kind {
	case model.WorkloadKindDeployment:
		_, err = s.d.Kube.AppsV1().Deployments(req.Namespace).Patch(ctx, req.Name, types.StrategicMergePatchType, patch, opts)
	case model.WorkloadKindStatefulSet:
		_, err = s.d.Kube.AppsV1().StatefulSets(req.Namespace).Patch(ctx, req.Name, types.StrategicMergePatchType, patch, opts)
	default:
		_, err = s.d.Kube.AppsV1().DaemonSets(req.Namespace).Patch(ctx, req.Name, types.StrategicMergePatchType, patch, opts)
	}
	pending.Outcome(err)
	if err != nil {
		s.writeKubeErr(w, err)
		return
	}
	accepted(w)
}

func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
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
	_, err := s.d.Dyn.Resource(gvr).Namespace(req.Namespace).Patch(r.Context(), req.Name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: fieldManager})
	pending.Outcome(err)
	if err != nil {
		s.writeKubeErr(w, err)
		return
	}
	accepted(w)
}
