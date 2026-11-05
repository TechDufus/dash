package world

import "github.com/TechDufus/dash/internal/model"

// Where the demo's link providers point: the fake lab's Grafana and Headlamp, on example.com.
const (
	GrafanaURL      = "https://grafana.example.com"
	HeadlampURL     = "https://headlamp.example.com"
	HeadlampCluster = "demo"
)

// HostModel is the model of the fake host, shown next to its name (HostData.Model).
const HostModel = "Example 2U server"

// Launcher is the launcher payload: five infrastructure links, all on example.com.
func Launcher() model.LauncherData {
	return model.LauncherData{Links: []model.LauncherLink{
		{Name: "Proxmox", Href: "https://pve.example.com:8006", Icon: "proxmox", Description: "Hypervisor"},
		{Name: "iDRAC", Href: "https://idrac.example.com", Icon: "idrac", Description: "Out-of-band"},
		{Name: "UniFi", Href: "https://unifi.example.com", Icon: "unifi", Description: "Network"},
		{Name: "NAS", Href: "https://nas.example.com", Icon: "unifi-drive", Description: "Backups"},
		{Name: "GitOps repo", Href: "https://git.example.com/homelab", Icon: "github", Description: "Flux source"},
	}}
}
