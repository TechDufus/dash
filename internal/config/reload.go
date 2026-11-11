package config

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// DefaultReloadInterval is how often the Reloader looks at its files (design B.6).
const DefaultReloadInterval = 10 * time.Second

// Change says that settings of one section changed on a reload: "plugins.<id>" for a plugin,
// the first path segment ("server", "settings", "dataDir") for the rest. The plugin supervisor
// reacts to the plugin ones; the Reloader itself applies only rotated secrets (below).
type Change struct {
	Section string
	Paths   []string // changed settings of the section, sorted
}

// ReloadOptions configure a Reloader. The zero value is production-ready.
type ReloadOptions struct {
	// Interval is the polling period (default DefaultReloadInterval).
	Interval time.Duration
	// Log receives the reloader's messages; nil means slog.Default() at the time of the message.
	Log *slog.Logger
	// Live reports whether a change to a section ("plugins.<id>") is applied in-process by
	// its subscriber, so that it is not listed as needing a restart. A setting tagged
	// restart always is. Nil means no section is live. The plugin supervisor's Live.
	Live func(section string) bool
}

// Reloader watches the config file and every file: secret reference (<NAME>_FILE ones too) of
// a running configuration and re-resolves the layers when one of them changes (design B.6, C.5).
// The environment cannot change in a running process and stays as it was at startup; the file
// and the settings store are read again.
//
// A reload that does not validate is logged once and changes nothing: the running
// configuration stays in force. A valid one
//   - updates every rotated secret in place (secret.Value.Update), so holders of the Value, such
//     as the Proxmox client, use the new credential at their next request, and the registry
//     keeps scrubbing the old one (a k8s: reference is no rotation of this kind: Sources.K8s
//     hands out one holder per reference, which the kubernetes plugin keeps filled, so a
//     reload sees a change only when a reference is added, removed or pointed elsewhere);
//   - records every other changed setting in RestartRequired and logs it once, unless its
//     section is live (ReloadOptions.Live): the plugin framework applies those in-process;
//   - emits one Change per affected section to the subscribers.
type Reloader struct {
	base     Sources // environment and keys as at startup; the file and the store are read again
	store    *settings.Store
	reg      *secret.Registry
	startup  *Resolved // what the process runs on
	interval time.Duration
	logger   *slog.Logger
	live     func(section string) bool

	// lock serialises a tick with WithLock's critical section. It is a channel rather than a
	// sync.Mutex so that a tick waiting on it is durably blocked for testing/synctest.
	lock chan struct{}

	// notifyMu serialises one reload with the notification of its changes, so that a Reload
	// outside WithLock and a tick never run the subscribers at the same time, and the
	// subscribers hear the changes of consecutive reloads in order. The lock order is lock,
	// notifyMu, mu; mu is never held while the subscribers run.
	notifyMu sync.Mutex

	mu       sync.Mutex // guards the fields below
	cur      *Resolved  // the last valid resolution, to tell what a reload changed; never nil
	watch    map[string]fileState
	revision uint64 // of the settings store at the last resolution
	lastErr  string // the invalid state already logged; "" when the last resolution was valid
	restart  []string
	subs     []func(Change)
	// file is the config file the last valid resolution applied (nil: none). held is a read of
	// the config file that was missing or empty while file had content, seen on one tick and
	// not put into effect yet (nil: none).
	file *File
	held *fileState
}

// NewReloader watches what res was resolved from. store is the settings store its UI layer
// came from, or nil. The caller runs it with Run.
func NewReloader(res *Resolved, store *settings.Store, opts ReloadOptions) *Reloader {
	r := &Reloader{
		base: res.src, store: store, reg: res.src.Registry, startup: res,
		interval: opts.Interval, logger: opts.Log, live: opts.Live, cur: res,
		lock: make(chan struct{}, 1), watch: map[string]fileState{},
		file: res.src.File,
	}
	r.base.File, r.base.UI, r.base.Registry = nil, nil, nil
	r.base.Log = slog.New(slog.DiscardHandler) // startup already said what it had to say
	if r.interval <= 0 {
		r.interval = DefaultReloadInterval
	}
	if store != nil {
		r.revision = store.Revision()
	}
	// Seeded unread, so the first poll re-resolves and compares with the startup tree: a file
	// that changed between the startup reads and this point is not lost.
	for _, p := range r.watchedPaths(&res.Tree) {
		r.watch[p] = fileState{}
	}
	return r
}

