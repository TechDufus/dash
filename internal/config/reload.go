package config

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// DefaultReloadInterval is how often the Reloader looks at its files (design B.6).
const DefaultReloadInterval = 10 * time.Second

// Change says that settings of one section changed on a reload: "plugins.<id>" for a plugin,
// the first path segment ("server", "settings", "dataDir") for the rest. The plugin framework
// (G2) reacts to these; until then the Reloader applies only rotated secrets (below).
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
//     keeps scrubbing the old one;
//   - records every other changed setting in RestartRequired and logs it once, because nothing
//     else applies live until the plugin framework (G2);
//   - emits one Change per affected section to the subscribers.
type Reloader struct {
	base     Sources // environment and keys as at startup; the file and the store are read again
	store    *settings.Store
	reg      *secret.Registry
	startup  *Resolved // what the process runs on
	interval time.Duration
	logger   *slog.Logger

	mu       sync.Mutex // guards the fields below
	prev     Tree       // the last valid resolution, to tell what a reload changed
	watch    map[string]fileState
	revision uint64 // of the settings store at the last resolution
	lastErr  string // the invalid state already logged; "" when the last resolution was valid
	restart  []string
	subs     []func(Change)
}

// NewReloader watches what res was resolved from. store is the settings store its UI layer
// came from, or nil. The caller runs it with Run.
func NewReloader(res *Resolved, store *settings.Store, opts ReloadOptions) *Reloader {
	r := &Reloader{
		base: res.src, store: store, reg: res.src.Registry, startup: res,
		interval: opts.Interval, logger: opts.Log, prev: res.Tree,
		watch: map[string]fileState{},
	}
	r.base.File, r.base.UI, r.base.Registry = nil, nil, nil
	r.base.Log = slog.New(slog.DiscardHandler) // startup already said what it had to say
	if r.interval <= 0 {
		r.interval = DefaultReloadInterval
	}
	if store != nil {
		r.revision = store.Revision()
	}
	for _, p := range r.watchedPaths(&res.Tree) {
		r.watch[p] = fingerprint(p, fileState{})
	}
	return r
}

func (r *Reloader) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// Subscribe registers fn to receive every Change. It is called from the reloader's goroutine,
// after the new state is in place, so it must not block.
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
// may be outside the watched files).
func (r *Reloader) poll() {
	changes, subs := r.check()
	for _, c := range changes {
		for _, fn := range subs {
			fn(c)
		}
	}
}

func (r *Reloader) check() ([]Change, []func(Change)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	changed := false
	for p, old := range r.watch {
		now := fingerprint(p, old)
		changed = changed || now.ok != old.ok || now.sum != old.sum
		r.watch[p] = now
	}
	if r.store != nil && r.store.Revision() != r.revision {
		changed = true
	}
	if !changed && r.lastErr == "" {
		return nil, nil
	}

	var rev uint64
	if r.store != nil {
		rev = r.store.Revision()
	}
	next, err := r.resolve()
	if err != nil {
		if msg := err.Error(); msg != r.lastErr {
			r.log().Error("configuration reload failed; the running configuration stays in force", "error", msg)
			r.lastErr = msg
		}
		return nil, nil
	}
	r.lastErr, r.revision = "", rev

	changes := r.apply(next)
	want := r.watchedPaths(&next.Tree)
	for _, p := range want {
		if _, ok := r.watch[p]; !ok {
			r.watch[p] = fingerprint(p, fileState{})
		}
	}
	maps.DeleteFunc(r.watch, func(p string, _ fileState) bool { return !slices.Contains(want, p) })
	return changes, slices.Clone(r.subs)
}

// resolve layers the sources again: the environment and keys as at startup, the file and the
// settings store as they are now. Warnings of the layering are not repeated.
func (r *Reloader) resolve() (*Resolved, error) {
	src := r.base
	f, err := readConfigFile(src.Environ)
	if err != nil {
		return nil, err
	}
	src.File = f
	if r.store != nil {
		src.UI, src.UIRef = r.store.Values(), r.store.Path()
	}
	res, err := Resolve(src)
	if err == nil {
		err = res.LoadLauncherFile()
	}
	return res, err
}

// apply puts a valid resolution into effect as far as that is possible and reports what
// changed since the previous valid one.
func (r *Reloader) apply(next *Resolved) []Change {
	var changed []string
	restart := map[string]bool{}
	for _, l := range schema.leaves {
		was, now := l.field(&r.prev), l.field(&next.Tree)
		start := l.field(&r.startup.Tree)
		if l.secret {
			ov, nv, running := was.Interface().(secret.Value), now.Interface().(secret.Value), start.Interface().(secret.Value)
			if running.IsSet() != nv.IsSet() {
				restart[l.path] = true // the plugin that reads it is on or off until restart
			}
			if ov.Reveal() == nv.Reveal() {
				continue
			}
			changed = append(changed, l.path)
			switch {
			case running.IsSet() && nv.IsSet() && running.Update(nv.Reveal()):
				r.log().Info("secret reloaded", "path", l.path, "ref", nv.Ref())
			case nv.IsSet() && r.reg != nil:
				r.reg.Add(nv) // not in use before the restart, but never to be shown
			}
			continue
		}
		if !reflect.DeepEqual(was.Interface(), now.Interface()) {
			changed = append(changed, l.path)
		}
		if !reflect.DeepEqual(start.Interface(), now.Interface()) {
			restart[l.path] = true
		}
	}
	r.prev = next.Tree

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

// fileState identifies the content of a watched file. The cheap part (real path after
// symlinks, modification time, size) says whether the content is worth hashing again; the
// SHA-256 says whether it changed. The real path is part of it because a ConfigMap update swaps
// a symlink to a new directory, which can leave size and time alike.
type fileState struct {
	real  string
	mtime time.Time
	size  int64
	sum   [sha256.Size]byte
	ok    bool // false: the file cannot be read (missing, mid-swap)
}

func fingerprint(path string, old fileState) fileState {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fileState{}
	}
	fi, err := os.Stat(path) // follows symlinks
	if err != nil {
		return fileState{}
	}
	if old.ok && old.real == real && old.mtime.Equal(fi.ModTime()) && old.size == fi.Size() {
		return old
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileState{}
	}
	return fileState{real: real, mtime: fi.ModTime(), size: fi.Size(), sum: sha256.Sum256(data), ok: true}
}
