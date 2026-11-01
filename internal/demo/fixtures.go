package demo

import (
	"context"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
)

// demoLinks gives the fake homelab its links: the Proxmox console from the production builder,
// pointed at example.com by Config, and the Headlamp links from the headlamp plugin pointed at
// the fake lab, as its demo twin is.
type demoLinks struct {
	*links.Builder
	hl *headlamp.Plugin
}

var _ world.LinkSource = demoLinks{}

func newLinks(cfg config.Config) demoLinks {
	hl := &headlamp.Plugin{}
	// Start only records where Headlamp is: it takes no host and cannot fail for this section.
	_ = hl.Start(context.Background(), nil, &pluginconf.Headlamp{URL: world.HeadlampURL, Cluster: world.HeadlampCluster})
	return demoLinks{Builder: links.New(cfg), hl: hl}
}

// Headlamp links to an object in the fake lab's Headlamp.
func (d demoLinks) Headlamp(kind, namespace, name string) []model.Link {
	return d.hl.Links(plugin.LinkRequest{Kind: kind, Namespace: namespace, Name: name})
}

// built is every topic payload at one instant, as the legacy publisher and the REST fixtures
// read them.
type built struct {
	overview model.OverviewData
	host     model.HostData
	vms      model.VMsData
	cluster  model.ClusterData
	flux     model.FluxData
	storage  model.StorageData
	alerts   model.AlertsData
	status   model.StatusData
	apps     model.AppsData
}

func (s *Source) build() built {
	w := s.world.Build()
	return built{
		overview: w.Overview, host: w.Host, vms: w.VMs, cluster: w.Cluster, flux: w.Flux,
		storage: w.Storage, alerts: w.Alerts, status: w.Status, apps: w.Apps,
	}
}
