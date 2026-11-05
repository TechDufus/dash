package config

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/TechDufus/dash/internal/secret"
)

// secretType is the type of a credential leaf. The layers carry it as a string (a literal or an
// env:/file: reference); resolve turns it into a secret.Value once every layer is applied.
var secretType = reflect.TypeFor[secret.Value]()

// The schema is read from Tree by reflection, once. Every non-struct field is a leaf: a scalar
// or a list that layers replace whole (design B.2 "Granularity"). Struct fields are sections.

// leaf is one settable field of the tree.
type leaf struct {
	path    string // dotted json names, e.g. "plugins.proxmox.url"
	env     string // generated environment name, e.g. "DASH_PLUGINS_PROXMOX_URL"
	index   []int  // reflect.Value.FieldByIndex path from Tree
	typ     reflect.Type
	secret  bool
	restart bool
	url     bool
	// dest: the leaf decides where or how a request is sent (design D16): changing it while
	// the section has a set secret needs every set secret re-entered.
	dest bool
}

// field returns the leaf's value inside t.
func (l *leaf) field(t *Tree) reflect.Value { return reflect.ValueOf(t).Elem().FieldByIndex(l.index) }

// node is a section (children) or a leaf of the schema tree.
type node struct {
	children []*node
	names    []string // json name of each child, parallel to children
	leaf     *leaf
}

func (n *node) child(name string) *node {
	for i, c := range n.names {
		if c == name {
			return n.children[i]
		}
	}
	return nil
}

type schemaT struct {
	root   *node
	leaves []*leaf // depth-first, in declaration order
	byPath map[string]*leaf
	byEnv  map[string]*leaf // a name collision would leave fewer entries than leaves; a test checks
}

var schema = buildSchema(reflect.TypeFor[Tree]())

func buildSchema(root reflect.Type) *schemaT {
	s := &schemaT{root: &node{}, byPath: map[string]*leaf{}, byEnv: map[string]*leaf{}}
	s.walk(s.root, root, nil, "")
	return s
}

func (s *schemaT) walk(n *node, t reflect.Type, index []int, prefix string) {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			// An embedded struct without a json name is flattened, as encoding/json does:
			// its fields are settings of the section that embeds it (PluginBase).
			s.walk(n, f.Type, append(append([]int(nil), index...), i), prefix)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			panic(fmt.Sprintf("config: %s.%s has no json name", t.Name(), f.Name))
		}
		path, idx := prefix+name, append(append([]int(nil), index...), i)
		child := &node{}
		if f.Type.Kind() == reflect.Struct && f.Type != secretType {
			s.walk(child, f.Type, idx, path+".")
		} else {
			l := &leaf{path: path, env: envName(path), index: idx, typ: f.Type}
			for _, opt := range strings.Split(f.Tag.Get("dash"), ",") {
				switch opt {
				case "secret":
					l.secret = true
				case "restart":
					l.restart = true
				case "url":
					l.url = true
				case "dest":
					l.dest = true
				case "":
				case "required":
					// Only the JSON Schema reflector reads these (internal/jsonschema).
				default:
					if strings.HasPrefix(opt, "format=") || strings.HasPrefix(opt, "enum=") {
						continue
					}
					panic(fmt.Sprintf("config: %s: unknown dash tag option %q", path, opt))
				}
			}
			if (f.Type == secretType) != l.secret {
				panic(fmt.Sprintf("config: %s: the secret tag and the secret.Value type go together", path))
			}
			child.leaf = l
			s.leaves = append(s.leaves, l)
			s.byPath[path], s.byEnv[l.env] = l, l
		}
		n.names, n.children = append(n.names, name), append(n.children, child)
	}
}

// envName is the environment variable generated for a schema path (design B.3): DASH_ plus the
// path segments in UPPER_SNAKE, joined with "_". An acronym stays one word, plural or not:
// publicURL is PUBLIC_URL, tokenID is TOKEN_ID, criticalVMs is CRITICAL_VMS.
func envName(path string) string {
	var b strings.Builder
	b.WriteString("DASH")
	for seg := range strings.SplitSeq(path, ".") {
		b.WriteByte('_')
		r := []rune(seg)
		for i, c := range r {
			if i > 0 && unicode.IsUpper(c) && (!unicode.IsUpper(r[i-1]) || endsAcronym(r, i)) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToUpper(c))
		}
	}
	return b.String()
}

// endsAcronym reports whether the upper-case rune r[i], which follows another upper-case rune,
// starts a new word: it is followed by lower case (HTTPServer is HTTP_SERVER). The exception is
// a plural acronym: a lower-case "s" that ends the segment or is followed by upper case belongs
// to the acronym (VMs, IDs), so criticalVMs is CRITICAL_VMS and not CRITICAL_V_MS.
func endsAcronym(r []rune, i int) bool {
	if i+1 >= len(r) || !unicode.IsLower(r[i+1]) {
		return false
	}
	return !(r[i+1] == 's' && (i+2 == len(r) || unicode.IsUpper(r[i+2])))
}

// uiIneligible reports whether a path can never come from the UI layer (design B.2, S.14).
func uiIneligible(path string) bool {
	for _, p := range []string{"server", "auth", "settings.ui", "dataDir"} {
		if path == p || strings.HasPrefix(path, p+".") {
			return true
		}
	}
	return false
}
