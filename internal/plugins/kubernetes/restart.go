package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

const (
	actionRestart = "restart"

	// restartedAtAnnotation is the pod template annotation kubectl rollout restart sets.
	restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"
)

// restartInput is the body of the restart action: the request type the SPA already sends, with
// the checks that need no cluster. A kind other than Deployment, StatefulSet or DaemonSet, or a
// namespace or name that is not a DNS name, is a 400 before the cluster is read.
type restartInput struct{ model.RestartRequest }

// Validate holds the checks that need no cluster.
func (in *restartInput) Validate() error {
	switch in.Kind {
	case model.WorkloadKindDeployment, model.WorkloadKindStatefulSet, model.WorkloadKindDaemonSet:
	default:
		return errors.New("unsupported kind")
	}
	if !queries.ValidDNS(in.Namespace) || !queries.ValidDNS(in.Name) {
		return errors.New("invalid namespace or name")
	}
	return nil
}

// actionSpecs are the plugin's actions. ConfirmFor closes over p: it reads the instance's last
// kubernetes.cluster payload.
func actionSpecs(p *Plugin) []plugin.ActionSpec {
	return []plugin.ActionSpec{{
		Name: actionRestart, Title: "Restart", MinRole: auth.RoleAdmin, Confirm: plugin.ConfirmDialog,
		Destructive: true, Idempotent: false,
		Input: func() any { return &restartInput{} },
		Summary: func(in any) string {
			i := in.(*restartInput)
			return fmt.Sprintf("Restart %s %s/%s", i.Kind, i.Namespace, i.Name)
		},
		Target: func(in any) (string, string, string) {
			i := in.(*restartInput)
			return string(i.Kind), i.Namespace, i.Name
		},
		ConfirmFor: p.restartConfirm,
	}}
}

// restartConfirm is the confirmation rule of a restart (contract D49): the workload's name typed
// in a namespace the published kubernetes.cluster marks Protected, the dialog elsewhere. What
// the payload cannot answer (no running instance, nothing published yet, a namespace it does not
// list) is typeName, the strictest rule. Act still reads the namespace's label from the cluster
// and refuses a protected restart without the name, whatever this answered.
func (p *Plugin) restartConfirm(_ context.Context, input any) plugin.Confirm {
	in, ok := input.(*restartInput)
	r := p.rt.Load()
	if !ok || r == nil {
		return plugin.ConfirmTypeName
	}
	cl, _ := r.builtPayloads()
	if cl == nil {
		return plugin.ConfirmTypeName
	}
	for _, ns := range cl.Namespaces {
		if ns.Name != in.Namespace {
			continue
		}
		if ns.Protected {
			return plugin.ConfirmTypeName
		}
		return plugin.ConfirmDialog
	}
	return plugin.ConfirmTypeName
}

func actionRefusal(status int, code, message string) *plugin.ActionError {
	return &plugin.ActionError{Status: status, Code: code, Message: message}
}

// Act runs the restart action: a rollout restart of a Deployment, StatefulSet or DaemonSet,
// which stamps the pod template with the time. In a namespace the protected label marks, the
// request must carry the workload's name as its confirmation. The core answers 202 when Act
// returns without an error.
func (p *Plugin) Act(ctx context.Context, action string, pr auth.Principal, input any) (any, error) {
	in, ok := input.(*restartInput)
	if action != actionRestart || !ok {
		return nil, actionRefusal(http.StatusNotFound, model.ErrCodeNotFound, "no such action: "+action)
	}
	r := p.rt.Load()
	if r == nil {
		return nil, errNotRunning
	}
	ns, err := r.cl.Client.CoreV1().Namespaces().Get(ctx, in.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, kubeActionError(r, err)
	}
	// An unconfigured label never matches: without a protected label no namespace asks for the name.
	if label := r.cfg.Conventions.ProtectedLabel; label != "" && ns.Labels[label] == "true" && in.Confirm != in.Name {
		return nil, actionRefusal(http.StatusBadRequest, model.ErrCodeInvalidInput, "type the workload name to confirm")
	}

	// The body holds the annotation and nothing else: an admission policy may deny any other
	// field of a restart.
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{restartedAtAnnotation: r.h.Now().UTC().Format(time.RFC3339)},
	}}}})
	if err != nil {
		return nil, err
	}
	opts := metav1.PatchOptions{FieldManager: kube.FieldManager}
	apps := r.cl.Client.AppsV1()
	switch in.Kind {
	case model.WorkloadKindDeployment:
		_, err = apps.Deployments(in.Namespace).Patch(ctx, in.Name, types.StrategicMergePatchType, patch, opts)
	case model.WorkloadKindStatefulSet:
		_, err = apps.StatefulSets(in.Namespace).Patch(ctx, in.Name, types.StrategicMergePatchType, patch, opts)
	default:
		_, err = apps.DaemonSets(in.Namespace).Patch(ctx, in.Name, types.StrategicMergePatchType, patch, opts)
	}
	if err != nil {
		return nil, kubeActionError(r, err)
	}
	return nil, nil
}

// kubeActionError answers a Kubernetes API error. 403, 404, 409 and 422 keep their status and
// message so an admission denial reaches the UI unchanged: they come from the cluster's own
// policy, not from an upstream URL, and the message is scrubbed of secrets. Any other error is
// returned as it is, so the core answers a 502 naming the category only.
func kubeActionError(r *runtime, err error) error {
	var st apierrors.APIStatus
	if !errors.As(err, &st) {
		return err
	}
	code := int(st.Status().Code)
	var errCode string
	switch code {
	case http.StatusForbidden:
		errCode = model.ErrCodeForbidden
	case http.StatusNotFound:
		errCode = model.ErrCodeNotFound
	case http.StatusConflict:
		errCode = model.ErrCodeConflict
	case http.StatusUnprocessableEntity:
		errCode = model.ErrCodeInvalidInput
	default:
		return err
	}
	return actionRefusal(code, errCode, r.h.ScrubLog(err.Error()))
}
