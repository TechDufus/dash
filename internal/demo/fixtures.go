package demo

import (
	"context"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
)

// demoLinks gives the fake homelab its Headlamp links, from the headlamp plugin pointed at the
// fake lab, as its demo twin is. The Proxmox links are the proxmox twin's.
type demoLinks struct {
	hl *headlamp.Plugin
}

var _ world.LinkSource = demoLinks{}

func newLinks() demoLinks {
	hl := &headlamp.Plugin{}
	// Start only records where Headlamp is: it takes no host and cannot fail for this section.
	_ = hl.Start(context.Background(), nil, &pluginconf.Headlamp{URL: world.HeadlampURL, Cluster: world.HeadlampCluster})
	return demoLinks{hl: hl}
}

// Headlamp links to an object in the fake lab's Headlamp.
func (d demoLinks) Headlamp(kind, namespace, name string) []model.Link {
	return d.hl.Links(plugin.LinkRequest{Kind: kind, Namespace: namespace, Name: name})
}