func (r *Reloader) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// Subscribe registers fn to receive every Change. It is called from the goroutine that runs the
// reload (Run's, or Reload's caller), after the new state is in place, and never concurrently
// with itself. It must not block, and must not call Reload or WithLock: either deadlocks. It
// may call Resolved, Tree and RestartRequired.
func (r *Reloader) Subscribe(fn func(Change)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subs = append(r.subs, fn)
}

// RestartRequired lists, sorted, the settings whose value in the files and the store now
// differs from the one the process started with and that need a restart to apply. A setting
// edited back to its startup value leaves the list.
func (r *Reloader) RestartRequired() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.restart)
}

// Tree is the last valid resolution: what a reload applied or noted, not necessarily what the
// process started with. A Change only names sections; the plugin supervisor reads the
// sections through this. It is always Resolved().Tree.
func (r *Reloader) Tree() Tree {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur.Tree
}

// Resolved is the last valid resolution with its provenance, never nil: the startup one until a
// reload puts another into effect. It is shared; callers must not modify it.
func (r *Reloader) Resolved() *Resolved {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

// WithLock runs fn while no tick can run, so that a sequence such as Candidate,
// settings.Store.Apply and Reload cannot interleave with a poll: a tick that comes due waits
// until fn returns. Inside fn call Reload, which does not take this lock, and never WithLock.
func (r *Reloader) WithLock(fn func() error) error {
	r.lock <- struct{}{}
	defer func() { <-r.lock }()
	return fn()
}

// Reload runs a check now instead of at the next tick: it reads the files and the store again,
// puts a valid resolution into effect and calls the subscribers before it returns. It fails
// only when the resolution does (an invalid environment, or a setting that does not validate);
// the running configuration then stays in force, as on a tick. A config file that reads
// missing, empty or not parsable while the applied one had content is not Reload's to put into
// effect: the reload keeps the applied file layer, so a settings write that lands while a
// writer has the file truncated still applies, and the ticks decide about the file (see poll).
// It serialises with a tick's reload and notification. It does not take the WithLock lock, so
// it can run inside WithLock; R6 calls it there. A subscriber must not call it: that deadlocks.
func (r *Reloader) Reload() error {
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()
	changes, subs, err := r.check(false)
	notify(changes, subs)
	return err
}

// Candidate resolves the configuration as it would be with ui as the settings store's values:
// the environment and keys as at startup, the config file as it is now. It changes nothing
// that runs: no secret is updated or registered, no new k8s: reference gets a handle, and
// neither Tree nor Resolved moves. Unlike a reload, a UI value that is not valid is an error
// naming its path ("plugins.gatus.url: expected a string, got number"), unless it equals what
// the store holds now: a stored key of another version stays a warning, so that it cannot
// refuse every later write.
func (r *Reloader) Candidate(ui map[string]any) (*Resolved, error) {
	src := r.base
	f, err := readConfigFile(src.Environ)
	if err != nil {
		return nil, err
	}
	src.File, src.UI, src.strictUI = f, ui, map[string]any{}
	if r.store != nil {
		src.UIRef, src.strictUI = r.store.Path(), r.store.Values()
	}
	return Resolve(src)
}

func (r *Reloader) isLive(section string) bool { return r.live != nil && r.live(section) }

// Run polls until ctx is done.
func (r *Reloader) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.poll()
		}
	}
}

// poll is one tick: look at the files and the store, reload if one changed or the last reload
// was invalid (the cause of an invalid state, such as a secret file that does not exist yet,
// may be outside the watched files). It waits while WithLock runs. A config file that has
// vanished or become empty since a reload applied it with content is put into effect only when
// the next tick reads it the same way: a writer that truncates the file in place (os.WriteFile,
// an editor that saves in place) or deletes and recreates it leaves that state for an instant.
// A truncated file that a writer has half filled again still parses or not as it happens: a
// valid prefix is applied like an edit, an invalid one is logged and keeps the running
// configuration.
func (r *Reloader) poll() {
	r.lock <- struct{}{}
	defer func() { <-r.lock }()
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()
	changes, subs, _ := r.check(true) // check logged the error
	notify(changes, subs)
}

func notify(changes []Change, subs []func(Change)) {
	for _, c := range changes {
		for _, fn := range subs {
			fn(c)
		}
	}
}

