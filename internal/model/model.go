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

// Topic is a name published on /api/stream.
type Topic string

const (
	TopicOverview Topic = "overview"
	TopicHost     Topic = "host"
	TopicVMs      Topic = "vms"
	TopicCluster  Topic = "cluster"
	TopicFlux     Topic = "flux"
	TopicStorage  Topic = "storage"
	TopicAlerts   Topic = "alerts"
	TopicStatus   Topic = "status"
	TopicApps     Topic = "apps"
)

type Link struct {
	Label string   `json:"label"`
	Href  string   `json:"href"`
	Kind  LinkKind `json:"kind"`
}

// SourceStatus reports one upstream behind a topic; ok=false shows "source unavailable".
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
	ID       string    `json:"id"`
	Severity Severity  `json:"severity"`
	Source   string    `json:"source"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail"`
	Since    time.Time `json:"since"`
	// Route is an in-app path, e.g. /cluster/pods/<ns>/<name>.
	Route string `json:"route"`
	Links []Link `json:"links"`
}

type WorstPVC struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	UsedRatio float64 `json:"usedRatio"`
}

type Kpis struct {
	AlertsFiring   int       `json:"alertsFiring"`
	FluxReady      int       `json:"fluxReady"`
	FluxTotal      int       `json:"fluxTotal"`
	FluxSuspended  int       `json:"fluxSuspended"`
	SourceVerified bool      `json:"sourceVerified"`
	NodesReady     int       `json:"nodesReady"`
	NodesTotal     int       `json:"nodesTotal"`
	VMsRunning     int       `json:"vmsRunning"`
	VMsTotal       int       `json:"vmsTotal"`
	PowerWatts     float64   `json:"powerWatts"`
	CPUTempMaxC    float64   `json:"cpuTempMaxC"`
	WorstPVC       *WorstPVC `json:"worstPvc,omitempty"`
}

type TopologyHost struct {
	Name         string  `json:"name"`
	Health       Health  `json:"health"`
	PowerWatts   float64 `json:"powerWatts"`
	CPUTempC     float64 `json:"cpuTempC"`
	MemUsedRatio float64 `json:"memUsedRatio"`
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
	Kpis      Kpis            `json:"kpis"`
	Topology  Topology        `json:"topology"`
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

type Pool struct {
	Name            string     `json:"name"`
	State           string     `json:"state"`
	LastScrubEnd    *time.Time `json:"lastScrubEnd,omitempty"`
	ScrubErrors     int        `json:"scrubErrors"`
	ScrubInProgress bool       `json:"scrubInProgress"`
}

type Disk struct {
	Bay              string   `json:"bay"`
	Serial           string   `json:"serial"`
	Device           string   `json:"device"`
	Healthy          bool     `json:"healthy"`
	TempC            *float64 `json:"tempC,omitempty"`
	LifeRemainingPct *float64 `json:"lifeRemainingPct,omitempty"`
}

type HostData struct {
	Name          string    `json:"name"`
	UptimeSeconds int64     `json:"uptimeSeconds"`
	PVEVersion    string    `json:"pveVersion"`
	Kernel        string    `json:"kernel"`
	CPUModel      string    `json:"cpuModel"`
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
	Disks         []Disk    `json:"disks"`
	Links         []Link    `json:"links"`
}

// ---- vms ----

type K8sNodeRef struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}

type VM struct {
	VMID          int         `json:"vmid"`
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	Health        Health      `json:"health"`
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

type Machine struct {
	Name   string `json:"name"`
	Up     bool   `json:"up"`
	Source string `json:"source"`
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
	VMs      []VM      `json:"vms"`
	Machines []Machine `json:"machines"`
	Tasks    []PVETask `json:"tasks"`
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
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Ready     bool       `json:"ready"`
	NotAfter  *time.Time `json:"notAfter,omitempty"`
	Links     []Link     `json:"links"`
}

type ExtSecret struct {
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

type PVEStorage struct {
	Name      string `json:"name"`
	UsedBytes int64  `json:"usedBytes"`
	SizeBytes int64  `json:"sizeBytes"`
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

type NASBackup struct {
	Name                  string   `json:"name"`
	LastSuccessAgeSeconds *float64 `json:"lastSuccessAgeSeconds,omitempty"`
}

type Etcd struct {
	DBBytes    int64 `json:"dbBytes"`
	QuotaBytes int64 `json:"quotaBytes"`
}

type StorageData struct {
	PVCs        []PVC         `json:"pvcs"`
	Buckets     []Bucket      `json:"buckets"`
	RustFS      UsedSize      `json:"rustfs"`
	PVEStorages []PVEStorage  `json:"pveStorages"`
	CNPG        []CNPGCluster `json:"cnpg"`
	NASBackups  []NASBackup   `json:"nasBackups"`
	Etcd        Etcd          `json:"etcd"`
}

// ---- alerts ----

type Alert struct {
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
}

// ---- status (Gatus) ----

type CheckResult struct {
	Success    bool      `json:"success"`
	Timestamp  time.Time `json:"timestamp"`
	DurationMs float64   `json:"durationMs"`
}

type Endpoint struct {
	Group   string        `json:"group"`
	Name    string        `json:"name"`
	Key     string        `json:"key"`
	URL     string        `json:"url"`
	Healthy bool          `json:"healthy"`
	Results []CheckResult `json:"results"`
}

type StatusData struct {
	Endpoints []Endpoint `json:"endpoints"`
}

// ---- apps ----

type App struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IconURL     string `json:"iconUrl"`
	Href        string `json:"href"`
	Group       Group  `json:"group"`
	Namespace   string `json:"namespace"`
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

type Me struct {
	User    string `json:"user"`
	Version string `json:"version"`
}

// LauncherLink is one infrastructure link in the launcher (DASH_LAUNCHER_FILE).
type LauncherLink struct {
	Name string `json:"name"`
	Href string `json:"href"`
	// Icon is a dashboard-icons name (e.g. "proxmox") or an image URL; empty shows a generic icon.
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

// UIConfig is GET /api/config: the site-specific bits of the UI.
type UIConfig struct {
	// HostModel is shown next to the host's name, e.g. "Dell R730"; empty shows nothing.
	HostModel string         `json:"hostModel"`
	Launcher  []LauncherLink `json:"launcher"`
}

// APIError is the body of every non-2xx JSON answer.
type APIError struct {
	Error string `json:"error"`
}

// RestartRequest is the body of POST /api/actions/restart.
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

// SilenceRequest is the body of POST /api/silences.
type SilenceRequest struct {
	Matchers []SilenceMatcher `json:"matchers"`
	// Duration is 1h, 4h, 24h or 7d.
	Duration string `json:"duration"`
	Comment  string `json:"comment"`
}

type SilenceResponse struct {
	SilenceID string `json:"silenceID"`
}
