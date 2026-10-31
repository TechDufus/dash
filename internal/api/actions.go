package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

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

// audit records one user action.
func audit(r *http.Request, action, kind, ns, name string, err error) {
	result, msg := "ok", ""
	if err != nil {
		result, msg = "error", err.Error()
	}
	slog.Info("audit", "user", userFrom(r.Context()), "action", action, "kind", kind,
		"namespace", ns, "name", name, "result", result, "error", msg)
}

// writeKubeErr answers a Kubernetes API error. 403/404/409/422 keep their code and message
// verbatim so an admission denial (`homelab guardrail: ...`) reaches the UI unchanged.
func writeKubeErr(w http.ResponseWriter, err error) {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		switch code := int(st.Status().Code); code {
		case http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
			writeErr(w, code, err.Error())
			return
		}
	}
	writeErr(w, http.StatusBadGateway, err.Error())
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
		writeErr(w, http.StatusBadRequest, "unsupported kind")
		return
	}
	if !queries.ValidDNS(req.Namespace) || !queries.ValidDNS(req.Name) {
		writeErr(w, http.StatusBadRequest, "invalid namespace or name")
		return
	}
	kind := string(req.Kind)
	ctx := r.Context()

	ns, err := s.d.Kube.CoreV1().Namespaces().Get(ctx, req.Namespace, metav1.GetOptions{})
	if err != nil {
		audit(r, "restart", kind, req.Namespace, req.Name, err)
		writeKubeErr(w, err)
		return
	}
	if ns.Labels[s.d.Cfg.Conventions.ProtectedLabel] == "true" && req.Confirm != req.Name {
		err := errors.New("type the workload name to confirm")
		audit(r, "restart", kind, req.Namespace, req.Name, err)
		writeErr(w, http.StatusBadRequest, err.Error())
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
	audit(r, "restart", kind, req.Namespace, req.Name, err)
	if err != nil {
		writeKubeErr(w, err)
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
		writeErr(w, http.StatusBadRequest, "unsupported kind")
		return
	}
	if !queries.ValidDNS(req.Namespace) || !queries.ValidDNS(req.Name) {
		writeErr(w, http.StatusBadRequest, "invalid namespace or name")
		return
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{"reconcile.fluxcd.io/requestedAt": s.now().UTC().Format(time.RFC3339Nano)},
	}})
	_, err := s.d.Dyn.Resource(gvr).Namespace(req.Namespace).Patch(r.Context(), req.Name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: fieldManager})
	audit(r, "reconcile", string(req.Kind), req.Namespace, req.Name, err)
	if err != nil {
		writeKubeErr(w, err)
		return
	}
	accepted(w)
}