// check reloads if something changed, logging a failed resolution once per distinct error, and
// returns the changes for the subscribers or the resolution error. It reads the config file
// once: the torn decision, the change detection and the resolution all see the same bytes. On
// a tick (tick) a torn read waits for the next one (see poll); Reload resolves a torn or
// unparsable read with the file layer last applied instead (see Reload).
func (r *Reloader) check(tick bool) ([]Change, []func(Change), error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cfg, _ := configFilePath(r.base.Environ)
	f, ferr := readConfigFile(r.base.Environ)
	read := stateOf(f)
	if tick && hasContent(r.file) && torn(read) {
		if r.held == nil || *r.held != read {
			r.held = &read
			return nil, nil, nil
		}
	}
	if !tick && hasContent(r.file) && (ferr != nil || torn(read) || !parses(f)) {
		f, ferr, read = r.file, nil, stateOf(r.file) // keep the applied layer; held stays as it is
	} else {
		r.held = nil
	}

	changed := false
	for p, old := range r.watch {
		now := read
		if p != cfg {
			now = fingerprint(p)
		}
		changed = changed || now.ok != old.ok || now.sum != old.sum
		r.watch[p] = now
	}
	if r.store != nil && r.store.Revision() != r.revision {
		changed = true
	}
	if !changed && r.lastErr == "" {
		return nil, nil, nil
	}

	var rev uint64
	if r.store != nil {
		rev = r.store.Revision()
	}
	next, err := (*Resolved)(nil), ferr // a config file that cannot be read fails like an invalid one
	if err == nil {
		next, err = r.resolve(f)
	}
	if err != nil {
		if msg := err.Error(); msg != r.lastErr {
			r.log().Error("configuration reload failed; the running configuration stays in force", "error", msg)
			r.lastErr = msg
		}
		return nil, nil, err
	}
	r.lastErr, r.revision, r.file = "", rev, f

	changes := r.apply(next)
	want := r.watchedPaths(&next.Tree)
	for _, p := range want {
		if _, ok := r.watch[p]; !ok {
			r.watch[p] = fileState{} // read by this resolve; the next poll confirms it
		}
	}
	maps.DeleteFunc(r.watch, func(p string, _ fileState) bool { return !slices.Contains(want, p) })
	return changes, slices.Clone(r.subs), nil
}

// resolve layers the sources again: the environment and keys as at startup, the config file f
// (read once by check) and the settings store as it is now. Warnings of the layering are not
// repeated.
func (r *Reloader) resolve(f *File) (*Resolved, error) {
	src := r.base
	src.File = f
	if r.store != nil {
		src.UI, src.UIRef = r.store.Values(), r.store.Path()
	}
	return Resolve(src)
}

// apply puts a valid resolution into effect as far as that is possible and reports what
// changed since the previous valid one.
func (r *Reloader) apply(next *Resolved) []Change {
	var changed []string
	restart := map[string]bool{}
	for _, l := range schema.leaves {
		was, now := l.field(&r.cur.Tree), l.field(&next.Tree)
		start := l.field(&r.startup.Tree)
		if l.secret {
			ov, nv, running := was.Interface().(secret.Value), now.Interface().(secret.Value), start.Interface().(secret.Value)
			if running.Present() != nv.Present() && (l.restart || !r.isLive(sectionOf(l.path))) {
				// The plugin that reads it is on or off until restart, unless its section is
				// live: the supervisor then restarts the plugin in-process with the new value.
				restart[l.path] = true
			}
			same := ov.Reveal() == nv.Reveal()
			if same && (k8sRef(ov) || k8sRef(nv)) {
				// A k8s: handle is one per reference and its value is whatever the kubernetes
				// plugin has read, so equal values prove nothing: a pending handle has none
				// ("" == "" would hide a reference that a reload added, removed or pointed
				// elsewhere), and two filled handles can hold the same value.
				same = ov.Ref() == nv.Ref()
			}
			if same {
				continue
			}
			changed = append(changed, l.path)
			switch {
			case k8sRef(running) || k8sRef(nv):
				// A k8s: handle is filled by the kubernetes plugin and is the same one on every
				// resolution: copying a value into or out of it would overwrite what the
				// plugin read. A different reference is a different handle, which the plugin
				// framework picks up with the section's Change. A value that leaves a k8s:
				// handle is a new secret all the same: it must never be shown.
				if !k8sRef(nv) && nv.IsSet() && r.reg != nil {
					r.reg.Add(nv)
				}
			case running.IsSet() && nv.IsSet() && running.Update(nv.Reveal()):
				r.log().Info("secret reloaded", "path", l.path, "ref", nv.Ref())
			case nv.IsSet() && r.reg != nil:
				r.reg.Add(nv) // not in use before the restart, but never to be shown
			}
			continue
		}
		// A url or dial leaf of a plugin that keeps its value but moves into or out of the UI
		// layer is a change too: where an address comes from decides whether it may reach loopback.
		uiFlip := (l.url || l.dial) && strings.HasPrefix(l.path, "plugins.") &&
			(r.cur.Provenance[l.path].Source == SourceUI) != (next.Provenance[l.path].Source == SourceUI)
		if uiFlip || !reflect.DeepEqual(was.Interface(), now.Interface()) {
			changed = append(changed, l.path)
		}
		if !reflect.DeepEqual(start.Interface(), now.Interface()) && (l.restart || !r.isLive(sectionOf(l.path))) {
			restart[l.path] = true
		}
	}
	r.cur = next // the tree and its provenance together

	if added := slices.DeleteFunc(slices.Sorted(maps.Keys(restart)), func(p string) bool { return slices.Contains(r.restart, p) }); len(added) > 0 {
		r.log().Warn("configuration changed; these settings apply after a restart", "settings", added)
	}
	r.restart = slices.Sorted(maps.Keys(restart))

	bySection := map[string][]string{}
	for _, p := range changed {
		s := sectionOf(p)
		bySection[s] = append(bySection[s], p)
	}
	var out []Change
	for _, s := range slices.Sorted(maps.Keys(bySection)) {
		out = append(out, Change{Section: s, Paths: bySection[s]})
	}
	if len(out) > 0 {
		sections := make([]string, len(out))
		for i, c := range out {
			sections[i] = c.Section
		}
		r.log().Info("configuration reloaded", "sections", sections)
	}
	return out
}

