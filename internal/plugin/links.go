package plugin

import (
	"fmt"

	"github.com/TechDufus/dash/internal/model"
)

// LinkRequest asks for the deep links to one object. Kind is the object's kind: a Kubernetes kind
// ("Pod", "Deployment", "Kustomization", …) or one of the dash-wide kinds "Alerts" (the alert
// list), "Host" (the hypervisor host), "VM" (a guest: ID is its VMID, Name its name),
// "HostShell" (the shell of a hypervisor node: Name is the node, empty for the node of the
// Hardware page's host) and "ProxmoxUI" (the hypervisor's own web UI).
// Namespace is empty for a cluster-scoped object. ID is the object's identity where a name does
// not identify it (a VMID); it is empty for the kinds that have none.
type LinkRequest struct {
	Kind      string
	Namespace string
	Name      string
	ID        string
}

// LinkProvider is a plugin that links objects into its own tool (Grafana, Headlamp, the launcher).
// Links returns nothing for a kind it does not know, and never nil is not required: the resolver
// normalises it.
type LinkProvider interface {
	Links(req LinkRequest) []model.Link
}

// LinkResolver collects the links of every running provider for an object. Resolve never
// returns nil. A provider that is not running, is disabled or is not configured contributes
// nothing, and a panicking one is skipped.
type LinkResolver interface {
	Resolve(req LinkRequest) []model.Link
}

// NoLinks is the LinkResolver of a process with no link providers.
var NoLinks LinkResolver = noLinks{}

type noLinks struct{}

func (noLinks) Resolve(LinkRequest) []model.Link { return []model.Link{} }

// linkSet is a snapshot of the running providers, in registry order.
type linkSet struct {
	s   *Supervisor
	set []linkEntry
}

type linkEntry struct {
	id ID
	p  LinkProvider
}

func (l linkSet) Resolve(req LinkRequest) []model.Link {
	out := []model.Link{}
	for _, e := range l.set {
		var ls []model.Link
		if rec, _ := protect(func() { ls = e.p.Links(req) }); rec != nil {
			l.s.log().Error("panic in Links", "plugin", e.id, "panic", fmt.Sprint(rec))
			continue
		}
		for _, link := range ls {
			if link.Kind == "" {
				link.Kind = model.LinkKind(e.id)
			}
			out = append(out, link)
		}
	}
	return out
}

// Links returns a resolver over the link providers that run now: the registry is read once, and
// the providers are called outside every supervisor lock, so a provider may call back into the
// supervisor. A caller asks once per round, not once per object.
func (s *Supervisor) Links() LinkResolver {
	var set []linkEntry
	for _, in := range s.registry() {
		in.mu.RLock()
		running, p := in.running, in.p
		in.mu.RUnlock()
		if !running || p == nil {
			continue
		}
		if lp, ok := p.(LinkProvider); ok {
			set = append(set, linkEntry{in.id, lp})
		}
	}
	if len(set) == 0 {
		return NoLinks
	}
	return linkSet{s, set}
}
