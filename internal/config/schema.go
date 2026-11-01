package config

import (
	"fmt"
	"reflect"
	"strings"
)

// The schema is read from Tree by reflection, once. Every non-struct field is a leaf: a scalar
// or a list that layers replace whole (design B.2 "Granularity"). Struct fields are sections.

// leaf is one settable field of the tree.
type leaf struct {
	path    string // dotted json names, e.g. "plugins.proxmox.url"
	index   []int  // reflect.Value.FieldByIndex path from Tree
	typ     reflect.Type
	secret  bool
	restart bool
	url     bool
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
}

var schema = buildSchema(reflect.TypeFor[Tree]())

func buildSchema(root reflect.Type) *schemaT {
	s := &schemaT{root: &node{}, byPath: map[string]*leaf{}}
	s.walk(s.root, root, nil, "")
	return s
}

func (s *schemaT) walk(n *node, t reflect.Type, index []int, prefix string) {
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			panic(fmt.Sprintf("config: %s.%s has no json name", t.Name(), f.Name))
		}
		path, idx := prefix+name, append(append([]int(nil), index...), i)
		child := &node{}
		if f.Type.Kind() == reflect.Struct {
			s.walk(child, f.Type, idx, path+".")
		} else {
			l := &leaf{path: path, index: idx, typ: f.Type}
			for _, opt := range strings.Split(f.Tag.Get("dash"), ",") {
				switch opt {
				case "secret":
					l.secret = true
				case "restart":
					l.restart = true
				case "url":
					l.url = true
				case "":
				default:
					panic(fmt.Sprintf("config: %s: unknown dash tag option %q", path, opt))
				}
			}
			child.leaf = l
			s.leaves = append(s.leaves, l)
			s.byPath[path] = l
		}
		n.names, n.children = append(n.names, name), append(n.children, child)
	}
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
