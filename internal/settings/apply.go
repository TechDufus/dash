package settings

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Snapshot is what an Apply replaced: the previous value of every path it set or deleted, and
// the paths it scrubbed. Revert writes it back. The zero Snapshot reverts nothing.
type Snapshot struct {
	prior []prior
	scrub [][]string
}

type prior struct {
	path string
	segs []string
	v    any  // a deep copy of the previous value
	ok   bool // false: the path held nothing
}

// edit is one key of a multi-key write.
type edit struct {
	path string
	segs []string
	v    any // normalized; unused for a delete
	del  bool
}

// Apply writes several keys as one commit: the sets and deletes land together, with one file
// replacement and one revision bump, or not at all. Every path and value is checked before
// anything is written; a path both set and deleted, or two paths of which one holds the other
// ("a" and "a.b"), is refused. scrub lists the secret paths to remove from every retained
// .bak.N, as SetScrubbed does, also when the live file does not change. An Apply that changes
// nothing and scrubs nothing writes nothing.
//
// prev holds the previous values of the paths written, for Revert. A non-zero prev with an error
// means the write landed (the new file is in place and only a later step, the directory fsync,
// failed): Revert it to undo. With an error before the write prev is zero, and Revert of a zero
// Snapshot reverts nothing, so a caller may Revert(prev) after any error.
func (s *Store) Apply(sets map[string]any, deletes []string, scrub []string) (prev Snapshot, err error) {
	edits, err := prepare(sets, deletes)
	if err != nil {
		return Snapshot{}, err
	}
	scrubSegs, err := splitPaths(scrub)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return Snapshot{}, err
	}
	prev = Snapshot{scrub: scrubSegs}
	for _, e := range edits {
		v, ok := lookup(s.values, e.segs)
		prev.prior = append(prev.prior, prior{path: e.path, segs: e.segs, v: clone(v), ok: ok})
	}
	rev := s.rev
	if err := s.applyLocked(edits, scrubSegs); err != nil {
		if s.rev != rev {
			return prev, err // the write landed
		}
		return Snapshot{}, err
	}
	return prev, nil
}

// Revert writes back what prev recorded, as one new revision: every path the Apply set or
// deleted gets its previous value again, a path that held nothing is deleted, and the paths the
// Apply scrubbed are scrubbed again, so the reverted secret is left in no backup. It does not
// look at what was written in between; callers serialise their writes
// (config.Reloader.WithLock). As with Apply, an error after the new file is in place (the
// directory fsync) leaves the reverted values in the store: Revision tells whether it moved.
func (s *Store) Revert(prev Snapshot) error {
	edits := make([]edit, len(prev.prior))
	for i, p := range prev.prior {
		edits[i] = edit{path: p.path, segs: p.segs, v: clone(p.v), del: !p.ok}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	return s.applyLocked(edits, prev.scrub)
}

// applyLocked makes the edits on a copy of the values and commits it once. The edits do not
// overlap, so their order does not matter; a set that cannot be made (a stored scalar where it
// needs a section) fails before anything is written. The caller holds s.mu.
func (s *Store) applyLocked(edits []edit, scrub [][]string) error {
	next := cloneMap(s.values)
	for _, e := range edits {
		if e.del {
			deleteIn(next, e.segs)
		} else if err := setIn(next, e.segs, e.v); err != nil {
			return err
		}
	}
	switch {
	case !reflect.DeepEqual(next, s.values):
		return s.commit(next, scrub)
	case len(scrub) > 0:
		// The live file stays as it is, but a backup may still hold a retired value.
		return s.scrubBackups(scrub)
	}
	return nil
}

// prepare checks and normalizes every key of a multi-key write, sorted by path.
func prepare(sets map[string]any, deletes []string) ([]edit, error) {
	edits := make([]edit, 0, len(sets)+len(deletes))
	for path, v := range sets {
		segs, err := splitPath(path)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("settings: cannot store nil at %q; delete it instead", path)
		}
		nv, err := normalize(v)
		if err != nil {
			return nil, fmt.Errorf("%w (at %q)", err, path)
		}
		edits = append(edits, edit{path: path, segs: segs, v: nv})
	}
	for _, path := range deletes {
		segs, err := splitPath(path)
		if err != nil {
			return nil, err
		}
		edits = append(edits, edit{path: path, segs: segs, del: true})
	}
	slices.SortFunc(edits, func(a, b edit) int { return strings.Compare(a.path, b.path) })
	for i, a := range edits {
		for _, b := range edits[i+1:] {
			switch {
			case a.path == b.path && a.del != b.del:
				return nil, fmt.Errorf("settings: %q is both set and deleted", a.path)
			case a.path == b.path:
				return nil, fmt.Errorf("settings: %q is listed twice", a.path)
			case strings.HasPrefix(b.path, a.path+".") || strings.HasPrefix(a.path, b.path+"."):
				return nil, fmt.Errorf("settings: %q and %q overlap: one would overwrite the other", a.path, b.path)
			}
		}
	}
	return edits, nil
}

func splitPaths(paths []string) ([][]string, error) {
	out := make([][]string, 0, len(paths))
	for _, p := range paths {
		segs, err := splitPath(p)
		if err != nil {
			return nil, err
		}
		out = append(out, segs)
	}
	return out, nil
}
