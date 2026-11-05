package config

import (
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/secret"
)

// Patch is an RFC 7396 merge patch of the settings, flattened to the leaves it writes: the
// settings API (P4.7) classifies, validates and stores leaf by leaf. A list, or an object of a
// list, is one leaf (design B.2).
type Patch struct {
	Sets    map[string]any // leaf path → its new value
	Deletes []string       // leaf paths whose stored value goes ("reset to default"), sorted
}

// Paths lists every leaf the patch writes, sorted.
func (p Patch) Paths() []string {
	return slices.Sorted(func(yield func(string) bool) {
		for k := range p.Sets {
			if !yield(k) {
				return
			}
		}
		for _, k := range p.Deletes {
			if !yield(k) {
				return
			}
		}
	})
}

// FlattenPatch reads patch, a merge patch relative to the section at prefix ("" for the root,
// "plugins.proxmox" for a plugin), against the schema. A null at a leaf deletes its stored value;
// a null at a section deletes every value stored under it (stored is the store's nested map).
// Every key the schema does not have is a FieldError, path plus fixed text; nothing is written
// unless there is none. The paths of the patch are the schema's own strings, not built per
// request: the audit records that keep them share one copy (design S.21's 1 MiB bound).
func FlattenPatch(prefix string, patch map[string]any, stored map[string]any) (Patch, []FieldError) {
	p := Patch{Sets: map[string]any{}}
	n := schema.root
	if prefix != "" {
		for _, k := range strings.Split(prefix, ".") {
			if n = n.child(k); n == nil || n.leaf != nil {
				return p, []FieldError{{Path: prefix, Message: "not a section of the settings"}}
			}
		}
		prefix += "."
	}
	var errs []FieldError
	var walk func(n *node, prefix string, m map[string]any)
	walk = func(n *node, prefix string, m map[string]any) {
		for _, key := range slices.Sorted(maps.Keys(m)) {
			path, v := prefix+key, m[key]
			c := n.child(key)
			switch {
			case c == nil:
				errs = append(errs, FieldError{Path: path, Message: "unknown field"})
			case c.leaf != nil && v == nil:
				p.Deletes = append(p.Deletes, c.leaf.path)
			case c.leaf != nil:
				p.Sets[c.leaf.path] = v
			case v == nil:
				for _, l := range schema.leaves {
					if _, ok := StoredAt(stored, l.path); ok && strings.HasPrefix(l.path, path+".") {
						p.Deletes = append(p.Deletes, l.path)
					}
				}
			default:
				sub, ok := v.(map[string]any)
				if !ok {
					errs = append(errs, FieldError{Path: path, Message: "expected a section (a map of settings)"})
					continue
				}
				walk(c, path+".", sub)
			}
		}
	}
	walk(n, prefix, patch)
	slices.Sort(p.Deletes)
	p.Deletes = slices.Compact(p.Deletes)
	return p, errs
}

// HasReference reports whether v, a JSON value, holds a string anywhere that starts with
// env: or file: (after leading spaces). The UI layer never holds one (design S.14): it would let
// whoever edits settings read any variable or file of the dash process.
func HasReference(v any) bool {
	switch v := v.(type) {
	case string:
		s := strings.TrimSpace(v)
		return strings.HasPrefix(s, "env:") || strings.HasPrefix(s, "file:")
	case []any:
		return slices.ContainsFunc(v, HasReference)
	case map[string]any:
		for _, e := range v {
			if HasReference(e) {
				return true
			}
		}
	}
	return false
}

// UILayer is the store's nested map stored with the patch applied: what Candidate takes. stored
// is not modified. sets may hold replacement values for the patch's (encrypted secrets).
func UILayer(stored map[string]any, sets map[string]any, deletes []string) map[string]any {
	out := cloneJSON(stored).(map[string]any)
	for _, path := range deletes {
		deleteAt(out, strings.Split(path, "."))
	}
	for _, path := range slices.Sorted(maps.Keys(sets)) {
		setAt(out, strings.Split(path, "."), cloneJSON(sets[path]))
	}
	return out
}

