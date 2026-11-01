package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

// protectedLabel is the configured Conventions.ProtectedLabel of newServer.
const protectedLabel = "example.com/protected"

func namespace(name string, protected bool) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if protected {
		ns.Labels = map[string]string{protectedLabel: "true"}
	}
	return ns
}

func workloadObjects() []runtime.Object {
	meta := func(ns string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: "web", Namespace: ns} }
	return []runtime.Object{
		namespace("apps", false), namespace("platform", true),
		&appsv1.Deployment{ObjectMeta: meta("apps")},
		&appsv1.StatefulSet{ObjectMeta: meta("apps")},
		&appsv1.DaemonSet{ObjectMeta: meta("apps")},
		&appsv1.Deployment{ObjectMeta: meta("platform")},
	}
}

// patches returns the patch actions recorded by a fake.
func patches(actions []k8stesting.Action) []k8stesting.PatchActionImpl {
	var out []k8stesting.PatchActionImpl
	for _, a := range actions {
		if p, ok := a.(k8stesting.PatchActionImpl); ok {
			out = append(out, p)
		}
	}
	return out
}

func captureAudit(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func auditEntries(t *testing.T, buf *syncBuf) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewBufferString(buf.String()))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		if m["msg"] == "audit" {
			out = append(out, m)
		}
	}
	return out
}

func TestRestart(t *testing.T) {
	const restartedAt = "2026-10-30T12:00:00Z"
	wantPatch := `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"` + restartedAt + `"}}}}}`

	tests := []struct {
		name    string
		body    any
		code    int
		errMsg  string
		patched string // resource that must be patched; "" = no patch at all
		wantNS  string
	}{
		{name: "deployment in unprotected namespace", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "web"}, code: 202, patched: "deployments", wantNS: "apps"},
		{name: "statefulset", body: model.RestartRequest{Kind: "StatefulSet", Namespace: "apps", Name: "web"}, code: 202, patched: "statefulsets", wantNS: "apps"},
		{name: "daemonset", body: model.RestartRequest{Kind: "DaemonSet", Namespace: "apps", Name: "web"}, code: 202, patched: "daemonsets", wantNS: "apps"},
		{name: "kind Pod", body: model.RestartRequest{Kind: "Pod", Namespace: "apps", Name: "web"}, code: 400, errMsg: "unsupported kind"},
		{name: "kind Secret", body: model.RestartRequest{Kind: "Secret", Namespace: "apps", Name: "web"}, code: 400, errMsg: "unsupported kind"},
		{name: "kind Job", body: model.RestartRequest{Kind: "Job", Namespace: "apps", Name: "web"}, code: 400, errMsg: "unsupported kind"},
		{name: "empty kind", body: model.RestartRequest{Namespace: "apps", Name: "web"}, code: 400, errMsg: "unsupported kind"},
		{name: "bad namespace", body: model.RestartRequest{Kind: "Deployment", Namespace: "Apps/..", Name: "web"}, code: 400},
		{name: "bad name", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: ""}, code: 400},
		{name: "garbage body", body: "{nope", code: 400},
		{name: "protected without confirm", body: model.RestartRequest{Kind: "Deployment", Namespace: "platform", Name: "web"}, code: 400, errMsg: "type the workload name to confirm"},
		{name: "protected with wrong confirm", body: model.RestartRequest{Kind: "Deployment", Namespace: "platform", Name: "web", Confirm: "other"}, code: 400, errMsg: "type the workload name to confirm"},
		{name: "protected with confirm", body: model.RestartRequest{Kind: "Deployment", Namespace: "platform", Name: "web", Confirm: "web"}, code: 202, patched: "deployments", wantNS: "platform"},
		{name: "unknown namespace", body: model.RestartRequest{Kind: "Deployment", Namespace: "ghost", Name: "web"}, code: 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kc := fake.NewClientset(workloadObjects()...)
			s := newServer(func(d *Deps) { d.Kube = kc })
			w := req{method: "POST", path: "/api/actions/restart", body: tc.body}.do(s.Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			if tc.errMsg != "" && decodeErr(t, w) != tc.errMsg {
				t.Errorf("error = %q, want %q", decodeErr(t, w), tc.errMsg)
			}
			got := patches(kc.Actions())
			if tc.patched == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected patch actions: %v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("patch actions = %d, want 1", len(got))
			}
			p := got[0]
			if p.GetResource().Resource != tc.patched || p.GetNamespace() != tc.wantNS || p.GetName() != "web" {
				t.Errorf("patched %s %s/%s", p.GetResource().Resource, p.GetNamespace(), p.GetName())
			}
			if p.GetPatchType() != types.StrategicMergePatchType {
				t.Errorf("patch type = %s", p.GetPatchType())
			}
			if string(p.GetPatch()) != wantPatch {
				t.Errorf("patch = %s\nwant    %s", p.GetPatch(), wantPatch)
			}
			if fm := p.GetPatchOptions().FieldManager; fm != "dash" {
				t.Errorf("field manager = %q, want dash", fm)
			}
			if w.Body.String() != "{\"status\":\"accepted\"}\n" {
				t.Errorf("body = %q", w.Body)
			}
		})
	}
}

// With no protected label configured nothing is protected: a namespace that carries some label
// is restarted without the typed confirmation.
func TestRestartWithoutProtectedLabelSkipsConfirmation(t *testing.T) {
	kc := fake.NewClientset(workloadObjects()...)
	s := newServer(func(d *Deps) { d.Kube = kc; d.Cfg.Conventions = config.Conventions{} })
	w := req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "platform", Name: "web"}}.do(s.Handler())
	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202 (%s)", w.Code, w.Body)
	}
	if got := patches(kc.Actions()); len(got) != 1 {
		t.Fatalf("patch actions = %d, want 1", len(got))
	}
}

func TestRestartKubeErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{"guardrail forbidden verbatim", &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 403, Reason: metav1.StatusReasonForbidden, Message: "homelab guardrail: x"}}, 403},
		{"unprocessable", &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 422, Message: "homelab guardrail: invalid"}}, 422},
		{"conflict", &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 409, Reason: metav1.StatusReasonConflict, Message: "conflict"}}, 409},
		{"other api error", apierrors.NewServiceUnavailable("down"), 502},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kc := fake.NewClientset(workloadObjects()...)
			kc.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, tc.err })
			s := newServer(func(d *Deps) { d.Kube = kc })
			w := req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "web"}}.do(s.Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d", w.Code, tc.code)
			}
			if got := decodeErr(t, w); tc.code != 502 && got != tc.err.Error() {
				t.Errorf("error = %q, want verbatim %q", got, tc.err.Error())
			}
		})
	}
}

func fluxObject(gvr schema.GroupVersionResource, kind string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.GroupVersion().String(),
		"kind":       kind,
		"metadata":   map[string]any{"name": "app", "namespace": "flux-system"},
	}}
}

func TestReconcile(t *testing.T) {
	const requestedAt = "2026-10-30T12:00:00.123456789Z"
	wantPatch := `{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"` + requestedAt + `"}}}`

	tests := []struct {
		kind model.FluxKind
		gvr  schema.GroupVersionResource
	}{
		{"Kustomization", schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}},
		{"HelmRelease", schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}},
		{"GitRepository", schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}},
		{"OCIRepository", schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"}},
		{"HelmRepository", schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"}},
	}
	listKinds := map[schema.GroupVersionResource]string{}
	var objs []runtime.Object
	for _, tc := range tests {
		listKinds[tc.gvr] = string(tc.kind) + "List"
		objs = append(objs, fluxObject(tc.gvr, string(tc.kind)))
	}

	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
			s := newServer(func(d *Deps) { d.Dyn = dyn })
			w := req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: tc.kind, Namespace: "flux-system", Name: "app"}}.do(s.Handler())
			if w.Code != 202 {
				t.Fatalf("code = %d (%s)", w.Code, w.Body)
			}
			got := patches(dyn.Actions())
			if len(got) != 1 {
				t.Fatalf("patch actions = %d", len(got))
			}
			p := got[0]
			if p.GetResource() != tc.gvr || p.GetNamespace() != "flux-system" || p.GetName() != "app" {
				t.Errorf("patched %v %s/%s", p.GetResource(), p.GetNamespace(), p.GetName())
			}
			if p.GetPatchType() != types.MergePatchType || string(p.GetPatch()) != wantPatch {
				t.Errorf("patch = %s (%s)", p.GetPatch(), p.GetPatchType())
			}
			if fm := p.GetPatchOptions().FieldManager; fm != "dash" {
				t.Errorf("field manager = %q", fm)
			}
		})
	}

	for _, tc := range []struct {
		name string
		body model.ReconcileRequest
	}{
		{"unsupported kind", model.ReconcileRequest{Kind: "Secret", Namespace: "flux-system", Name: "app"}},
		{"empty kind", model.ReconcileRequest{Namespace: "flux-system", Name: "app"}},
		{"bad name", model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "A B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
			s := newServer(func(d *Deps) { d.Dyn = dyn })
			w := req{method: "POST", path: "/api/actions/reconcile", body: tc.body}.do(s.Handler())
			if w.Code != http.StatusBadRequest || len(patches(dyn.Actions())) != 0 {
				t.Errorf("code = %d, patches = %d", w.Code, len(patches(dyn.Actions())))
			}
		})
	}

	t.Run("guardrail denial passes through", func(t *testing.T) {
		deny := &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 403, Reason: metav1.StatusReasonForbidden, Message: "homelab guardrail: no"}}
		dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
		dyn.PrependReactor("patch", "*", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, deny })
		s := newServer(func(d *Deps) { d.Dyn = dyn })
		w := req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "app"}}.do(s.Handler())
		if w.Code != 403 || decodeErr(t, w) != "homelab guardrail: no" {
			t.Errorf("code = %d body = %s", w.Code, w.Body)
		}
	})
}

func TestAuditLog(t *testing.T) {
	buf := captureAudit(t)
	kc := fake.NewClientset(workloadObjects()...)
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), nil)
	s := newServer(func(d *Deps) { d.Kube = kc; d.Dyn = dyn })
	h := s.Handler()

	req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "web"}}.do(h)
	req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "ghost"}}.do(h) // NotFound

	entries := auditEntries(t, buf)
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d: %s", len(entries), buf.String())
	}
	for i, want := range []map[string]any{
		{"user": "alice", "action": "restart", "kind": "Deployment", "namespace": "apps", "name": "web", "result": "ok"},
		{"user": "alice", "action": "reconcile", "kind": "Kustomization", "namespace": "flux-system", "name": "ghost", "result": "error"},
	} {
		for k, v := range want {
			if entries[i][k] != v {
				t.Errorf("entry %d: %s = %v, want %v", i, k, entries[i][k], v)
			}
		}
	}
	if entries[1]["error"] == "" {
		t.Error("error field must carry the failure")
	}
}
