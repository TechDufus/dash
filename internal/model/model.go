// Package model holds the JSON contract between the dash backend and the SPA.
// `make generate` turns it into web/src/api/types.ts (tygo); never edit that file by hand.
package model

import (
	"encoding/json"
	"time"
)

// Health of anything shown in the UI. The overall status is the worst member.
type Health string

const (
	HealthOK      Health = "ok"
	HealthWarn    Health = "warn"
	HealthCrit    Health = "crit"
	HealthUnknown Health = "unknown"
)

// Severity of an attention item.
type Severity string

const (
	SeverityWarn Severity = "warn"
	SeverityCrit Severity = "crit"
)

// LinkKind says where a deep link goes.
type LinkKind string

const (
	LinkKindHeadlamp LinkKind = "headlamp"
	LinkKindProxmox  LinkKind = "proxmox"
	LinkKindGrafana  LinkKind = "grafana"
	LinkKindApp      LinkKind = "app"
	LinkKindExternal LinkKind = "external"
)

// Topic is a name published on /api/v1/stream.
type Topic string

const (
	TopicOverview Topic = "core.overview"
	// TopicHost is the host's facts (HostData), published by the hardware plugin.
	TopicHost Topic = "hardware.hosts"
	TopicVMs  Topic = "proxmox.vms"
	// TopicProxmoxNode is the Proxmox node's facts (proxmox/model.NodeData), TopicProxmoxStorage
	// its storages (PVEStorageData): both published by the proxmox plugin.
	TopicProxmoxNode      Topic = "proxmox.node"
	TopicProxmoxStorage   Topic = "proxmox.storage"
	TopicHardwareMachines Topic = "hardware.machines"
	// TopicCluster, TopicStorage and TopicApps are the kubernetes plugin's topics (ClusterData,
	// StorageData, AppsData).
	TopicCluster Topic = "kubernetes.cluster"
	TopicFlux    Topic = "flux"
	TopicStorage Topic = "kubernetes.storage"
	// TopicAlerts is the alerts and silences (AlertsData), published by the alertmanager plugin.
	TopicAlerts Topic = "alertmanager.alerts"
	TopicStatus Topic = "gatus.status"
	TopicApps   Topic = "kubernetes.apps"
	// TopicLauncherLinks is the launcher's infrastructure links (LauncherData), published by the
	// launcher plugin.
	TopicLauncherLinks Topic = "launcher.links"
	// TopicPlugins is the status of every plugin instance (PluginsData), published by the
	// plugin supervisor. The core.* names belong to dash itself, not to a plugin.
	TopicPlugins Topic = "core.plugins"
)

type Link struct {
	Label string   `json:"label"`
	Href  string   `json:"href"`
	Kind  LinkKind `json:"kind" tstype:"string"`
}

// SourceStatus reports one upstream behind a topic; ok=false shows "source unavailable". Error is
// one category, never a URL or host name (the detail is in dash's log): unreachable, timeout,
// tls, auth, http_<code>, invalid_response or not_configured.
type SourceStatus struct {
	Name      string    `json:"name"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// Envelope is what every SSE event carries; Data is the topic's *Data struct.
type Envelope struct {
	Topic     string          `json:"topic"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Sources   []SourceStatus  `json:"sources"`
	Data      json.RawMessage `json:"data" tstype:"unknown"`
}

// ---- overview ----

type AttentionItem struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	// Source is the display label of the system the item came from; Plugin is the plugin that
	// contributed it.
	Source string    `json:"source"`
	Plugin string    `json:"plugin"`
	Title  string    `json:"title"`
	Detail string    `json:"detail"`
	Since  time.Time `json:"since"`
	// Route is an in-app path, e.g. /cluster/pods/<ns>/<name>.
	Route string `json:"route"`
	Links []Link `json:"links"`
	// Ref is the plugin:Kind/id reference of the object the item points at (design L.6); empty
	// for items of code that has not migrated to plugins yet.
	Ref string `json:"ref,omitempty"`
}

