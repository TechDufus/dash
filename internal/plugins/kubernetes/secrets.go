package kubernetes

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	kclient "k8s.io/client-go/kubernetes"

	"github.com/TechDufus/dash/internal/secret"
)

// Secret watchers: the half of k8s: secret references (P3.10) that reads the Secrets.
//
// There is no informer and no cache. For each distinct (namespace, name) one goroutine does a
// Get and then a Watch limited to that one object by field selector, started from the Get's
// resourceVersion (a 410 Gone starts over with a new Get). Only the referenced keys are copied,
// into their handles, and the object is dropped. The RBAC that makes this work is get, list and
// watch with resourceNames on the named Secrets in a namespaced Role (README): a list or watch
// restricted by resourceNames must carry the matching metadata.name field selector, and
// dash never asks for anything wider.

const (
	defaultMinBackoff = time.Second
	defaultMaxBackoff = 30 * time.Second
)

// errGone says the watch's resourceVersion is too old: read the object again.
var errGone = errors.New("resource version expired")

// secretWatchers reads the Secrets the k8s: references of a secret.K8sRefs name.
type secretWatchers struct {
	client kclient.Interface
	refs   *secret.K8sRefs
	log    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	minBackoff, maxBackoff time.Duration

	mu     sync.Mutex
	objs   map[objKey]*objWatch
	closed bool
}

type objKey struct{ namespace, name string }

// objWatch is one Secret and the keys of it that references name.
type objWatch struct {
	key    objKey
	resync chan struct{} // a key was added: read the object again

	mu         sync.Mutex
	keys       map[string]string // key -> the reference that asks for it
	lastReason string            // what the log last said about this object
}

// bindSecrets starts reading the Secrets that refs names, now and as references appear (a
// reload can add one), until ctx ends or stop is called. The kubernetes plugin calls it from
// Start with its client and stops it in Stop. Nil refs or client bind nothing and return nil,
// which is safe to stop.
//
// When the watchers end the values they read stay in their handles and the reason becomes
// kubernetes_unavailable: a consumer that already has its credential keeps working while the
// plugin restarts.
func bindSecrets(ctx context.Context, client kclient.Interface, refs *secret.K8sRefs, log *slog.Logger) *secretWatchers {
	if client == nil || refs == nil {
		return nil
	}
	w := newSecretWatchers(ctx, client, refs, log, defaultMinBackoff, defaultMaxBackoff)
	w.start()
	return w
}

func newSecretWatchers(ctx context.Context, client kclient.Interface, refs *secret.K8sRefs, log *slog.Logger, minBackoff, maxBackoff time.Duration) *secretWatchers {
	if log == nil {
		log = slog.Default()
	}
	w := &secretWatchers{
		client: client, refs: refs, log: log,
		minBackoff: minBackoff, maxBackoff: maxBackoff,
		objs: map[objKey]*objWatch{},
	}
	w.ctx, w.cancel = context.WithCancel(ctx)
	return w
}

// start subscribes to the references: a watcher per Secret starts for each one now and for each
// one a reload adds later.
func (w *secretWatchers) start() {
	context.AfterFunc(w.ctx, w.refs.Subscribe(w.add))
}

// stop ends every watcher and waits for them. It is safe on nil and to call twice.
func (w *secretWatchers) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.cancel()
	w.wg.Wait()
}

// add starts the watcher of the reference's Secret, or gives an existing one another key.
func (w *secretWatchers) add(r secret.K8sRef) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.ctx.Err() != nil {
		return
	}
	k := objKey{r.Namespace, r.Name}
	if o, ok := w.objs[k]; ok {
		o.addKey(r)
		return
	}
	o := &objWatch{key: k, resync: make(chan struct{}, 1), keys: map[string]string{r.Key: r.Raw}}
	w.objs[k] = o
	w.wg.Add(1)
	go w.run(o)
}

func (o *objWatch) addKey(r secret.K8sRef) {
	o.mu.Lock()
	_, known := o.keys[r.Key]
	o.keys[r.Key] = r.Raw
	o.mu.Unlock()
	if !known {
		select {
		case o.resync <- struct{}{}:
		default:
		}
	}
}

func (o *objWatch) snapshot() map[string]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string]string, len(o.keys))
	for k, v := range o.keys {
		out[k] = v
	}
	return out
}

