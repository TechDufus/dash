package kubernetes

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

// The watchers are tested against a fake clientset whose watches the test drives by hand: an
// event sent on a fake watcher is what the API server would have streamed.

const swCanary = "CANARY-secret-value-0123456789"

type swBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *swBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *swBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func swEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func swSecret(ns, name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, ResourceVersion: "7"}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

type swRig struct {
	t      *testing.T
	client *fake.Clientset
	reg    *secret.Registry
	refs   *secret.K8sRefs
	log    *swBuf
	w      *secretWatchers

	mu         sync.Mutex
	watches    []*watch.RaceFreeFakeWatcher
	selectors  []string // field selector of every watch and list the watchers made
	violations []string
	getErr     atomic.Pointer[error]
	fills      atomic.Int32
}

func newSwRig(t *testing.T, objs ...*corev1.Secret) *swRig {
	t.Helper()
	g := &swRig{t: t, reg: secret.NewRegistry(), log: &swBuf{}}
	g.refs = secret.NewK8sRefs(g.reg)
	g.refs.OnFill(func() { g.fills.Add(1) })
	objects := make([]kruntime.Object, len(objs))
	for i, o := range objs {
		objects[i] = o
	}
	g.client = fake.NewClientset(objects...)

	g.client.PrependReactor("list", "secrets", func(a k8stesting.Action) (bool, kruntime.Object, error) {
		g.checkSelector(a.(k8stesting.ListAction).GetListRestrictions().Fields.String(), "list")
		return false, nil, nil
	})
	g.client.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, kruntime.Object, error) {
		if e := g.getErr.Load(); e != nil {
			return true, nil, *e
		}
		return false, nil, nil
	})
	g.client.PrependWatchReactor("secrets", func(a k8stesting.Action) (bool, watch.Interface, error) {
		g.checkSelector(a.(k8stesting.WatchAction).GetWatchRestrictions().Fields.String(), "watch")
		fw := watch.NewRaceFreeFake()
		g.mu.Lock()
		g.watches = append(g.watches, fw)
		g.mu.Unlock()
		return true, fw, nil
	})
	return g
}

func (g *swRig) checkSelector(sel, verb string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.selectors = append(g.selectors, sel)
	if !strings.HasPrefix(sel, "metadata.name=") {
		g.violations = append(g.violations, verb+" on secrets without a metadata.name field selector: "+sel)
	}
}

// start runs watchers with a short backoff, stopped with the test.
func (g *swRig) start(ctx context.Context) *secretWatchers {
	g.t.Helper()
	g.w = newSecretWatchers(ctx, g.client, g.refs, slog.New(slog.NewTextHandler(g.log, nil)), 2*time.Millisecond, 10*time.Millisecond)
	g.w.start()
	g.t.Cleanup(g.w.stop)
	return g.w
}

func (g *swRig) handle(ref string) secret.Value {
	g.t.Helper()
	v, err := g.refs.Handle(ref)
	if err != nil {
		g.t.Fatal(err)
	}
	return v
}

// watcher waits for the n-th watch to be opened and returns it.
func (g *swRig) watcher(n int) *watch.RaceFreeFakeWatcher {
	g.t.Helper()
	swEventually(g.t, "a watch to be opened", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.watches) >= n })
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.watches[n-1]
}

func (g *swRig) gets() int {
	n := 0
	for _, a := range g.client.Actions() {
		if a.Matches("get", "secrets") {
			n++
		}
	}
	return n
}

func (g *swRig) checkNoViolations() {
	g.t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.violations) > 0 {
		g.t.Errorf("unfiltered requests: %v", g.violations)
	}
}

func reveals(v secret.Value, want string) func() bool {
	return func() bool { return v.Reveal() == want }
}

const refOne = "k8s:dash/creds#token"