type Kpis struct {
	FluxReady      int  `json:"fluxReady"`
	FluxTotal      int  `json:"fluxTotal"`
	FluxSuspended  int  `json:"fluxSuspended"`
	SourceVerified bool `json:"sourceVerified"`
}

type TopologyHost struct {
	Name       string  `json:"name"`
	Health     Health  `json:"health"`
	PowerWatts float64 `json:"powerWatts"`
	CPUTempC   float64 `json:"cpuTempC"`
	// CPUTempHealth is the health of the hottest CPU package against the host's configured
	// temperature thresholds.
	CPUTempHealth Health  `json:"cpuTempHealth"`
	MemUsedRatio  float64 `json:"memUsedRatio"`
}

type TopologyVM struct {
	VMID      int     `json:"vmid"`
	Name      string  `json:"name"`
	Health    Health  `json:"health"`
	CPURatio  float64 `json:"cpuRatio"`
	MemRatio  float64 `json:"memRatio"`
	IsK8sNode bool    `json:"isK8sNode"`
	Running   bool    `json:"running"`
}

type TopologyNamespace struct {
	Name   string `json:"name"`
	Health Health `json:"health"`
	Pods   int    `json:"pods"`
}

// Group is Platform, Apps or Tenants.
type Group string

const (
	GroupPlatform Group = "Platform"
	GroupApps     Group = "Apps"
	GroupTenants  Group = "Tenants"
)

type TopologyGroup struct {
	Name       Group               `json:"name"`
	Health     Health              `json:"health"`
	Namespaces []TopologyNamespace `json:"namespaces"`
	// PodsByNode counts the group's pods per Kubernetes node name.
	PodsByNode map[string]int `json:"podsByNode"`
}

type Topology struct {
	Host   TopologyHost    `json:"host"`
	VMs    []TopologyVM    `json:"vms"`
	Groups []TopologyGroup `json:"groups"`
}

type OverviewData struct {
	Status    Health          `json:"status"`
	Attention []AttentionItem `json:"attention"`
	// Kpis is the fixed row of headline numbers the integrations that still run in the topic
	// manager fill; KPIList is what plugins contribute, sorted by plugin and key.
	Kpis     Kpis     `json:"kpis"`
	KPIList  []KPI    `json:"kpiList"`
	Topology Topology `json:"topology"`
}

// ---- host ----

type Sensor struct {
	Name   string  `json:"name"`
	ValueC float64 `json:"valueC"`
}

type Fan struct {
	Name string  `json:"name"`
	RPM  float64 `json:"rpm"`
}

type PSU struct {
	Name  string `json:"name"`
	State Health `json:"state"`
}

// SensorAliases are the texts the Inlet and Exhaust tiles match a sensor's name by, ignoring case.
type SensorAliases struct {
	Inlet   string `json:"inlet"`
	Exhaust string `json:"exhaust"`
}

type Pool struct {
	// Ref is hardware:Pool/<node>/<name> (design L.6).
	Ref             string     `json:"ref"`
	Name            string     `json:"name"`
	State           string     `json:"state"`
	LastScrubEnd    *time.Time `json:"lastScrubEnd,omitempty"`
	ScrubErrors     int        `json:"scrubErrors"`
	ScrubInProgress bool       `json:"scrubInProgress"`
}

type Disk struct {
	// Ref is hardware:Disk/<node>/<bay> (design L.6).
	Ref              string   `json:"ref"`
	Bay              string   `json:"bay"`
	Serial           string   `json:"serial"`
	Device           string   `json:"device"`
	Healthy          bool     `json:"healthy"`
	TempC            *float64 `json:"tempC,omitempty"`
	LifeRemainingPct *float64 `json:"lifeRemainingPct,omitempty"`
}

