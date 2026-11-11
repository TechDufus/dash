package config

import "strings"

// FromUI reports whether an address of section ("plugins.proxmox") came from the UI layer: a
// leaf tagged url or dial under it whose effective value has provenance ui. A list leaf (the ssh
// plugin's hosts) has one provenance for the whole list, so one address saved from the UI makes
// the section FromUI. A client built for such a section refuses loopback too (design A.3,
// K.22), so that an address saved from the UI cannot reach dash's own listeners or its node. An
// address from the file or the environment is the operator's and keeps loopback.
func (r *Resolved) FromUI(section string) bool {
	prefix := section + "."
	for _, l := range schema.leaves {
		if (l.url || l.dial) && strings.HasPrefix(l.path, prefix) && r.Provenance[l.path].Source == SourceUI {
			return true
		}
	}
	return false
}
