package model

import (
	"reflect"

	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// TopicTypes maps every topic dash itself publishes to the type of its payload (Envelope.Data).
// The OpenAPI generator and the SPA's type check read it. A plugin's own topics are not here:
// they come from its manifest (plugin.TopicSpec.Type).
func TopicTypes() map[Topic]reflect.Type {
	return map[Topic]reflect.Type{
		TopicPlugins:          reflect.TypeFor[PluginsData](),
		TopicOverview:         reflect.TypeFor[OverviewData](),
		TopicHost:             reflect.TypeFor[HostsData](),
		TopicVMs:              reflect.TypeFor[VMsData](),
		TopicProxmoxCluster:   reflect.TypeFor[pvemodel.PVEClusterData](),
		TopicProxmoxStorage:   reflect.TypeFor[pvemodel.PVEStorageData](),
		TopicHardwareMachines: reflect.TypeFor[hwmodel.MachinesData](),
		TopicCluster:          reflect.TypeFor[ClusterData](),
		TopicFlux:             reflect.TypeFor[FluxData](),
		TopicStorage:          reflect.TypeFor[StorageData](),
		TopicCNPGClusters:     reflect.TypeFor[CNPGClustersData](),
		TopicRustFSUsage:      reflect.TypeFor[RustFSUsageData](),
		TopicAlerts:           reflect.TypeFor[AlertsData](),
		TopicStatus:           reflect.TypeFor[StatusData](),
		TopicApps:             reflect.TypeFor[AppsData](),
		TopicLauncherLinks:    reflect.TypeFor[LauncherData](),
		TopicSSHHosts:         reflect.TypeFor[sshmodel.HostsData](),
		TopicApprovals:        reflect.TypeFor[ApprovalsData](),
	}
}