type HostData struct {
	// Ref is hardware:Host/<node> (design L.6).
	Ref string `json:"ref"`
	// Model is shown next to the host's name, e.g. "Dell R730"; empty shows nothing. The hardware
	// plugin fills it from plugins.hardware.hosts[0].model.
	Model string `json:"model"`
	// Bays is plugins.hardware.hosts[0].bays: the drive slots the Disks grid draws. 0 derives them:
	// the grid numbers its slots from 1 (from 0 when a disk sits in slot 0) up to the highest
	// populated one, at most 128; above that the page lists the disks sorted.
	Bays int `json:"bays"`
	// FanMaxRPM is plugins.hardware.hosts[0].fanMaxRPM: the top of the fan gauges, 0 to scale them
	// to the fastest fan. The wire key is fanMaxRpm (camel case); the config key is fanMaxRPM.
	FanMaxRPM int `json:"fanMaxRpm"`
	// SensorAliases name the sensors the Inlet and Exhaust tiles read.
	SensorAliases SensorAliases `json:"sensorAliases"`
	// TempWarnC and TempCritC are plugins.hardware.tempThresholds, in whole °C.
	TempWarnC int `json:"tempWarnC"`
	TempCritC int `json:"tempCritC"`
	// CPUTempHealth is the health of the hottest CPU package against those thresholds.
	CPUTempHealth Health    `json:"cpuTempHealth"`
	Name          string    `json:"name"`
	CPURatio      float64   `json:"cpuRatio"`
	MemUsedBytes  int64     `json:"memUsedBytes"`
	MemTotalBytes int64     `json:"memTotalBytes"`
	PowerWatts    float64   `json:"powerWatts"`
	PackageTempsC []float64 `json:"packageTempsC"`
	Sensors       []Sensor  `json:"sensors"`
	Fans          []Fan     `json:"fans"`
	PSUs          []PSU     `json:"psus"`
	SELFreeBytes  int64     `json:"selFreeBytes"`
	Pools         []Pool    `json:"pools"`
	// ScrubTracked is false when plugins.hardware.zfsScrubMetricPrefix is empty: no scrub series
	// is read, so the pools carry no scrub data and the UI shows none.
	ScrubTracked bool   `json:"scrubTracked"`
	Disks        []Disk `json:"disks"`
	Links        []Link `json:"links"`
}

// ---- vms ----

type K8sNodeRef struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}

type VM struct {
	// Ref is proxmox:VM/<vmid> (design L.6).
	Ref    string `json:"ref"`
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Health Health `json:"health"`
	// Critical is true when the VM is listed in plugins.proxmox.criticalVMs (by id or by tag): a
	// stopped critical VM is critical, any other stopped VM warns.
	Critical      bool        `json:"critical"`
	Tags          []string    `json:"tags"`
	CPUs          float64     `json:"cpus"`
	CPURatio      float64     `json:"cpuRatio"`
	MemBytes      int64       `json:"memBytes"`
	MaxMemBytes   int64       `json:"maxMemBytes"`
	UptimeSeconds int64       `json:"uptimeSeconds"`
	NetInBytes    int64       `json:"netInBytes"`
	NetOutBytes   int64       `json:"netOutBytes"`
	Template      bool        `json:"template"`
	Lock          string      `json:"lock"`
	K8sNode       *K8sNodeRef `json:"k8sNode,omitempty"`
	Links         []Link      `json:"links"`
}

type PVETask struct {
	UPID      string     `json:"upid"`
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	User      string     `json:"user"`
	Status    string     `json:"status"`
	StartTime time.Time  `json:"startTime"`
	EndTime   *time.Time `json:"endTime,omitempty"`
}

type VMsData struct {
	VMs   []VM      `json:"vms"`
	Tasks []PVETask `json:"tasks"`
}

type KeySpec struct {
	Key  string `json:"key"`
	Spec string `json:"spec"`
}

