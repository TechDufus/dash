package config

import "strings"

// FromUI reports whether a URL of section ("plugins.proxmox") came from the UI layer: a leaf
// tagged url under it whose effective value has provenance ui. A client built for such a
// section refuses loopback too (design A.3, K.22), so that a URL saved from the UI cannot reach
// dash's own listeners or its node. A URL from the file or the environment is the operator's
// and keeps loopback.
func (r *Resolved) FromUI(section string) bool {
	prefix := section + "."
	for _, l := range schema.leaves {
		if l.url && strings.HasPrefix(l.path, prefix) && r.Provenance[l.path].Source == SourceUI {
			return true
		}
	}
	return false
}