func TestFakeClientsetResolvesAReferenceAndPicksUpAnUpdate(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": swCanary + "-1", "other": "unrelated"}))
	v := g.handle(refOne)
	consumer := v // a copy, as every plugin holds one: it sees the update without a restart
	g.start(t.Context())

	swEventually(t, "the first read", reveals(consumer, swCanary+"-1"))
	if v.Pending() || v.Reason() != "" {
		t.Errorf("a read handle is neither pending nor has a reason: pending=%v reason=%q", v.Pending(), v.Reason())
	}
	if g.fills.Load() != 1 {
		t.Errorf("first fills = %d", g.fills.Load())
	}
	g.watcher(1).Modify(swSecret("dash", "creds", map[string]string{"token": swCanary + "-2"}))
	swEventually(t, "the update", reveals(consumer, swCanary+"-2"))
	if g.fills.Load() != 1 {
		t.Error("an update is not a first fill: the consumer is not restarted")
	}
	g.checkNoViolations()
}

func TestSecretsAreNeverListedUnfiltered(t *testing.T) {
	g := newSwRig(t,
		swSecret("dash", "creds", map[string]string{"token": "a"}),
		swSecret("media", "other-creds", map[string]string{"pw": "b"}),
	)
	one, two := g.handle(refOne), g.handle("k8s:media/other-creds#pw")
	g.start(t.Context())
	swEventually(t, "both Secrets", func() bool { return one.IsSet() && two.IsSet() })
	g.watcher(2)

	g.mu.Lock()
	sels := append([]string(nil), g.selectors...)
	g.mu.Unlock()
	if len(sels) != 2 || !(sels[0] == "metadata.name=creds" && sels[1] == "metadata.name=other-creds" || sels[1] == "metadata.name=creds" && sels[0] == "metadata.name=other-creds") {
		t.Errorf("selectors = %v, want one watch per Secret limited to its name", sels)
	}
	for _, a := range g.client.Actions() {
		if a.Matches("list", "secrets") {
			t.Errorf("a list of secrets: %v", a)
		}
		if w, ok := a.(k8stesting.WatchAction); ok && w.GetWatchRestrictions().Fields.Empty() {
			t.Errorf("a watch without a field selector in %ss", w.GetNamespace())
		}
	}
	g.checkNoViolations()
}

// A watch is started from the Get's resourceVersion.
func TestWatchStartsFromTheGetsResourceVersion(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": "a"}))
	v := g.handle(refOne)
	var rv atomic.Value
	g.client.PrependWatchReactor("secrets", func(a k8stesting.Action) (bool, watch.Interface, error) {
		rv.Store(a.(k8stesting.WatchActionImpl).WatchRestrictions.ResourceVersion)
		return false, nil, nil
	})
	g.start(t.Context())
	swEventually(t, "the secret", v.IsSet)
	g.watcher(1)
	if got, _ := rv.Load().(string); got != "7" {
		t.Errorf("resourceVersion of the watch = %q, want the Get's", got)
	}
}

// The object is dropped after its keys were copied: nothing keeps the Secret (and the keys
// nobody asked for) in memory.
func TestSecretObjectsAreNotRetained(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": "a"}))
	v := g.handle(refOne)
	g.start(t.Context())
	swEventually(t, "the first read", v.IsSet)
	fw := g.watcher(1)

	send := func() weak.Pointer[corev1.Secret] {
		s := swSecret("dash", "creds", map[string]string{"token": swCanary, "unreferenced": "big-unrelated-value"})
		fw.Modify(s)
		return weak.Make(s)
	}
	wp := send()
	swEventually(t, "the event to be applied", reveals(v, swCanary))
	swEventually(t, "the Secret object to be collected", func() bool {
		goruntime.GC()
		return wp.Value() == nil
	})
}

func TestRotationUpdatesTheHolder(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": swCanary + "-old"}))
	v := g.handle(refOne)
	g.start(t.Context())
	swEventually(t, "the first read", reveals(v, swCanary+"-old"))

	g.watcher(1).Modify(swSecret("dash", "creds", map[string]string{"token": swCanary + "-new"}))
	swEventually(t, "the rotation", reveals(v, swCanary+"-new"))
	scrubbed := g.reg.Scrub("x " + swCanary + "-old y " + swCanary + "-new")
	if strings.Contains(scrubbed, swCanary) {
		t.Errorf("the new value and its predecessor must both be scrubbed: %q", scrubbed)
	}
}

