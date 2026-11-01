package settings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Paths are dotted keys such as "plugins.proxmox.url". Config keys never contain a dot, so no
// escaping exists.
func splitPath(path string) ([]string, error) {
	segs := strings.Split(path, ".")
	for _, s := range segs {
		if s == "" {
			return nil, fmt.Errorf("settings: invalid path %q: empty segment", path)
		}
	}
	return segs, nil
}

func lookup(m map[string]any, segs []string) (any, bool) {
	var cur any = m
	for _, s := range segs {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[s]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// setIn writes v at segs, creating intermediate maps. It refuses to turn a scalar into a
// parent: that means the stored shape and the caller disagree, and silently replacing the
// scalar would drop a saved value.
func setIn(m map[string]any, segs []string, v any) error {
	for i, s := range segs[:len(segs)-1] {
		switch next := m[s].(type) {
		case nil:
			child := map[string]any{}
			m[s] = child
			m = child
		case map[string]any:
			m = next
		default:
			return fmt.Errorf("settings: cannot set %q: %q holds a value, not a section", strings.Join(segs, "."), strings.Join(segs[:i+1], "."))
		}
	}
	m[segs[len(segs)-1]] = v
	return nil
}

// deleteIn removes the key and prunes parents it leaves empty, so a reset to default leaves the
// store as sparse as if the key had never been saved. It reports whether anything was removed.
func deleteIn(m map[string]any, segs []string) bool {
	child, ok := m[segs[0]]
	if !ok {
		return false
	}
	if len(segs) == 1 {
		delete(m, segs[0])
		return true
	}
	cm, ok := child.(map[string]any)
	if !ok || !deleteIn(cm, segs[1:]) {
		return false
	}
	if len(cm) == 0 {
		delete(m, segs[0])
	}
	return true
}

func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = clone(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = clone(e)
		}
		return out
	default:
		return v
	}
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return clone(m).(map[string]any)
}

// normalize converts v to the shapes a YAML load produces (maps, slices, string, bool, float64),
// so what Get returns before a restart equals what it returns after, and callers cannot alias
// store memory.
func normalize(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("settings: value is not serialisable: %w", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
