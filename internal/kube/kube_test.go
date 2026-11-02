package kube

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// writeKubeconfig writes a kubeconfig with two contexts, a and b, on different servers.
func writeKubeconfig(t *testing.T) string {
	t.Helper()
	body := `apiVersion: v1
kind: Config
current-context: a
clusters:
- name: ca
  cluster: {server: "https://192.0.2.10:6443"}
- name: cb
  cluster: {server: "https://192.0.2.11:6443"}
users:
- name: u
  user: {token: "x"}
contexts:
- name: a
  context: {cluster: ca, user: u}
- name: b
  context: {cluster: cb, user: u}
`
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigKubeconfigAndContext(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	path := writeKubeconfig(t)
	for _, tc := range []struct{ mode, ctx, host string }{
		{ModeKubeconfig, "", "https://192.0.2.10:6443"},
		{ModeKubeconfig, "b", "https://192.0.2.11:6443"},
		{ModeAuto, "", "https://192.0.2.10:6443"},
		{ModeAuto, "b", "https://192.0.2.11:6443"},
	} {
		rc, err := LoadConfig(tc.mode, path, tc.ctx, 20, 40)
		if err != nil || rc.Host != tc.host {
			t.Errorf("%s/%q: host %q, err %v, want %s", tc.mode, tc.ctx, rc.Host, err, tc.host)
		}
	}
	if _, err := LoadConfig(ModeKubeconfig, path, "nope", 20, 40); err == nil {
		t.Error("an unknown context loaded")
	}
}

func TestLoadConfigQPSAndBurstReachTheClient(t *testing.T) {
	path := writeKubeconfig(t)
	rc, err := LoadConfig(ModeKubeconfig, path, "", 7, 11)
	if err != nil || rc.QPS != 7 || rc.Burst != 11 {
		t.Fatalf("QPS %v, burst %v, err %v", rc.QPS, rc.Burst, err)
	}
}

func TestLoadConfigWithoutAClusterInEachMode(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if _, err := LoadConfig(ModeAuto, "", "", 20, 40); !errors.Is(err, ErrNoCluster) {
		t.Errorf("auto with nothing: %v, want ErrNoCluster", err)
	}
	if _, err := LoadConfig(ModeInCluster, "", "", 20, 40); err == nil || errors.Is(err, ErrNoCluster) {
		t.Errorf("inCluster outside a cluster: %v, want an error that is not ErrNoCluster", err)
	}
	if _, err := LoadConfig(ModeKubeconfig, "", "", 20, 40); err == nil || errors.Is(err, ErrNoCluster) {
		t.Errorf("kubeconfig mode without a path: %v", err)
	}
	if _, err := LoadConfig(ModeKubeconfig, filepath.Join(t.TempDir(), "missing"), "", 20, 40); err == nil {
		t.Error("a missing kubeconfig loaded")
	}
	if _, err := LoadConfig("remote", "", "", 20, 40); err == nil {
		t.Error("an unknown mode loaded")
	}
}

func TestLoadConfigInClusterReadsTheServiceAccount(t *testing.T) {
	// The in-cluster loader reads fixed paths under /var/run/secrets; outside a pod it fails after
	// the environment check, which is as far as a test can take it.
	t.Setenv("KUBERNETES_SERVICE_HOST", "192.0.2.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	_, err := LoadConfig(ModeInCluster, "", "", 20, 40)
	if err == nil || errors.Is(err, ErrNoCluster) {
		t.Skipf("a service account is mounted here: %v", err)
	}
}

func node(name string, ready bool) *corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}}}}
}

// The typed informers gate HasSynced, and the listers answer once they have.
func TestTypedInformersGateSync(t *testing.T) {
	c := NewFromClients(kubefake.NewClientset(node("n1", true)), dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), nil))
	if c.HasSynced() {
		t.Fatal("synced before Start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)
	waitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if !c.WaitForSync(waitCtx) || !c.HasSynced() {
		t.Fatal("never synced")
	}
	if got := c.ListNodes(); len(got) != 1 || got[0].Name != "n1" {
		t.Errorf("nodes = %v", got)
	}
	if err := c.SyncError(); err != nil {
		t.Errorf("SyncError = %v on a healthy cluster", err)
	}
}

// A typed informer whose list is refused never syncs, and SyncError says why: that is what the
// plugin turns into a category after its sync timeout.
func TestSyncErrorExplainsAStuckSync(t *testing.T) {
	kc := kubefake.NewClientset()
	kc.PrependReactor("list", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no"))
	})
	c := NewFromClients(kc, dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)
	waitFor(t, "the watch error", func() bool { return c.SyncError() != nil })
	if got := Category(c.SyncError()); got != "auth" {
		t.Errorf("category = %q (%v), want auth", got, c.SyncError())
	}
	if c.HasSynced() {
		t.Error("synced although pods cannot be listed")
	}
}

func TestCategory(t *testing.T) {
	timeout := &net.DNSError{IsTimeout: true}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p", errors.New("no")), "auth"},
		{apierrors.NewUnauthorized("no"), "auth"},
		{apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "p"), "http_404"},
		{apierrors.NewServiceUnavailable("down"), "http_503"},
		{fmt.Errorf("probe: %w", context.DeadlineExceeded), "timeout"},
		{timeout, "timeout"},
		{errors.New("connection refused"), "unreachable"},
	} {
		if got := Category(tc.err); got != tc.want {
			t.Errorf("Category(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestProbeAsksForTheVersion(t *testing.T) {
	kc := kubefake.NewClientset()
	if err := Probe(context.Background(), kc); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	kc.PrependReactor("get", "version", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	err := Probe(context.Background(), kc)
	if err == nil || Category(err) != "unreachable" {
		t.Errorf("Probe against a refusing server = %v", err)
	}
}

func TestInstanceOfIsTheNodeExporterAddress(t *testing.T) {
	if got := InstanceOf("192.0.2.11"); got != "192.0.2.11:9100" {
		t.Errorf("InstanceOf = %q", got)
	}
	if got := InstanceOf("2001:db8::1"); got != "[2001:db8::1]:9100" {
		t.Errorf("InstanceOf(v6) = %q", got)
	}
}

func owned(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: new(true)}}
}

func TestResolverFollowsTheOwnerChain(t *testing.T) {
	kc := kubefake.NewClientset(
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web-7d", OwnerReferences: owned("Deployment", "web")}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "nightly-1", OwnerReferences: owned("CronJob", "nightly")}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web-7d-x", OwnerReferences: owned("ReplicaSet", "web-7d")}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "nightly-1-x", OwnerReferences: owned("Job", "nightly-1")}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "bare"}},
	)
	c := NewFromClients(kc, dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)
	waitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if !c.WaitForSync(waitCtx) {
		t.Fatal("never synced")
	}
	res := c.Resolver()
	pod := func(name string) *corev1.Pod {
		p, err := c.Pods.Pods("app").Get(name)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := res.Chain(pod("web-7d-x")); len(got) != 2 || got[0] != (Owner{"ReplicaSet", "web-7d"}) || got[1] != (Owner{"Deployment", "web"}) {
		t.Errorf("deployment chain = %v", got)
	}
	if top, ok := res.Top(pod("nightly-1-x")); !ok || top != (Owner{"CronJob", "nightly"}) {
		t.Errorf("job top = %v, %v", top, ok)
	}
	if _, ok := res.Top(pod("bare")); ok {
		t.Error("a bare pod has an owner")
	}
}
