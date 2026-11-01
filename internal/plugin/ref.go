package plugin

import (
	"regexp"
	"strings"
)

// kindRE is the Kind of a ref: a Go-style type name ("Pod", "VM", "Kustomization").
var kindRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// Ref builds the reference of an object (design L.6): "<plugin>:<Kind>/<id>", where id is the
// object's identity, joined with '/' when it has several parts (namespace and name). The prefix
// is the publishing plugin's instance id, which routes the ref to its owner. Clients treat a ref
// as opaque.
//
//	Ref("kubernetes", "Pod", "media", "app-0")  // kubernetes:Pod/media/app-0
//	Ref("proxmox", "VM", "101")                  // proxmox:VM/101
func Ref(plugin ID, kind string, id ...string) string {
	return string(plugin) + ":" + kind + "/" + strings.Join(id, "/")
}

// ParseRef splits a ref made by Ref. ok is false for anything else: a bad plugin id or kind, or
// an empty id. The id keeps its '/' separators.
func ParseRef(s string) (plugin ID, kind, id string, ok bool) {
	head, rest, found := strings.Cut(s, ":")
	if !found {
		return "", "", "", false
	}
	kind, id, found = strings.Cut(rest, "/")
	if !found || id == "" || !kindRE.MatchString(kind) || !ID(head).Valid() {
		return "", "", "", false
	}
	return ID(head), kind, id, true
}
