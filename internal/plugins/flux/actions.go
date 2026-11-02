package flux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

const (
	actionReconcile = "reconcile"

	// requestedAtAnnotation is the annotation `flux reconcile` sets: the controllers reconcile an
	// object whose value changes.
	requestedAtAnnotation = "reconcile.fluxcd.io/requestedAt"
)

// reconcileInput is the body of the reconcile action: the request type the SPA already sends,
// with the checks that need no cluster. A kind other than the five Flux kinds, or a namespace or
// name that is not a DNS name, is a 400 before the cluster is read; a field the request does not
// have is a 400 too (the core decodes strictly).
type reconcileInput struct{ model.ReconcileRequest }

// Validate holds the checks that need no cluster.
func (in *reconcileInput) Validate() error {
	if _, ok := gvrOf(in.Kind); !ok {
		return errors.New("unsupported kind")
	}
	if !queries.ValidDNS(in.Namespace) || !queries.ValidDNS(in.Name) {
		return errors.New("invalid namespace or name")
	}
	return nil
}

func actionSpecs() []plugin.ActionSpec {
	return []plugin.ActionSpec{{
		Name: actionReconcile, Title: "Reconcile", MinRole: auth.RoleAdmin, Confirm: plugin.ConfirmNone,
		Destructive: false, Idempotent: true,
		Input: func() any { return &reconcileInput{} },
		Summary: func(in any) string {
			i := in.(*reconcileInput)
			return fmt.Sprintf("Reconcile %s %s/%s", i.Kind, i.Namespace, i.Name)
		},
		Target: func(in any) (string, string, string) {
			i := in.(*reconcileInput)
			return string(i.Kind), i.Namespace, i.Name
		},
	}}
}

func refusal(status int, code, message string) *plugin.ActionError {
	return &plugin.ActionError{Status: status, Code: code, Message: message}
}

// errNoCluster answers a reconcile while there is no kubernetes service to patch through: the
// kubernetes plugin is not running or exports none (it is in error). It is the answer the
// legacy route gave.
var errNoCluster = refusal(http.StatusServiceUnavailable, model.ErrCodeUpstreamUnavailable, "kubernetes is not available")

// Act runs the reconcile action: it sets the requestedAt annotation of the object to the time,
// which makes the Flux controllers reconcile it now. The merge patch holds that annotation and
// nothing else (an admission policy may deny any other field). The core answers 202 when Act
// returns without an error.
func (p *Plugin) Act(ctx context.Context, action string, _ auth.Principal, input any) (any, error) {
	in, ok := input.(*reconcileInput)
	if action != actionReconcile || !ok {
		return nil, refusal(http.StatusNotFound, model.ErrCodeNotFound, "no such action: "+action)
	}
	r := p.rt.Load()
	if r == nil {
		return nil, errNoCluster
	}
	return nil, r.reconcile(ctx, in)
}

func (r *runtime) reconcile(ctx context.Context, in *reconcileInput) error {
	gvr, ok := gvrOf(in.Kind)
	if !ok {
		return refusal(http.StatusBadRequest, model.ErrCodeInvalidInput, "unsupported kind")
	}
	c := r.lookup()
	if c == nil {
		return errNoCluster
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{requestedAtAnnotation: r.h.Now().UTC().Format(time.RFC3339Nano)},
	}})
	if err != nil {
		return err
	}
	if err := c.Patch(ctx, gvr, in.Namespace, in.Name, patch); err != nil {
		return r.kubeError(err)
	}
	return nil
}

// kubeError answers a Kubernetes API error. 403, 404, 409 and 422 keep their status and message
// so an admission denial reaches the UI unchanged: they come from the cluster's own policy, not
// from an upstream URL, and the message is scrubbed of secrets. Any other error is returned as
// it is, so the core answers a 502 naming the category only.
func (r *runtime) kubeError(err error) error {
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
	return refusal(code, errCode, r.h.ScrubLog(err.Error()))
}