type VMConfig struct {
	Cores      int       `json:"cores"`
	Sockets    int       `json:"sockets"`
	MemoryMiB  int       `json:"memoryMiB"`
	BalloonMiB int       `json:"balloonMiB"`
	OSType     string    `json:"ostype"`
	Agent      string    `json:"agent"`
	Disks      []KeySpec `json:"disks"`
	NICs       []KeySpec `json:"nics"`
}

type IfaceAddr struct {
	Iface   string `json:"iface"`
	Address string `json:"address"`
}

type Snapshot struct {
	Name        string     `json:"name"`
	Time        *time.Time `json:"time,omitempty"`
	Description string     `json:"description"`
}

type VMDetail struct {
	VM        VM          `json:"vm"`
	Config    VMConfig    `json:"config"`
	IPs       []IfaceAddr `json:"ips"`
	Snapshots []Snapshot  `json:"snapshots"`
}

// ---- cluster ----

type Node struct {
	// Ref is kubernetes:Node/<name> (design L.6).
	Ref            string  `json:"ref"`
	Name           string  `json:"name"`
	IP             string  `json:"ip"`
	Ready          bool    `json:"ready"`
	KubeletVersion string  `json:"kubeletVersion"`
	CPURatio       float64 `json:"cpuRatio"`
	MemRatio       float64 `json:"memRatio"`
	Pods           int     `json:"pods"`
	Links          []Link  `json:"links"`
}

type Namespace struct {
	// Ref is kubernetes:Namespace/<name> (design L.6).
	Ref               string `json:"ref"`
	Name              string `json:"name"`
	Group             Group  `json:"group"`
	Protected         bool   `json:"protected"`
	Tenant            string `json:"tenant"`
	Privileged        bool   `json:"privileged"`
	PodsRunning       int    `json:"podsRunning"`
	PodsTotal         int    `json:"podsTotal"`
	Restarts          int    `json:"restarts"`
	WorkloadsNotReady int    `json:"workloadsNotReady"`
	Health            Health `json:"health"`
}

// WorkloadKind is the kind of a workload controller.
type WorkloadKind string

const (
	WorkloadKindDeployment  WorkloadKind = "Deployment"
	WorkloadKindStatefulSet WorkloadKind = "StatefulSet"
	WorkloadKindDaemonSet   WorkloadKind = "DaemonSet"
	WorkloadKindJob         WorkloadKind = "Job"
	WorkloadKindCronJob     WorkloadKind = "CronJob"
)

// FluxKind is the kind of a Flux object dash shows and may reconcile.
type FluxKind string

const (
	FluxKindKustomization  FluxKind = "Kustomization"
	FluxKindHelmRelease    FluxKind = "HelmRelease"
	FluxKindGitRepository  FluxKind = "GitRepository"
	FluxKindOCIRepository  FluxKind = "OCIRepository"
	FluxKindHelmRepository FluxKind = "HelmRepository"
)

type Workload struct {
	// Ref is kubernetes:Workload/<Kind>/<ns>/<name> (design L.6).
	Ref       string       `json:"ref"`
	Kind      WorkloadKind `json:"kind"`
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	Ready     int          `json:"ready"`
	Desired   int          `json:"desired"`
	Images    []string     `json:"images"`
	Health    Health       `json:"health"`
	// Restartable is true for Deployments, StatefulSets and DaemonSets.
	Restartable bool   `json:"restartable"`
	Protected   bool   `json:"protected"`
	Links       []Link `json:"links"`
}

type OwnerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Pod struct {
	// Ref is kubernetes:Pod/<ns>/<name> (design L.6).
	Ref             string    `json:"ref"`
	Namespace       string    `json:"namespace"`
	Name            string    `json:"name"`
	Phase           string    `json:"phase"`
	Reason          string    `json:"reason"`
	ReadyContainers int       `json:"readyContainers"`
	TotalContainers int       `json:"totalContainers"`
	Restarts        int       `json:"restarts"`
	Node            string    `json:"node"`
	CreatedAt       time.Time `json:"createdAt"`
	CPUCores        *float64  `json:"cpuCores,omitempty"`
	MemBytes        *float64  `json:"memBytes,omitempty"`
	// Owner is the top of the owner chain (a Deployment rather than its ReplicaSet).
	Owner  *OwnerRef `json:"owner,omitempty"`
	Health Health    `json:"health"`
	Links  []Link    `json:"links"`
}

type Event struct {
	Namespace string    `json:"namespace"`
	Object    string    `json:"object"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Count     int       `json:"count"`
	LastSeen  time.Time `json:"lastSeen"`
}

type Cert struct {
	// Ref is kubernetes:Certificate/<ns>/<name> (design L.6).
	Ref       string     `json:"ref"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Ready     bool       `json:"ready"`
	NotAfter  *time.Time `json:"notAfter,omitempty"`
	Links     []Link     `json:"links"`
}

type ExtSecret struct {
	// Ref is kubernetes:ExternalSecret/<ns>/<name> (design L.6).
	Ref       string `json:"ref"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`
	Message   string `json:"message"`
}

type ClusterData struct {
	Nodes           []Node      `json:"nodes"`
	Namespaces      []Namespace `json:"namespaces"`
	Workloads       []Workload  `json:"workloads"`
	Pods            []Pod       `json:"pods"`
	Events          []Event     `json:"events"`
	Certificates    []Cert      `json:"certificates"`
	ExternalSecrets []ExtSecret `json:"externalSecrets"`
}

type Condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type Container struct {
	Name                  string `json:"name"`
	Image                 string `json:"image"`
	State                 string `json:"state"`
	Ready                 bool   `json:"ready"`
	Restarts              int    `json:"restarts"`
	LastTerminationReason string `json:"lastTerminationReason"`
}

type PodDetail struct {
	Pod        Pod         `json:"pod"`
	IPs        []string    `json:"ips"`
	Conditions []Condition `json:"conditions"`
	Containers []Container `json:"containers"`
	// OwnerChain runs from the direct owner up (ReplicaSet, then Deployment).
	OwnerChain []OwnerRef `json:"ownerChain"`
	Events     []Event    `json:"events"`
}

// ---- flux ----

type FluxObj struct {
	Kind           FluxKind   `json:"kind"`
	Namespace      string     `json:"namespace"`
	Name           string     `json:"name"`
	Ready          bool       `json:"ready"`
	Suspended      bool       `json:"suspended"`
	SuspendReason  string     `json:"suspendReason"`
	Revision       string     `json:"revision"`
	Message        string     `json:"message"`
	LastTransition *time.Time `json:"lastTransition,omitempty"`
	// Verified is the SourceVerified condition (sources only; nil when absent).
	Verified *bool  `json:"verified,omitempty"`
	Links    []Link `json:"links"`
}

type FluxData struct {
	Sources        []FluxObj `json:"sources"`
	Kustomizations []FluxObj `json:"kustomizations"`
	HelmReleases   []FluxObj `json:"helmReleases"`
}

// ---- storage ----

type PVC struct {
	// Ref is kubernetes:PVC/<ns>/<name> (design L.6).
	Ref               string   `json:"ref"`
	Namespace         string   `json:"namespace"`
	Name              string   `json:"name"`
	StorageClass      string   `json:"storageClass"`
	Phase             string   `json:"phase"`
	CapacityBytes     int64    `json:"capacityBytes"`
	UsedBytes         *int64   `json:"usedBytes,omitempty"`
	UsedRatio         *float64 `json:"usedRatio,omitempty"`
	GrowthBytesPerDay *float64 `json:"growthBytesPerDay,omitempty"`
	Health            Health   `json:"health"`
	Links             []Link   `json:"links"`
}