// k8sRef reports whether v is a handle of a k8s: reference.
func k8sRef(v secret.Value) bool { return strings.HasPrefix(v.Ref(), "k8s:") }

// sectionOf names the section a setting belongs to: the plugin for plugins.<id>.*, otherwise
// the first path segment.
func sectionOf(path string) string {
	parts := strings.Split(path, ".")
	if parts[0] == "plugins" && len(parts) > 2 {
		return parts[0] + "." + parts[1]
	}
	return parts[0]
}

// watchedPaths are the files whose change can change the configuration: the config file (also
// when it does not exist yet) and every file: reference a secret of t was read from.
func (r *Reloader) watchedPaths(t *Tree) []string {
	p, _ := configFilePath(r.base.Environ)
	paths := []string{p}
	for _, l := range schema.leaves {
		if !l.secret {
			continue
		}
		if f, ok := strings.CutPrefix(l.field(t).Interface().(secret.Value).Ref(), "file:"); ok && !slices.Contains(paths, f) {
			paths = append(paths, f)
		}
	}
	return paths
}

// fileState identifies the content of a watched file by its SHA-256. Every poll hashes the
// file again: modification times tick coarsely (a same-size edit can keep the previous mtime),
// and the watched files are a config file and a few secret files, so reading them every
// interval is cheap. A ConfigMap update swaps a symlink to a new directory; the hash follows
// the content, so an identical swap is not a change.
type fileState struct {
	sum [sha256.Size]byte
	ok  bool // false: the file cannot be read (missing, mid-swap)
}

func fingerprint(path string) fileState {
	data, err := os.ReadFile(path) // follows symlinks
	if err != nil {
		return fileState{}
	}
	return fileState{sum: sha256.Sum256(data), ok: true}
}

// emptySum is the fingerprint of an empty file.
var emptySum = sha256.Sum256(nil)

// torn reports whether s is a config file that is missing or empty.
func torn(s fileState) bool { return !s.ok || s.sum == emptySum }

// stateOf is the fingerprint of a config file as readConfigFile returned it (nil: missing).
func stateOf(f *File) fileState {
	if f == nil {
		return fileState{}
	}
	return fileState{sum: sha256.Sum256(f.Data), ok: true}
}

// hasContent reports whether f is a config file that is there and not empty.
func hasContent(f *File) bool { return f != nil && len(f.Data) > 0 }

// parses reports whether f is YAML at all, valid settings or not: a file a writer has only
// partly written again usually ends mid-token and does not.
func parses(f *File) bool {
	if f == nil {
		return true
	}
	var doc map[string]any
	return yaml.UnmarshalStrict(f.Data, &doc) == nil
}
