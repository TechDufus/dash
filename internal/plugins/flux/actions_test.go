package flux

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
)

var admin = auth.Principal{Kind: auth.KindProxy, User: "alice", Role: auth.RoleAdmin}

func reconcileOf(kind model.FluxKind, ns, name string) *reconcileInput {
	return &reconcileInput{model.ReconcileRequest{Kind: kind, Namespace: ns, Name: name}}
}

// reconcile runs the mounted action as the core's chain would after its checks.
func (r *rig) reconcile(in *reconcileInput) error {
	r.t.Helper()
	ref, ok := r.sup.Action(ID, "reconcile")
	if !ok {
		r.t.Fatal("reconcile is not mounted")
	}
	_, err := ref.Run(context.Background(), admin, in)
	return err
}

func wantActionError(t *testing.T, err error, status int, code string) *plugin.ActionError {
	t.Helper()
	var ae *plugin.ActionError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want a plugin.ActionError", err)
	}
	if ae.Status != status || ae.Code != code {
		t.Errorf("action error = %d %s (%s), want %d %s", ae.Status, ae.Code, ae.Message, status, code)
	}
	return ae
}

// The action is mounted when plugins.flux.actions.enabled is on and not otherwise, and a reload
// flips it live; the deny list of the section reaches the mounted action for the core to apply.
func TestReconcileIsMountedOnlyWhenEnabled(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitState(model.PluginHealthy)
	if _, ok := r.sup.Action(ID, "reconcile"); ok {
		t.Fatal("reconcile is mounted with actions.enabled off")
	}
	for _, pi := range r.sup.Infos() {
		if pi.ID == "flux" && len(pi.Actions) != 0 {
			t.Errorf("the row lists actions: %+v", pi.Actions)
		}
	}

	r.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &r.cfg, ActionsEnabled: true, DenyNamespaces: []string{"kube-system", "gitops"}})
	r.reload()
	eventually(t, "reconcile to be mounted", func() bool { _, ok := r.sup.Action(ID, "reconcile"); return ok })
	ref, _ := r.sup.Action(ID, "reconcile")
	if !slices.Equal(ref.DenyNamespaces, []string{"kube-system", "gitops"}) {
		t.Errorf("deny namespaces = %v", ref.DenyNamespaces)
	}
	if ref.Spec.MinRole != auth.RoleAdmin || ref.Spec.Confirm != plugin.ConfirmNone || ref.Spec.Destructive || !ref.Spec.Idempotent {
		t.Errorf("spec = %+v", ref.Spec)
	}
	var row model.PluginInfo
	eventually(t, "the row to list the action", func() bool { row = r.row(); return len(row.Actions) == 1 })
	if a := row.Actions[0]; a.Name != "reconcile" || a.Title != "Reconcile" {
		t.Errorf("row action = %+v", a)
	}

	r.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &r.cfg})
	r.reload()
	eventually(t, "reconcile to be unmounted", func() bool { _, ok := r.sup.Action(ID, "reconcile"); return !ok })
}

// The audit target and the confirmation text come from the input, and are what the legacy
// route's records had: kind, namespace and name.
func TestReconcileSpecDescribesTheInput(t *testing.T) {
	spec := actionSpecs()[0]
	in := reconcileOf(model.FluxKindKustomization, "gitops", "platform")
	kind, ns, name := spec.Target(in)
	if kind != "Kustomization" || ns != "gitops" || name != "platform" {
		t.Errorf("target = %s %s/%s", kind, ns, name)
	}
	if got := spec.Summary(in); got != "Reconcile Kustomization gitops/platform" {
		t.Errorf("summary = %q", got)
	}
	if _, ok := spec.Input().(*reconcileInput); !ok {
		t.Errorf("Input() = %T", spec.Input())
	}
}

func TestReconcileInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   model.ReconcileRequest
		ok   bool
	}{
		{"a kustomization", model.ReconcileRequest{Kind: kst, Namespace: "gitops", Name: "platform"}, true},
		{"a helm release", model.ReconcileRequest{Kind: hr, Namespace: "media", Name: "app"}, true},
		{"a git repository", model.ReconcileRequest{Kind: git, Namespace: "gitops", Name: "root"}, true},
		{"an oci repository", model.ReconcileRequest{Kind: oci, Namespace: "gitops", Name: "charts"}, true},
		{"a helm repository", model.ReconcileRequest{Kind: hlm, Namespace: "gitops", Name: "stable"}, true},
		{"another kind", model.ReconcileRequest{Kind: "Deployment", Namespace: "a", Name: "b"}, false},
		{"no kind", model.ReconcileRequest{Namespace: "a", Name: "b"}, false},
		{"a bad namespace", model.ReconcileRequest{Kind: kst, Namespace: "Not_DNS", Name: "b"}, false},
		{"a bad name", model.ReconcileRequest{Kind: kst, Namespace: "a", Name: "b/../c"}, false},
		{"no name", model.ReconcileRequest{Kind: kst, Namespace: "a"}, false},
		{"no namespace", model.ReconcileRequest{Kind: kst, Name: "b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (&reconcileInput{tc.in}).Validate()
			if (err == nil) != tc.ok {
				t.Errorf("Validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// The body is the annotation and nothing else, as the legacy route sent it: the cluster's admission policy
// denies any other field of a reconcile.
func TestReconcilePatchBodyIsExactlyTheAnnotation(t *testing.T) {
	r := newRig(t, rigOpts{actions: true})
	r.waitState(model.PluginHealthy)
	r.clk.advance(1500 * time.Millisecond)
	if err := r.reconcile(reconcileOf(model.FluxKindKustomization, "gitops", "platform")); err != nil {
		t.Fatal(err)
	}
	ps := r.kc.Patches()
	if len(ps) != 1 {
		t.Fatalf("%d patches", len(ps))
	}
	p := ps[0]
	if p.GVR != kube.GVRKustomizations || p.Namespace != "gitops" || p.Name != "platform" {
		t.Errorf("patch = %+v", p)
	}
	var body map[string]map[string]map[string]string
	if err := json.Unmarshal(p.Merge, &body); err != nil {
		t.Fatalf("body %s: %v", p.Merge, err)
	}
	if len(body) != 1 || len(body["metadata"]) != 1 || len(body["metadata"]["annotations"]) != 1 {
		t.Fatalf("body = %s, want {metadata:{annotations:{requestedAt}}} and nothing else", p.Merge)
	}
	got := body["metadata"]["annotations"]["reconcile.fluxcd.io/requestedAt"]
	at, err := time.Parse(time.RFC3339Nano, got)
	if err != nil || !at.Equal(r.clk.now()) {
		t.Errorf("requestedAt = %q (%v), want the host's now %v", got, err, r.clk.now())
	}
	if !strings.HasSuffix(got, "Z") {
		t.Errorf("requestedAt %q is not UTC", got)
	}
}

// Each of the five kinds patches its own resource.
func TestReconcileTargetsTheResourceOfTheKind(t *testing.T) {
	r := newRig(t, rigOpts{actions: true})
	r.waitState(model.PluginHealthy)
	for i, k := range kinds {
		if err := r.reconcile(reconcileOf(k, "ns", "obj")); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		ps := r.kc.Patches()
		if len(ps) != i+1 || ps[i].GVR != gvrs[i] || ps[i].Namespace != "ns" || ps[i].Name != "obj" {
			t.Fatalf("after %s: patches = %+v", k, ps)
		}
	}
}

// Without a kubernetes service (kubernetes is in error, or flux has not found it) the action is a
// 503 upstream_unavailable "kubernetes is not available": the legacy route's answer.
func TestReconcileWithoutAClusterIs503(t *testing.T) {
	r := newRig(t, rigOpts{actions: true, noService: true, kubeState: plugin.StateError})
	row := r.waitState(model.PluginStarting)
	if row.Message != "waiting for the kubernetes service" {
		t.Fatalf("row = %+v", row)
	}
	ae := wantActionError(t, r.reconcile(reconcileOf(model.FluxKindKustomization, "gitops", "platform")),
		http.StatusServiceUnavailable, model.ErrCodeUpstreamUnavailable)
	if ae.Message != "kubernetes is not available" {
		t.Errorf("message = %q", ae.Message)
	}
	if n := len(r.kc.Patches()); n != 0 {
		t.Errorf("%d patches without a service", n)
	}
	// The service returns: the same action works, with no restart of the plugin.
	r.kube.setService(r.kc)
	r.waitState(model.PluginHealthy)
	if err := r.reconcile(reconcileOf(model.FluxKindKustomization, "gitops", "platform")); err != nil {
		t.Errorf("after the service returned: %v", err)
	}
}

func statusErr(code int32, msg string) error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: code, Message: msg}}
}

// 403, 404, 409 and 422 keep their status and message (an admission denial reaches the user), with
// every secret scrubbed; any other error is handed to the core, which answers a 502 category.
func TestReconcileAPIErrorsAreMappedAndScrubbed(t *testing.T) {
	reg := secret.NewRegistry()
	reg.Add(secret.New(canary, ""))
	r := newRig(t, rigOpts{actions: true, scrub: reg.Scrub})
	r.waitState(model.PluginHealthy)
	msg := "admission policy: denied; the webhook echoed " + canary
	for _, tc := range []struct {
		code   int32
		status int
		want   string
	}{
		{http.StatusForbidden, http.StatusForbidden, model.ErrCodeForbidden},
		{http.StatusNotFound, http.StatusNotFound, model.ErrCodeNotFound},
		{http.StatusConflict, http.StatusConflict, model.ErrCodeConflict},
		{http.StatusUnprocessableEntity, http.StatusUnprocessableEntity, model.ErrCodeInvalidInput},
	} {
		t.Run(http.StatusText(int(tc.code)), func(t *testing.T) {
			r.kc.SetPatchError(statusErr(tc.code, msg))
			ae := wantActionError(t, r.reconcile(reconcileOf(model.FluxKindHelmRelease, "media", "app")), tc.status, tc.want)
			if !strings.Contains(ae.Message, "admission policy: denied") {
				t.Errorf("the policy message must reach the user: %q", ae.Message)
			}
			if strings.Contains(ae.Message, canary) {
				t.Errorf("the secret leaked: %q", ae.Message)
			}
		})
	}

	t.Run("another API status is not passed through", func(t *testing.T) {
		r.kc.SetPatchError(statusErr(http.StatusInternalServerError, "etcd said "+canary))
		err := r.reconcile(reconcileOf(model.FluxKindHelmRelease, "media", "app"))
		var ae *plugin.ActionError
		if err == nil || errors.As(err, &ae) {
			t.Errorf("err = %v, want a plain error for the core's 502", err)
		}
	})
	t.Run("a transport error is not passed through", func(t *testing.T) {
		r.kc.SetPatchError(errors.New("dial tcp 192.0.2.1:6443: connection refused"))
		err := r.reconcile(reconcileOf(model.FluxKindHelmRelease, "media", "app"))
		var ae *plugin.ActionError
		if err == nil || errors.As(err, &ae) {
			t.Errorf("err = %v, want a plain error", err)
		}
	})
}

func TestReconcileRefusesAnotherActionOrInput(t *testing.T) {
	r := newRig(t, rigOpts{actions: true})
	r.waitState(model.PluginHealthy)
	p := &Plugin{}
	_, err := p.Act(context.Background(), "restart", admin, reconcileOf(kst, "a", "b"))
	wantActionError(t, err, http.StatusNotFound, model.ErrCodeNotFound)
	_, err = p.Act(context.Background(), "reconcile", admin, &model.RestartRequest{})
	wantActionError(t, err, http.StatusNotFound, model.ErrCodeNotFound)
	// A plugin that has not started has no cluster to patch through.
	_, err = p.Act(context.Background(), "reconcile", admin, reconcileOf(kst, "a", "b"))
	wantActionError(t, err, http.StatusServiceUnavailable, model.ErrCodeUpstreamUnavailable)
	if n := len(r.kc.Patches()); n != 0 {
		t.Errorf("%d patches", n)
	}
}

// A reconcile of an object the cluster does not hold is the cluster's 404, not a check of the
// plugin's cache: the cache can lag.
func TestReconcileDoesNotConsultTheCache(t *testing.T) {
	r := newRig(t, rigOpts{actions: true})
	r.waitState(model.PluginHealthy)
	if err := r.reconcile(reconcileOf(model.FluxKindHelmRelease, "media", "not-in-the-cache")); err != nil {
		t.Fatal(err)
	}
	if n := len(r.kc.Patches()); n != 1 {
		t.Errorf("%d patches", n)
	}
}