type Bucket struct {
	Name      string `json:"name"`
	UsedBytes int64  `json:"usedBytes"`
	// QuotaBytes 0 means no quota.
	QuotaBytes int64  `json:"quotaBytes"`
	Health     Health `json:"health"`
}

type UsedSize struct {
	UsedBytes   int64 `json:"usedBytes"`
	UsableBytes int64 `json:"usableBytes"`
}

type CNPGCluster struct {
	Namespace            string   `json:"namespace"`
	Name                 string   `json:"name"`
	Instances            int      `json:"instances"`
	ReadyInstances       int      `json:"readyInstances"`
	Phase                string   `json:"phase"`
	LastBackupAgeSeconds *float64 `json:"lastBackupAgeSeconds,omitempty"`
	Links                []Link   `json:"links"`
}

// BackupJob is the age of the last successful run of a backup CronJob.
type BackupJob struct {
	// Ref is kubernetes:BackupJob/<ns>/<name> (design L.6).
	Ref                   string   `json:"ref"`
	Name                  string   `json:"name"`
	LastSuccessAgeSeconds *float64 `json:"lastSuccessAgeSeconds,omitempty"`
}

type Etcd struct {
	DBBytes    int64 `json:"dbBytes"`
	QuotaBytes int64 `json:"quotaBytes"`
}

type StorageData struct {
	PVCs       []PVC         `json:"pvcs"`
	Buckets    []Bucket      `json:"buckets"`
	RustFS     UsedSize      `json:"rustfs"`
	CNPG       []CNPGCluster `json:"cnpg"`
	BackupJobs []BackupJob   `json:"backupJobs"`
	Etcd       Etcd          `json:"etcd"`
}

// ---- alerts ----

type Alert struct {
	Ref string `json:"ref"` // alertmanager:Alert/<fingerprint>
	// Protected is set when the alert's name is in plugins.alertmanager.protectedAlerts: it cannot
	// be silenced through dash.
	Protected   bool   `json:"protected"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
	Severity    string `json:"severity"`
	// State is active, suppressed or unprocessed (Alertmanager status.state).
	State       string            `json:"state"`
	Summary     string            `json:"summary"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels"`
	StartsAt    time.Time         `json:"startsAt"`
	SilencedBy  []string          `json:"silencedBy"`
	InhibitedBy []string          `json:"inhibitedBy"`
}

type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

