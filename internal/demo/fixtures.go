package demo

import (
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
)

// The links of the fake homelab come from the production builder, pointed at example.com by
// Config.
var _ world.LinkSource = (*links.Builder)(nil)

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
