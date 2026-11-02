package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/svc"
)

// fakeKubeCluster is the svc.Cluster the kubernetes plugin exports, over a fake dynamic client: its
// Patch sends the JSON merge patch under the field manager "dash", as the plugin's does. Nothing
// else of the interface is read by the API.
type fakeKubeCluster struct{ dyn dynamic.Interface }

func (f fakeKubeCluster) Patch(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, merge []byte) error {
	_, err := f.dyn.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, merge, metav1.PatchOptions{FieldManager: "dash"})
	return err
}

func (fakeKubeCluster) Synced() bool                                      { return true }
func (fakeKubeCluster) Has(schema.GroupVersionResource) bool              { return true }
func (fakeKubeCluster) Listed(schema.GroupVersionResource) bool           { return true }
func (fakeKubeCluster) FailedGroups() []schema.GroupVersion               { return nil }
func (fakeKubeCluster) Watch(...schema.GroupVersionResource)              {}
func (fakeKubeCluster) GVRRevision(...schema.GroupVersionResource) uint64 { return 0 }
func (fakeKubeCluster) List(schema.GroupVersionResource) []*unstructured.Unstructured {
	return nil
}

var _ svc.Cluster = fakeKubeCluster{}

// withCluster serves the Flux routes through dyn, as the kubernetes plugin's service does.
func withCluster(dyn dynamic.Interface) func(*Deps) {
	return func(d *Deps) { d.Cluster = func() (svc.Cluster, bool) { return fakeKubeCluster{dyn}, true } }
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
			s := newServer(withCluster(dyn))
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
			if w.Body.String() != "{\"status\":\"accepted\"}\n" {
				t.Errorf("body = %q", w.Body)
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
			s := newServer(withCluster(dyn))
			w := req{method: "POST", path: "/api/actions/reconcile", body: tc.body}.do(s.Handler())
			if w.Code != http.StatusBadRequest || len(patches(dyn.Actions())) != 0 {
				t.Errorf("code = %d, patches = %d", w.Code, len(patches(dyn.Actions())))
			}
		})
	}
}

// What the API server answers a patch with reaches the caller as the cluster's own words for the
// statuses a policy gives (403, 404, 409, 422); anything else is a 502 with the category only.
func TestReconcileKubeErrors(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	listKinds := map[schema.GroupVersionResource]string{gvr: "KustomizationList"}
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
			dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, fluxObject(gvr, "Kustomization"))
			dyn.PrependReactor("patch", "*", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, tc.err })
			s := newServer(withCluster(dyn))
			w := req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "app"}}.do(s.Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			if got := decodeErr(t, w); tc.code != 502 && got != tc.err.Error() {
				t.Errorf("error = %q, want verbatim %q", got, tc.err.Error())
			}
		})
	}
}

// Without the kubernetes plugin's service (it is off, or not configured) there is nothing to patch
// through: 503 upstream_unavailable, audited as a failure, and no word of the cause.
func TestReconcileWithoutTheKubernetesService(t *testing.T) {
	buf := captureAudit(t)
	body := model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "app"}
	for name, mod := range map[string]func(*Deps){
		"no accessor": nil,
		"not running": func(d *Deps) { d.Cluster = func() (svc.Cluster, bool) { return nil, false } },
		"nil cluster": func(d *Deps) { d.Cluster = func() (svc.Cluster, bool) { return nil, true } },
	} {
		t.Run(name, func(t *testing.T) {
			w := req{method: "POST", path: "/api/actions/reconcile", body: body}.do(newServer(mod).Handler())
			if got := apiError(t, w); w.Code != http.StatusServiceUnavailable || got.Code != model.ErrCodeUpstreamUnavailable {
				t.Errorf("= %d %s, want 503 upstream_unavailable", w.Code, w.Body)
			}
		})
	}
	entries := auditEntries(t, buf)
	if len(entries) != 3 {
		t.Fatalf("audit entries = %d: %s", len(entries), buf.String())
	}
	for _, e := range entries {
		if e["action"] != "reconcile" || e["result"] != "error" {
			t.Errorf("entry = %v, want a failed reconcile", e)
		}
	}
}

func TestAuditLog(t *testing.T) {
	buf := captureAudit(t)
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "KustomizationList"},
		fluxObject(gvr, "Kustomization"))
	s := newServer(withCluster(dyn))
	h := s.Handler()

	req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "app"}}.do(h)
	req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "ghost"}}.do(h) // NotFound

	entries := auditEntries(t, buf)
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d: %s", len(entries), buf.String())
	}
	for i, want := range []map[string]any{
		{"user": "alice", "action": "reconcile", "kind": "Kustomization", "namespace": "flux-system", "name": "app", "result": "ok"},
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
