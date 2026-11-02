package secret

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// The reasons a k8s: reference is not read yet. They are categories for logs: the row of the
// plugin that waits shows none of them (design K.3).
const (
	ReasonKubernetesUnavailable = "kubernetes_unavailable" // the kubernetes plugin is not running
	ReasonNotFound              = "not_found"              // the Secret does not exist (or was deleted)
	ReasonKeyMissing            = "key_missing"            // the Secret has no such key, or an empty one
	ReasonForbidden             = "forbidden"              // RBAC refuses the read
	ReasonUnreachable           = "unreachable"            // the API server does not answer
)

// K8sRef is a parsed k8s:<namespace>/<name>#<key> reference.
type K8sRef struct {
	Namespace, Name, Key string
	// Raw is the reference as written; it identifies the handle.
	Raw string
}

var (
	dns1123Label     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	secretKey        = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
)

// ParseK8sRef checks the syntax of a k8s:<namespace>/<name>#<key> reference: an RFC 1123 label
// for the namespace, an RFC 1123 subdomain for the Secret, and a key of [-._a-zA-Z0-9]+. The
// errors say what is wrong and never repeat the reference: a literal that merely starts with
// "k8s:" must not end up in an error.
func ParseK8sRef(raw string) (K8sRef, error) {
	rest, ok := strings.CutPrefix(raw, "k8s:")
	if !ok {
		return K8sRef{}, errors.New("not a k8s: reference")
	}
	obj, key, ok := strings.Cut(rest, "#")
	if !ok {
		return K8sRef{}, errors.New("k8s: reference must look like k8s:<namespace>/<name>#<key>")
	}
	ns, name, ok := strings.Cut(obj, "/")
	if !ok {
		return K8sRef{}, errors.New("k8s: reference must look like k8s:<namespace>/<name>#<key>")
	}
	switch {
	case len(ns) > 63 || !dns1123Label.MatchString(ns):
		return K8sRef{}, errors.New("k8s: reference namespace is not a valid Kubernetes namespace name")
	case len(name) > 253 || !dns1123Subdomain.MatchString(name):
		return K8sRef{}, errors.New("k8s: reference Secret name is not a valid Kubernetes object name")
	case len(key) > 253 || key == "." || key == ".." || !secretKey.MatchString(key):
		return K8sRef{}, errors.New("k8s: reference key may only hold letters, digits, '-', '_' and '.'")
	}
	return K8sRef{Namespace: ns, Name: name, Key: key, Raw: raw}, nil
}

// K8sRefs hands out one Value per k8s: reference for the life of the process. A Value made by
// Handle is a pending handle (a ref and no secret) that the kubernetes plugin fills, so the
// same holder reaches the supervisor on every resolution and a reload never sees a "changed"
// secret where only the reading progressed.
//
// Every handle is added to the scrub registry when it is made, so a value read later is
// scrubbed from that moment, also for a reference that only a reload introduced.
//
// Handles are never removed: a reference deleted by a reload keeps its holder (and the
// kubernetes plugin its watcher) until the process ends. That is bounded by the references the
// process has ever seen.
//
// Callbacks (OnChange, OnFill, Subscribe) run on the goroutine that caused them and must not
// block.
type K8sRefs struct {
	reg *Registry

	mu      sync.Mutex
	handles map[string]Value
	order   []K8sRef // first-seen order, for Refs and Subscribe
	subs    map[int]func(K8sRef)
	nextSub int
	changes []func()
	fills   []func()
}

// NewK8sRefs makes a registry of references. reg learns every handle; nil is fine.
func NewK8sRefs(reg *Registry) *K8sRefs {
	return &K8sRefs{reg: reg, handles: map[string]Value{}, subs: map[int]func(K8sRef){}}
}

