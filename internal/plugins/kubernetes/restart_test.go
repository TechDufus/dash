package kubernetes

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/jsonschema"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// restartSecret is a value restartHost.ScrubLog removes, standing for a secret of the core.
const restartSecret = "s3cr3t-token"

// restartClock is the fake clock of the tests, in a zone that is not UTC: the annotation is
// stamped in UTC.
var restartClock = time.Date(2026, 11, 2, 12, 0, 0, 0, time.FixedZone("CEST", 2*3600))

// restartStamp is restartClock as the annotation holds it.
const restartStamp = "2026-11-02T10:00:00Z"

// restartHost is the plugin.Host of the action tests: a fixed clock and a scrubber that removes
// restartSecret.
type restartHost struct{}

func (restartHost) Publish(string, any, []model.SourceStatus) {}
func (restartHost) Logger() *slog.Logger                      { return slog.New(slog.DiscardHandler) }
func (restartHost) HTTPClient(plugin.HTTPConfig) (*http.Client, error) {
	return nil, errors.New("restartHost has no HTTP client")
}
func (restartHost) ScrubLog(s string) string {
	return strings.ReplaceAll(s, restartSecret, "[redacted]")
}
func (restartHost) Links() plugin.LinkResolver    { return nil }
func (restartHost) Service(plugin.ID) (any, bool) { return nil, false }
func (restartHost) Now() time.Time                { return restartClock }
func (restartHost) Go(_ string, fn func())        { go fn() }

func restartConventions(label string) pluginconf.Kubernetes {
	return pluginconf.Kubernetes{Conventions: pluginconf.KubernetesConventions{ProtectedLabel: label}}
}

// restartEnv is a running plugin over a fake clientset.
type restartEnv struct {
	p  *Plugin
	r  *runtime
	cs *kubefake.Clientset
}

func newRestartEnv(t *testing.T, cfg pluginconf.Kubernetes, objs ...k8sruntime.Object) *restartEnv {
	t.Helper()
	c := testCluster(t, nil, objs...)
	cs, ok := c.Client.(*kubefake.Clientset)
	if !ok {
		t.Fatalf("the test cluster's client is %T, not a fake clientset", c.Client)
	}
	r := &runtime{h: restartHost{}, cfg: cfg, cl: c}
	p := &Plugin{}
	p.rt.Store(r)
	return &restartEnv{p: p, r: r, cs: cs}
}

func restartReq(kind model.WorkloadKind, ns, name, confirm string) *restartInput {
	return &restartInput{model.RestartRequest{Kind: kind, Namespace: ns, Name: name, Confirm: confirm}}
}

func (e *restartEnv) act(in *restartInput) (any, error) {
	return e.p.Act(context.Background(), "restart", auth.Principal{}, in)
}

// patches are the patch actions the fake clientset recorded.
func (e *restartEnv) patches() []k8stesting.PatchActionImpl {
	var out []k8stesting.PatchActionImpl
	for _, a := range e.cs.Actions() {
		if pa, ok := a.(k8stesting.PatchActionImpl); ok {
			out = append(out, pa)
		}
	}
	return out
}

// writes are the verbs of every action other than a read.
func (e *restartEnv) writes() []string {
	var out []string
	for _, a := range e.cs.Actions() {
		if v := a.GetVerb(); v != "get" && v != "list" && v != "watch" {
			out = append(out, v+" "+a.GetResource().Resource)
		}
	}
	return out
}

// failPatch makes every patch of resource fail with err.
func (e *restartEnv) failPatch(resource string, err error) {
	e.cs.PrependReactor("patch", resource, func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, err
	})
}

// restartDaemonSet is a DaemonSet without a spec.
func restartDaemonSet(ns, name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func restartStatusError(code int32, reason metav1.StatusReason, msg string) error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: code, Reason: reason, Message: msg}}
}