func cloneJSON(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = cloneJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneJSON(e)
		}
		return out
	case nil:
		return map[string]any{}
	}
	return v
}

func setAt(m map[string]any, segs []string, v any) {
	for _, k := range segs[:len(segs)-1] {
		sub, ok := m[k].(map[string]any)
		if !ok {
			sub = map[string]any{}
			m[k] = sub
		}
		m = sub
	}
	m[segs[len(segs)-1]] = v
}

// deleteAt removes the key at segs and every map it leaves empty.
func deleteAt(m map[string]any, segs []string) {
	if len(segs) == 1 {
		delete(m, segs[0])
		return
	}
	sub, ok := m[segs[0]].(map[string]any)
	if !ok {
		return
	}
	deleteAt(sub, segs[1:])
	if len(sub) == 0 {
		delete(m, segs[0])
	}
}

// DestinationChanged reports whether next sends the requests of the section at prefix
// (plugins.<id>) somewhere else than cur does (design D16): the scheme, host or port of a URL
// leaf, or any value of a dest leaf, differs. The scheme counts: https to http on the same host
// and port would send a stored secret in clear text.
func DestinationChanged(cur, next *Resolved, section string) bool {
	for _, l := range schema.leaves {
		if !strings.HasPrefix(l.path, section+".") || !(l.url || l.dest) {
			continue
		}
		was, now := l.field(&cur.Tree), l.field(&next.Tree)
		if l.dest && !reflect.DeepEqual(was.Interface(), now.Interface()) {
			return true
		}
		if l.url && l.typ.Kind() == reflect.String && destination(was.String()) != destination(now.String()) {
			return true
		}
	}
	return false
}

// DestinationEdited reports whether next writes any url or dest leaf of the section at prefix
// (plugins.<id>) differently from cur, compared as values, not as destinations: a new scheme or
// another spelling of the same host counts. A patch that edits one may carry the section's
// secrets (design D16), which is what the form asks for on any edit of where requests go.
func DestinationEdited(cur, next *Resolved, section string) bool {
	for _, l := range schema.leaves {
		if strings.HasPrefix(l.path, section+".") && (l.url || l.dest) &&
			!reflect.DeepEqual(l.field(&cur.Tree).Interface(), l.field(&next.Tree).Interface()) {
			return true
		}
	}
	return false
}

// destination is where a URL sends a request: scheme://host:port, scheme and host lower-cased,
// with the scheme's default port made explicit. Anything that does not parse is compared whole.
func destination(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "\x00" + raw
	}
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// SetSecrets lists, in schema order, the secret leaves of the section (plugins.<id>) whose
// effective value in res is set or on its way (a pending reference).
func SetSecrets(res *Resolved, section string) []string {
	var out []string
	for _, l := range schema.leaves {
		if l.secret && strings.HasPrefix(l.path, section+".") && l.field(&res.Tree).Interface().(secret.Value).Present() {
			out = append(out, l.path)
		}
	}
	return out
}

// pluginIndexDepth is how many struct levels of Tree lead to a plugin section (Plugins, then the
// section): a leaf's index below them is its index inside the section struct.
const pluginIndexDepth = 2

// ReplaceSecretIn sets the secret leaf at path, under the plugin section whose configuration cfg
// is (a pointer to a copy of the section struct, PluginSection.Value), to v. The copy gets a
// Value of its own: the handle the running configuration shares is never updated (ruling 6).
func ReplaceSecretIn(cfg any, path string, v secret.Value) error {
	l := schema.byPath[path]
	if l == nil || !l.secret || !strings.HasPrefix(path, "plugins.") || len(l.index) <= pluginIndexDepth {
		return fmt.Errorf("%s: not a plugin secret", path)
	}
	sec := reflect.TypeFor[Tree]().FieldByIndex(l.index[:pluginIndexDepth]).Type
	rv := reflect.ValueOf(cfg)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Type() != sec {
		return fmt.Errorf("%s: the configuration is not its section's", path)
	}
	rv.Elem().FieldByIndex(l.index[pluginIndexDepth:]).Set(reflect.ValueOf(v))
	return nil
}