func TestDeletedSecretKeepsTheLastValue(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": swCanary}))
	v := g.handle(refOne)
	g.start(t.Context())
	swEventually(t, "the first read", reveals(v, swCanary))
	fw := g.watcher(1)

	fw.Delete(swSecret("dash", "creds", nil))
	swEventually(t, "not_found", func() bool { return v.Reason() == secret.ReasonNotFound })
	if v.Reveal() != swCanary {
		t.Error("a deleted Secret must keep the last value")
	}
	fw.Add(swSecret("dash", "creds", map[string]string{"token": swCanary + "-again"}))
	swEventually(t, "the recreated Secret", func() bool { return v.Reveal() == swCanary+"-again" && v.Reason() == "" })
}

func TestKeyMissingThenPresent(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"another": "x"}))
	v := g.handle(refOne)
	g.start(t.Context())
	swEventually(t, "key_missing", func() bool { return v.Reason() == secret.ReasonKeyMissing })
	if v.IsSet() || !v.Pending() {
		t.Error("nothing was read")
	}
	g.watcher(1).Modify(swSecret("dash", "creds", map[string]string{"token": swCanary, "another": "x"}))
	swEventually(t, "the key to appear", func() bool { return v.Reveal() == swCanary && v.Reason() == "" })

	// An empty value is as good as none.
	g.watcher(1).Modify(swSecret("dash", "creds", map[string]string{"token": ""}))
	swEventually(t, "key_missing for an empty key", func() bool { return v.Reason() == secret.ReasonKeyMissing })
	if v.Reveal() != swCanary {
		t.Error("an emptied key keeps the last value")
	}
}

// A Secret made from a file usually ends in a newline: the value is trimmed like an env: or
// file: one, and a value that is nothing but whitespace is missing.
func TestSecretValuesAreTrimmed(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": " \t" + swCanary + "\r\n", "blank": " \n"}))
	v := g.handle(refOne)
	blank := g.handle("k8s:dash/creds#blank")
	g.start(t.Context())
	swEventually(t, "the read", v.IsSet)
	if got := v.Reveal(); got != swCanary {
		t.Errorf("value = %q, want it trimmed", got)
	}
	swEventually(t, "key_missing for a blank value", func() bool { return blank.Reason() == secret.ReasonKeyMissing })
	if blank.IsSet() {
		t.Error("a blank value must not be a credential")
	}

	g.watcher(1).Modify(swSecret("dash", "creds", map[string]string{"token": swCanary + "-2\n", "blank": "x"}))
	swEventually(t, "the trimmed rotation", reveals(v, swCanary+"-2"))
	swEventually(t, "the blank key to be read", reveals(blank, "x"))
}