func TestRestartPatchBodyIsExactlyTheAnnotation(t *testing.T) {
	const want = `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"` + restartStamp + `"}}}}}`
	dep := deployment("apps", "web", 1, 1)
	sts := statefulSet("apps", "db", 1, 1)
	ds := restartDaemonSet("apps", "agent")
	for _, tc := range []struct {
		kind     model.WorkloadKind
		name     string
		resource string
		stamped  func(e *restartEnv) map[string]string
	}{
		{model.WorkloadKindDeployment, "web", "deployments", func(e *restartEnv) map[string]string {
			o, err := e.cs.AppsV1().Deployments("apps").Get(context.Background(), "web", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return o.Spec.Template.Annotations
		}},
		{model.WorkloadKindStatefulSet, "db", "statefulsets", func(e *restartEnv) map[string]string {
			o, err := e.cs.AppsV1().StatefulSets("apps").Get(context.Background(), "db", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return o.Spec.Template.Annotations
		}},
		{model.WorkloadKindDaemonSet, "agent", "daemonsets", func(e *restartEnv) map[string]string {
			o, err := e.cs.AppsV1().DaemonSets("apps").Get(context.Background(), "agent", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return o.Spec.Template.Annotations
		}},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			e := newRestartEnv(t, pluginconf.Kubernetes{}, namespace("apps", nil), dep.DeepCopy(), sts.DeepCopy(), ds.DeepCopy())
			out, err := e.act(restartReq(tc.kind, "apps", tc.name, ""))
			if out != nil || err != nil {
				t.Fatalf("Act = %v, %v: the core answers 202 for nil, nil", out, err)
			}
			got := e.patches()
			if len(got) != 1 {
				t.Fatalf("%d patch actions, want 1: %v", len(got), e.cs.Actions())
			}
			p := got[0]
			if p.GetResource().Resource != tc.resource || p.GetResource().Group != "apps" || p.GetNamespace() != "apps" || p.GetName() != tc.name {
				t.Errorf("patched %v %s/%s", p.GetResource(), p.GetNamespace(), p.GetName())
			}
			if p.GetPatchType() != types.StrategicMergePatchType {
				t.Errorf("patch type = %s, want strategic merge", p.GetPatchType())
			}
			if p.PatchOptions.FieldManager != "dash" {
				t.Errorf("field manager = %q, want dash", p.PatchOptions.FieldManager)
			}
			if string(p.GetPatch()) != want {
				t.Errorf("patch body = %s\nwant       %s", p.GetPatch(), want)
			}
			if w := e.writes(); !slices.Equal(w, []string{"patch " + tc.resource}) {
				t.Errorf("writes = %v, want the one patch", w)
			}
			if a := tc.stamped(e); a["kubectl.kubernetes.io/restartedAt"] != restartStamp {
				t.Errorf("the pod template annotations after the restart = %v", a)
			}
		})
	}
}

func TestRestartValidate(t *testing.T) {
	tests := []struct {
		name string
		in   *restartInput
		ok   bool
	}{
		{"deployment", restartReq(model.WorkloadKindDeployment, "apps", "web", ""), true},
		{"statefulset", restartReq(model.WorkloadKindStatefulSet, "apps", "db", ""), true},
		{"daemonset", restartReq(model.WorkloadKindDaemonSet, "kube-system", "agent.v2", ""), true},
		{"job", restartReq(model.WorkloadKindJob, "apps", "web", ""), false},
		{"cronjob", restartReq(model.WorkloadKindCronJob, "apps", "web", ""), false},
		{"pod", restartReq("Pod", "apps", "web", ""), false},
		{"no kind", restartReq("", "apps", "web", ""), false},
		{"lowercase kind", restartReq("deployment", "apps", "web", ""), false},
		{"empty namespace", restartReq(model.WorkloadKindDeployment, "", "web", ""), false},
		{"empty name", restartReq(model.WorkloadKindDeployment, "apps", "", ""), false},
		{"upper case name", restartReq(model.WorkloadKindDeployment, "apps", "Web", ""), false},
		{"name with a slash", restartReq(model.WorkloadKindDeployment, "apps", "web/x", ""), false},
		{"namespace with a path", restartReq(model.WorkloadKindDeployment, "../kube-system", "web", ""), false},
		{"name with a space", restartReq(model.WorkloadKindDeployment, "apps", "we b", ""), false},
		{"name of 254 characters", restartReq(model.WorkloadKindDeployment, "apps", strings.Repeat("a", 254), ""), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.in.Validate(); (err == nil) != tc.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestRestartProtectedNamespaceNeedsTheTypedName(t *testing.T) {
	const label = "example.com/protected"
	objs := func() []k8sruntime.Object {
		return []k8sruntime.Object{namespace("vault", map[string]string{label: "true"}), deployment("vault", "db", 1, 1)}
	}
	for _, confirm := range []string{"", "vault", "DB", "db "} {
		t.Run("refused with "+confirm, func(t *testing.T) {
			e := newRestartEnv(t, restartConventions(label), objs()...)
			_, err := e.act(restartReq(model.WorkloadKindDeployment, "vault", "db", confirm))
			var ae *plugin.ActionError
			if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest || ae.Code != model.ErrCodeInvalidInput || ae.Message != "type the workload name to confirm" {
				t.Fatalf("err = %#v, want a 400 invalid_input asking for the name", err)
			}
			if w := e.writes(); len(w) != 0 {
				t.Errorf("a refused restart wrote %v", w)
			}
		})
	}
	t.Run("accepted with the workload's name", func(t *testing.T) {
		e := newRestartEnv(t, restartConventions(label), objs()...)
		if _, err := e.act(restartReq(model.WorkloadKindDeployment, "vault", "db", "db")); err != nil {
			t.Fatal(err)
		}
		if n := len(e.patches()); n != 1 {
			t.Errorf("%d patches, want 1", n)
		}
	})
	t.Run("a label with another value does not protect", func(t *testing.T) {
		e := newRestartEnv(t, restartConventions(label), namespace("vault", map[string]string{label: "false"}), deployment("vault", "db", 1, 1))
		if _, err := e.act(restartReq(model.WorkloadKindDeployment, "vault", "db", "")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRestartUnprotectedNamespaceIgnoresConfirm(t *testing.T) {
	const label = "example.com/protected"
	for _, confirm := range []string{"", "db", "something else"} {
		e := newRestartEnv(t, restartConventions(label), namespace("apps", nil), deployment("apps", "db", 1, 1))
		if _, err := e.act(restartReq(model.WorkloadKindDeployment, "apps", "db", confirm)); err != nil {
			t.Fatalf("confirm %q: %v", confirm, err)
		}
		if n := len(e.patches()); n != 1 {
			t.Errorf("confirm %q: %d patches, want 1", confirm, n)
		}
	}
	t.Run("without a configured label no namespace is protected", func(t *testing.T) {
		e := newRestartEnv(t, pluginconf.Kubernetes{}, namespace("vault", map[string]string{label: "true"}), deployment("vault", "db", 1, 1))
		if _, err := e.act(restartReq(model.WorkloadKindDeployment, "vault", "db", "")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRestartAPIErrorsKeepTheirStatus(t *testing.T) {
	denial := "admission webhook \"guardrail\" denied the request: homelab guardrail: no restarts today " + restartSecret
	for _, tc := range []struct {
		code   int32
		reason metav1.StatusReason
		want   string
	}{
		{403, metav1.StatusReasonForbidden, model.ErrCodeForbidden},
		{404, metav1.StatusReasonNotFound, model.ErrCodeNotFound},
		{409, metav1.StatusReasonConflict, model.ErrCodeConflict},
		{422, metav1.StatusReasonInvalid, model.ErrCodeInvalidInput},
	} {
		t.Run(http.StatusText(int(tc.code)), func(t *testing.T) {
			e := newRestartEnv(t, pluginconf.Kubernetes{}, namespace("apps", nil), deployment("apps", "web", 1, 1))
			e.failPatch("deployments", restartStatusError(tc.code, tc.reason, denial))
			_, err := e.act(restartReq(model.WorkloadKindDeployment, "apps", "web", ""))
			var ae *plugin.ActionError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %#v, want an ActionError", err)
			}
			if ae.Status != int(tc.code) || ae.Code != tc.want {
				t.Errorf("status %d code %q, want %d %q", ae.Status, ae.Code, tc.code, tc.want)
			}
			if !strings.Contains(ae.Message, "homelab guardrail: no restarts today") {
				t.Errorf("message %q lost the admission denial", ae.Message)
			}
			if strings.Contains(ae.Message, restartSecret) || !strings.Contains(ae.Message, "[redacted]") {
				t.Errorf("message %q was not scrubbed through Host.ScrubLog", ae.Message)
			}
		})
	}

	t.Run("other API errors are returned as they are", func(t *testing.T) {
		for _, raw := range []error{
			restartStatusError(500, metav1.StatusReasonInternalError, "boom "+restartSecret),
			restartStatusError(401, metav1.StatusReasonUnauthorized, "no"),
			restartStatusError(429, metav1.StatusReasonTooManyRequests, "slow down"),
			errors.New("dial tcp 10.0.0.1:6443: connection refused"),
		} {
			e := newRestartEnv(t, pluginconf.Kubernetes{}, namespace("apps", nil), deployment("apps", "web", 1, 1))
			e.failPatch("deployments", raw)
			_, err := e.act(restartReq(model.WorkloadKindDeployment, "apps", "web", ""))
			var ae *plugin.ActionError
			if errors.As(err, &ae) {
				t.Errorf("%v became the refusal %v: the core must answer 502 with a category only", raw, ae)
			}
			if !errors.Is(err, raw) {
				t.Errorf("err = %v, want the original %v", err, raw)
			}
		}
	})

	t.Run("a namespace that does not exist is a 404", func(t *testing.T) {
		e := newRestartEnv(t, pluginconf.Kubernetes{})
		_, err := e.act(restartReq(model.WorkloadKindDeployment, "gone", "web", ""))
		var ae *plugin.ActionError
		if !errors.As(err, &ae) || ae.Status != http.StatusNotFound || ae.Code != model.ErrCodeNotFound {
			t.Fatalf("err = %#v, want a 404 not_found", err)
		}
		if w := e.writes(); len(w) != 0 {
			t.Errorf("wrote %v", w)
		}
	})

	t.Run("a workload that does not exist is a 404", func(t *testing.T) {
		e := newRestartEnv(t, pluginconf.Kubernetes{}, namespace("apps", nil))
		_, err := e.act(restartReq(model.WorkloadKindStatefulSet, "apps", "gone", ""))
		var ae *plugin.ActionError
		if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
			t.Fatalf("err = %#v, want a 404", err)
		}
	})

	t.Run("the namespace read fails with a server error", func(t *testing.T) {
		e := newRestartEnv(t, pluginconf.Kubernetes{}, namespace("apps", nil))
		raw := restartStatusError(500, metav1.StatusReasonInternalError, "etcd down")
		e.cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, raw })
		_, err := e.act(restartReq(model.WorkloadKindDeployment, "apps", "web", ""))
		var ae *plugin.ActionError
		if errors.As(err, &ae) || !errors.Is(err, raw) {
			t.Errorf("err = %#v, want the raw error", err)
		}
	})
}

// The namespace is read before anything else: a protected namespace is never patched on a wrong
// confirmation, even for a workload that does not exist.
func TestRestartChecksRunInOrder(t *testing.T) {
	const label = "example.com/protected"
	e := newRestartEnv(t, restartConventions(label), namespace("vault", map[string]string{label: "true"}))
	_, err := e.act(restartReq(model.WorkloadKindDeployment, "vault", "gone", "nope"))
	var ae *plugin.ActionError
	if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		t.Fatalf("err = %#v, want the 400 for the confirmation before any patch", err)
	}
	if n := len(e.patches()); n != 0 {
		t.Errorf("%d patches sent", n)
	}
}

func TestRestartBeforeStartIsNotRunning(t *testing.T) {
	p := &Plugin{}
	_, err := p.Act(context.Background(), "restart", auth.Principal{}, restartReq(model.WorkloadKindDeployment, "apps", "web", ""))
	if !errors.Is(err, errNotRunning) {
		t.Fatalf("err = %v, want errNotRunning", err)
	}
}

func TestRestartUnknownAction(t *testing.T) {
	e := newRestartEnv(t, pluginconf.Kubernetes{}, namespace("apps", nil), deployment("apps", "web", 1, 1))
	_, err := e.p.Act(context.Background(), "reconcile", auth.Principal{}, restartReq(model.WorkloadKindDeployment, "apps", "web", ""))
	var ae *plugin.ActionError
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound || ae.Code != model.ErrCodeNotFound {
		t.Fatalf("err = %#v, want a 404 not_found", err)
	}
	if w := e.writes(); len(w) != 0 {
		t.Errorf("an unknown action wrote %v", w)
	}
}

func TestActionSpecTargetsAndSummaries(t *testing.T) {
	specs := actionSpecs()
	if len(specs) != 1 {
		t.Fatalf("%d action specs, want 1", len(specs))
	}
	s := specs[0]
	if s.Name != "restart" || s.Title != "Restart" || s.MinRole != auth.RoleAdmin || s.Confirm != plugin.ConfirmDialog || !s.Destructive || s.Idempotent {
		t.Errorf("spec = %+v", s)
	}
	in := restartReq(model.WorkloadKindStatefulSet, "apps", "db", "")
	if k, ns, n := s.Target(in); k != "StatefulSet" || ns != "apps" || n != "db" {
		t.Errorf("target = %s/%s/%s", k, ns, n)
	}
	if got := s.Summary(in); got != "Restart StatefulSet apps/db" {
		t.Errorf("summary = %q", got)
	}
	if _, ok := s.Input().(*restartInput); !ok {
		t.Errorf("Input() is %T", s.Input())
	}
}

// The input describes itself to the OpenAPI generator and the MCP tool: the embedded request is
// flattened into one object that rejects other fields.
func TestRestartInputSchema(t *testing.T) {
	doc, err := jsonschema.Document(&restartInput{})
	if err != nil {
		t.Fatal(err)
	}
	props, _ := doc["properties"].(jsonschema.Schema)
	for _, name := range []string{"kind", "namespace", "name", "confirm"} {
		if _, ok := props[name]; !ok {
			t.Errorf("restart schema has no %q: %v", name, doc)
		}
	}
	if doc["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v", doc["additionalProperties"])
	}
}