// run keeps one Secret read until the watchers end.
func (w *secretWatchers) run(o *objWatch) {
	defer w.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("panic in a Secret watcher", "namespace", o.key.namespace, "name", o.key.name, "panic", r)
		}
		// The values stay; only the reason says the plugin no longer reads them.
		for _, ref := range o.snapshot() {
			w.refs.SetReason(ref, secret.ReasonKubernetesUnavailable)
		}
	}()

	backoff := w.minBackoff
	for w.ctx.Err() == nil {
		began := time.Now()
		rv, err := w.get(o)
		if err == nil {
			err = w.watch(o, rv)
		}
		switch {
		case w.ctx.Err() != nil:
			return
		case err == nil, errors.Is(err, errGone):
			backoff = w.minBackoff
			if time.Since(began) < w.minBackoff {
				w.sleep(w.minBackoff) // a server that closes at once must not be a hot loop
			}
		default:
			w.sleep(backoff)
			backoff = min(backoff*2, w.maxBackoff)
		}
	}
}

// sleep waits d or until the watchers end.
func (w *secretWatchers) sleep(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-w.ctx.Done():
	}
}

// get reads the Secret once, applies it and returns its resourceVersion. A failure sets the
// reason of every key.
func (w *secretWatchers) get(o *objWatch) (rv string, err error) {
	select { // a key added before this read is in it
	case <-o.resync:
	default:
	}
	sec, err := w.client.CoreV1().Secrets(o.key.namespace).Get(w.ctx, o.key.name, metav1.GetOptions{})
	if err != nil {
		w.fail(o, err)
		return "", err
	}
	rv = sec.ResourceVersion
	w.apply(o, sec)
	return rv, nil
}

// watch follows the one Secret from rv until the stream ends (nil), a key was added (nil), the
// version expired (errGone) or it fails.
func (w *secretWatchers) watch(o *objWatch, rv string) error {
	wi, err := w.client.CoreV1().Secrets(o.key.namespace).Watch(w.ctx, metav1.ListOptions{
		FieldSelector:       fields.OneTermEqualSelector("metadata.name", o.key.name).String(),
		ResourceVersion:     rv,
		AllowWatchBookmarks: true,
	})
	if err != nil {
		return w.watchErr(o, err)
	}
	defer wi.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return nil
		case <-o.resync:
			return nil
		case ev, ok := <-wi.ResultChan():
			if !ok {
				return nil
			}
			switch ev.Type {
			case watch.Added, watch.Modified:
				if sec, ok := ev.Object.(*corev1.Secret); ok {
					w.apply(o, sec)
				}
			case watch.Deleted:
				// The last value stays: a Secret that is recreated is read again, and a consumer
				// keeps its credential meanwhile.
				w.setReason(o, secret.ReasonNotFound, nil)
			case watch.Error:
				return w.watchErr(o, apierrors.FromObject(ev.Object))
			}
		}
	}
}

func (w *secretWatchers) watchErr(o *objWatch, err error) error {
	if apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
		return errGone
	}
	w.fail(o, err)
	return err
}

// apply copies the referenced keys of sec into their handles. sec is not kept.
func (w *secretWatchers) apply(o *objWatch, sec *corev1.Secret) {
	missing := false
	for key, ref := range o.snapshot() {
		if v := sec.Data[key]; len(v) > 0 {
			w.refs.Fill(ref, string(v))
		} else {
			missing = true
			w.refs.SetReason(ref, secret.ReasonKeyMissing)
		}
	}
	if missing {
		w.note(o, secret.ReasonKeyMissing, nil)
	} else {
		w.note(o, "", nil)
	}
}

// fail records why the Secret cannot be read.
func (w *secretWatchers) fail(o *objWatch, err error) {
	if w.ctx.Err() != nil {
		return // stopping: run's exit sets kubernetes_unavailable
	}
	reason := secret.ReasonUnreachable
	switch {
	case apierrors.IsNotFound(err):
		reason = secret.ReasonNotFound
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		reason = secret.ReasonForbidden
	}
	w.setReason(o, reason, err)
}

// setReason sets the reason of every key of o.
func (w *secretWatchers) setReason(o *objWatch, reason string, err error) {
	for _, ref := range o.snapshot() {
		w.refs.SetReason(ref, reason)
	}
	w.note(o, reason, err)
}

// note logs a change of what is wrong with o, once. The log carries the reason category and the
// Secret's name; never a value (there is none in scope here).
func (w *secretWatchers) note(o *objWatch, reason string, err error) {
	o.mu.Lock()
	changed := o.lastReason != reason
	o.lastReason = reason
	o.mu.Unlock()
	if !changed {
		return
	}
	if reason == "" {
		w.log.Info("a Kubernetes Secret is read", "namespace", o.key.namespace, "name", o.key.name)
		return
	}
	attrs := []any{"namespace", o.key.namespace, "name", o.key.name, "reason", reason}
	if err != nil && reason == secret.ReasonUnreachable {
		attrs = append(attrs, "error", err)
	}
	w.log.Warn("cannot read a Kubernetes Secret", attrs...)
}