func TestNotFoundThenCreated(t *testing.T) {
	g := newSwRig(t)
	v := g.handle(refOne)
	g.start(t.Context())
	swEventually(t, "not_found", func() bool { return v.Reason() == secret.ReasonNotFound })
	if _, err := g.client.CoreV1().Secrets("dash").Create(t.Context(), swSecret("dash", "creds", map[string]string{"token": swCanary}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	swEventually(t, "the retry to read it", reveals(v, swCanary))
	if v.Reason() != "" {
		t.Errorf("reason = %q", v.Reason())
	}
}

func TestForbiddenAndUnreachableAreReasonsAndRetried(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": swCanary}))
	v := g.handle(refOne)
	fail := func(err error) { g.getErr.Store(&err) }
	fail(apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "creds", errors.New("rbac says no")))
	g.start(t.Context())

	swEventually(t, "forbidden", func() bool { return v.Reason() == secret.ReasonForbidden })
	fail(errors.New("dial tcp 192.0.2.1:443: connect: connection refused"))
	swEventually(t, "unreachable", func() bool { return v.Reason() == secret.ReasonUnreachable })
	before := g.gets()
	swEventually(t, "retries with backoff", func() bool { return g.gets() > before+1 })

	g.getErr.Store(nil)
	swEventually(t, "recovery", func() bool { return v.Reveal() == swCanary && v.Reason() == "" })
	if v.Pending() {
		t.Error("a read handle is not pending")
	}
}

// A Gone watch (410, an old resourceVersion) starts over with a new Get and a new watch.
func TestGoneWatchStartsOverWithAGet(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": swCanary + "-1"}))
	v := g.handle(refOne)
	g.start(t.Context())
	swEventually(t, "the first read", reveals(v, swCanary+"-1"))
	first := g.watcher(1)
	gets := g.gets()

	// The Secret changed while the watch could not see it, then the server says Gone.
	if _, err := g.client.CoreV1().Secrets("dash").Update(t.Context(), swSecret("dash", "creds", map[string]string{"token": swCanary + "-2"}), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	first.Error(&metav1.Status{Status: metav1.StatusFailure, Code: 410, Reason: metav1.StatusReasonExpired, Message: "too old resource version"})
	swEventually(t, "a new Get to read the change", reveals(v, swCanary+"-2"))
	if g.gets() <= gets {
		t.Error("a Gone watch must read the object again")
	}
	g.watcher(2).Modify(swSecret("dash", "creds", map[string]string{"token": swCanary + "-3"}))
	swEventually(t, "the new watch", reveals(v, swCanary+"-3"))
	if v.Reason() != "" {
		t.Errorf("reason after a Gone watch = %q: it is not an error of the Secret", v.Reason())
	}
}

// A reference a reload adds reaches the running watchers through Subscribe: a new Secret gets
// a goroutine, another key of a watched one does not.
func TestAReferenceAddedLaterIsRead(t *testing.T) {
	g := newSwRig(t,
		swSecret("dash", "creds", map[string]string{"token": "a", "second": "b"}),
		swSecret("media", "app", map[string]string{"pw": "c"}),
	)
	first := g.handle(refOne)
	w := g.start(t.Context())
	swEventually(t, "the first read", first.IsSet)

	second := g.handle("k8s:dash/creds#second")
	other := g.handle("k8s:media/app#pw")
	swEventually(t, "both late references", func() bool { return second.Reveal() == "b" && other.Reveal() == "c" })
	w.mu.Lock()
	n := len(w.objs)
	w.mu.Unlock()
	if n != 2 {
		t.Errorf("watched Secrets = %d, want one watcher per distinct Secret", n)
	}
	if got := g.refs.Reason("k8s:dash/creds#second"); got != "" {
		t.Errorf("reason = %q", got)
	}
}

func TestStopKeepsTheLastValue(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": swCanary}))
	v := g.handle(refOne)
	w := g.start(t.Context())
	swEventually(t, "the first read", reveals(v, swCanary))

	w.stop()
	w.stop() // twice is fine
	if v.Reveal() != swCanary || !v.IsSet() {
		t.Error("stopping the plugin must not take the credential away")
	}
	if v.Reason() != secret.ReasonKubernetesUnavailable {
		t.Errorf("reason = %q, want kubernetes_unavailable", v.Reason())
	}
	// A restart of the plugin binds again and reads the Secret anew.
	w2 := newSecretWatchers(t.Context(), g.client, g.refs, slog.New(slog.DiscardHandler), 2*time.Millisecond, 10*time.Millisecond)
	w2.start()
	t.Cleanup(w2.stop)
	swEventually(t, "the reason to clear after the restart", func() bool { return v.Reason() == "" })
}

// stop waits for every watcher goroutine (its WaitGroup), so a stop that returns proves they
// ended; nothing counts the goroutines of the whole process.
func TestSecretWatchersEndEveryGoroutine(t *testing.T) {
	g := newSwRig(t,
		swSecret("dash", "creds", map[string]string{"token": "a"}),
		swSecret("media", "app", map[string]string{"pw": "b"}),
	)
	g.handle(refOne)
	g.handle("k8s:media/app#pw")
	w := g.start(t.Context())
	g.watcher(2) // one watch, hence one goroutine, per Secret

	stopped := make(chan struct{})
	go func() { w.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return: a watcher goroutine is still running")
	}

	// And no request reaches the API afterwards, not even a retry.
	n := len(g.client.Actions())
	time.Sleep(30 * time.Millisecond)
	if after := len(g.client.Actions()); after != n {
		t.Errorf("%d requests after stop", after-n)
	}
	// A reference seen after the stop starts nothing.
	w.mu.Lock()
	watched := len(w.objs)
	w.mu.Unlock()
	g.handle("k8s:late/ns#key")
	w.mu.Lock()
	now := len(w.objs)
	w.mu.Unlock()
	if now != watched {
		t.Errorf("a reference after stop started a watcher: %d -> %d", watched, now)
	}
}

func TestContextCancelEndsTheWatchersToo(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": swCanary}))
	v := g.handle(refOne)
	ctx, cancel := context.WithCancel(t.Context())
	w := g.start(ctx)
	swEventually(t, "the first read", v.IsSet)
	cancel()
	swEventually(t, "kubernetes_unavailable", func() bool { return v.Reason() == secret.ReasonKubernetesUnavailable })
	w.stop()
}

func TestBindSecretsNeedsClientAndRefs(t *testing.T) {
	if w := bindSecrets(t.Context(), nil, secret.NewK8sRefs(nil), nil); w != nil {
		t.Error("no client, no watchers")
	}
	if w := bindSecrets(t.Context(), fake.NewClientset(), nil, nil); w != nil {
		t.Error("no references, no watchers")
	}
	var none *secretWatchers
	none.stop() // the plugin's Stop calls it unconditionally
}

// The value never reaches a log line, whatever the Secret does.
func TestSecretValueNeverLeaks(t *testing.T) {
	g := newSwRig(t, swSecret("dash", "creds", map[string]string{"token": swCanary}))
	v := g.handle(refOne)
	g.start(t.Context())
	swEventually(t, "the first read", reveals(v, swCanary))
	fw := g.watcher(1)
	fw.Modify(swSecret("dash", "creds", map[string]string{"token": swCanary + "-2"}))
	swEventually(t, "the rotation", reveals(v, swCanary+"-2"))
	fw.Modify(swSecret("dash", "creds", nil))
	swEventually(t, "key_missing", func() bool { return v.Reason() == secret.ReasonKeyMissing })
	fw.Delete(swSecret("dash", "creds", map[string]string{"token": swCanary + "-3"}))
	swEventually(t, "not_found", func() bool { return v.Reason() == secret.ReasonNotFound })
	err := errors.New("upstream said " + swCanary)
	g.getErr.Store(&err)
	fw.Stop()
	swEventually(t, "unreachable", func() bool { return v.Reason() == secret.ReasonUnreachable })
	g.w.stop()

	log := g.log.String()
	if !strings.Contains(log, "reason=key_missing") || !strings.Contains(log, "reason=not_found") || !strings.Contains(log, "namespace=dash") {
		t.Errorf("the log should name the Secret and the reason:\n%s", log)
	}
	for _, leak := range []string{swCanary + "-2", swCanary + "-3"} {
		if strings.Contains(log, leak) {
			t.Errorf("the log shows a value (%s)", leak)
		}
	}
	// The error text of an unreachable API server is the one place foreign text enters the log;
	// the host scrubs it (Host.Logger), and the registry knows the value.
	if got := g.reg.Scrub(log); strings.Contains(got, swCanary) {
		t.Errorf("a registered value survives the scrub:\n%s", got)
	}
}

// The supervisor holds back any plugin with a pending k8s: reference, and the kubernetes plugin
// is the one that reads them: it must never have a secret field of its own, or it would wait
// for itself.
func TestKubernetesSectionHasNoSecretField(t *testing.T) {
	var walk func(path string, ty reflect.Type, seen map[reflect.Type]bool)
	walk = func(path string, ty reflect.Type, seen map[reflect.Type]bool) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Array || ty.Kind() == reflect.Map {
			ty = ty.Elem()
		}
		if ty == reflect.TypeFor[secret.Value]() {
			t.Errorf("%s is a secret.Value", path)
			return
		}
		if ty.Kind() != reflect.Struct || seen[ty] {
			return
		}
		seen[ty] = true
		for i := range ty.NumField() {
			walk(path+"."+ty.Field(i).Name, ty.Field(i).Type, seen)
		}
	}
	walk("plugins.kubernetes", reflect.TypeFor[pluginconf.Kubernetes](), map[reflect.Type]bool{})
}