// Handle returns the holder of a k8s: reference, the same one for the same reference on every
// call. A malformed reference is an error (see ParseK8sRef). A new handle starts pending with
// the reason ReasonKubernetesUnavailable, is announced to the subscribers and counts as a
// change for OnChange.
func (k *K8sRefs) Handle(ref string) (Value, error) {
	parsed, err := ParseK8sRef(ref)
	if err != nil {
		return Value{}, err
	}
	k.mu.Lock()
	if v, ok := k.handles[ref]; ok {
		k.mu.Unlock()
		return v, nil
	}
	v := New("", ref)
	v.setReason(ReasonKubernetesUnavailable)
	k.handles[ref] = v
	k.order = append(k.order, parsed)
	subs := k.subscribers()
	changes := append([]func(){}, k.changes...)
	k.mu.Unlock()

	if k.reg != nil {
		k.reg.Add(v)
	}
	for _, fn := range subs {
		fn(parsed)
	}
	for _, fn := range changes {
		fn()
	}
	return v, nil
}

// subscribers returns the subscribers in registration order. The caller holds k.mu.
func (k *K8sRefs) subscribers() []func(K8sRef) {
	out := make([]func(K8sRef), 0, len(k.subs))
	for id := range k.nextSub {
		if fn, ok := k.subs[id]; ok {
			out = append(out, fn)
		}
	}
	return out
}

// Refs lists every reference seen so far, in the order they first appeared.
func (k *K8sRefs) Refs() []K8sRef {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]K8sRef(nil), k.order...)
}

// Subscribe calls fn for every reference seen so far and then for each new one, and returns a
// function that ends the subscription. A reference may be delivered twice when it appears while
// Subscribe replays; fn must be idempotent.
func (k *K8sRefs) Subscribe(fn func(K8sRef)) (cancel func()) {
	k.mu.Lock()
	id := k.nextSub
	k.nextSub++
	k.subs[id] = fn
	existing := append([]K8sRef(nil), k.order...)
	k.mu.Unlock()
	for _, r := range existing {
		fn(r)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			k.mu.Lock()
			delete(k.subs, id)
			k.mu.Unlock()
		})
	}
}

// OnFill registers fn to run when a handle is filled for the first time.
func (k *K8sRefs) OnFill(fn func()) {
	k.mu.Lock()
	k.fills = append(k.fills, fn)
	k.mu.Unlock()
}

// OnChange registers fn to run when something a row can show changed: a handle's first fill,
// a change of a reason (a Secret that appeared, was refused or went) or a reference seen for
// the first time. Later updates of a value that is already there do not call it: consumers
// read the value per request. dash points it at the plugin supervisor's Reload, which
// coalesces the calls.
func (k *K8sRefs) OnChange(fn func()) {
	k.mu.Lock()
	k.changes = append(k.changes, fn)
	k.mu.Unlock()
}

// Fill puts the value the Secret holds into the handle of ref. The first fill and a fill that
// ends a reason call the callbacks. A reference no handle exists for and an empty value (the
// caller reports that as ReasonKeyMissing) are ignored.
func (k *K8sRefs) Fill(ref, value string) {
	k.mu.Lock()
	v, ok := k.handles[ref]
	if !ok || value == "" {
		k.mu.Unlock()
		return
	}
	first := !v.IsSet()
	v.Update(value)
	cleared := v.setReason("")
	var fills, changes []func()
	if first {
		fills = append(fills, k.fills...)
	}
	if first || cleared {
		changes = append(changes, k.changes...)
	}
	k.mu.Unlock()
	for _, fn := range fills {
		fn()
	}
	for _, fn := range changes {
		fn()
	}
}

// SetReason records why ref has not been read (or is no longer read): one of the Reason
// constants. The value stays as it is. A change calls the OnChange callbacks; an unknown
// reference is ignored.
func (k *K8sRefs) SetReason(ref, reason string) {
	k.mu.Lock()
	v, ok := k.handles[ref]
	if !ok || !v.setReason(reason) {
		k.mu.Unlock()
		return
	}
	changes := append([]func(){}, k.changes...)
	k.mu.Unlock()
	for _, fn := range changes {
		fn()
	}
}

// Reason returns the reason last recorded for ref, or "" when there is none or no such
// reference.
func (k *K8sRefs) Reason(ref string) string {
	k.mu.Lock()
	v, ok := k.handles[ref]
	k.mu.Unlock()
	if !ok {
		return ""
	}
	return v.Reason()
}

// String keeps the registry out of logs that print it whole.
func (k *K8sRefs) String() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return fmt.Sprintf("k8s references: %d", len(k.order))
}