type Silence struct {
	Ref string `json:"ref"` // alertmanager:Silence/<id>
	// Dash is set when dash created the silence (createdBy starts with "dash:"): only those can
	// be expired through dash.
	Dash      bool      `json:"dash"`
	ID        string    `json:"id"`
	Matchers  []Matcher `json:"matchers"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	State     string    `json:"state"`
}

type AlertsData struct {
	Alerts   []Alert   `json:"alerts"`
	Silences []Silence `json:"silences"`
	// SilenceDurations are the durations the silence dialog offers (plugins.alertmanager.
	// silenceDurations).
	SilenceDurations []string `json:"silenceDurations,omitempty"`
}

// ---- status (Gatus) ----

type CheckResult struct {
	Success    bool      `json:"success"`
	Timestamp  time.Time `json:"timestamp"`
	DurationMs float64   `json:"durationMs"`
}

type Endpoint struct {
	Ref     string        `json:"ref"` // gatus:Endpoint/<Key>
	Group   string        `json:"group"`
	Name    string        `json:"name"`
	Key     string        `json:"key"`
	URL     string        `json:"url"`
	Healthy bool          `json:"healthy"`
	Results []CheckResult `json:"results"`
}

type StatusData struct {
	Endpoints []Endpoint `json:"endpoints"`
	// CriticalGroups are the groups whose failing checks are critical and that the Status page
	// lists first (plugins.gatus.criticalGroups).
	CriticalGroups []string `json:"criticalGroups,omitempty"`
}

// ---- apps ----

type App struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IconURL     string `json:"iconUrl"`
	// Icon is the raw icon token of the app (a dashboard-icons name, a same-origin path or a URL);
	// the SPA resolves it, and it replaces IconURL (design D9).
	Icon string `json:"icon"`
	// Ref is kubernetes:App/<ns>/<source>/<resource>/<host> (design L.6).
	Ref       string `json:"ref"`
	Href      string `json:"href"`
	Group     Group  `json:"group"`
	Namespace string `json:"namespace"`
	// Healthy comes from the Gatus endpoint for the hostname; nil when none matches.
	Healthy *bool `json:"healthy,omitempty"`
}

type AppsData struct {
	Apps []App `json:"apps"`
}

// ---- REST ----

type SeriesLine struct {
	Labels map[string]string `json:"labels"`
	// Points are [unix seconds, value].
	Points [][2]float64 `json:"points"`
}

type Series struct {
	Name        string       `json:"name"`
	Range       string       `json:"range"`
	StepSeconds int          `json:"stepSeconds"`
	Series      []SeriesLine `json:"series"`
}

type LogLine struct {
	Time      time.Time `json:"time"`
	Pod       string    `json:"pod"`
	Container string    `json:"container"`
	Msg       string    `json:"msg"`
}

// Me is GET /api/v1/me: who the request acts as and what the API offers it (design L.4).
type Me struct {
	User         string         `json:"user"`
	Role         string         `json:"role"`     // admin or viewer
	AuthMode     string         `json:"authMode"` // proxy, dev or demo
	Scopes       []string       `json:"scopes"`   // read, logs and actions:<plugin> for each plugin with mounted actions
	Audience     string         `json:"audience"` // api
	Version      string         `json:"version"`
	Capabilities MeCapabilities `json:"capabilities"`
	UI           MeUI           `json:"ui"`
}

// MeUI is the site configuration the SPA reads once with /me.
type MeUI struct {
	// IconBaseURL resolves the icon names of launcher links and is the only origin an absolute
	// launcher icon URL may come from (ui.iconBaseURL). The JSON key is iconBaseUrl: the API
	// document is linted for camelCase.
	IconBaseURL string `json:"iconBaseUrl"`
}

// MeCapabilities says which optional parts of the API this dash serves.
type MeCapabilities struct {
	Actions bool `json:"actions"` // some plugin has mounted actions
	MCP     bool `json:"mcp"`
	Push    bool `json:"push"`
	OAuth   bool `json:"oauth"`
}

// LauncherData is the payload of the launcher.links topic.
type LauncherData struct {
	Links []LauncherLink `json:"links"`
}

// LauncherLink is one infrastructure link in the launcher (DASH_LAUNCHER_FILE).
type LauncherLink struct {
	Name string `json:"name"`
	Href string `json:"href"`
	// Icon is a dashboard-icons name (e.g. "proxmox") or an image URL; empty shows a generic icon.
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

// RestartRequest is the body of POST /api/v1/plugins/kubernetes/actions/restart.
type RestartRequest struct {
	// Kind is Deployment, StatefulSet or DaemonSet; anything else is 400.
	Kind      WorkloadKind `json:"kind"`
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	// Confirm must equal Name in a namespace labelled homelab.io/protected=true.
	Confirm string `json:"confirm"`
}

// ReconcileRequest is the body of POST /api/actions/reconcile.
type ReconcileRequest struct {
	// Kind is any FluxKind; anything else is 400.
	Kind      FluxKind `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
}

type SilenceMatcher struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SilenceRequest is the input of the alertmanager plugin's silence.create action.
type SilenceRequest struct {
	Matchers []SilenceMatcher `json:"matchers"`
	// Duration is 1h, 4h, 24h or 7d.
	Duration string `json:"duration"`
	Comment  string `json:"comment"`
}

type SilenceResponse struct {
	SilenceID string `json:"silenceID"`
}
