package config

import (
	"reflect"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/pluginconf"
)

// Tree is the typed configuration (design B.4). The json tags are the YAML keys and, converted
// to UPPER_SNAKE, the environment names (B.3). The dash tag holds comma-separated metadata:
//
//	secret   the value is a credential (a secret.Value, never logged or shown). Layers give a
//	         literal, env:NAME or file:/path; resolve reads the reference once all layers are set
//	restart  a change needs a restart (x-dash-restart, B.6)
//	url      the value is a URL: validated as http(s) and stored without a trailing slash
//
// The auth section is not here yet: a section appears with its first field (G4, P4.6). The
// plugin sections and ui are defined in internal/pluginconf, so the plugin packages share them.
type Tree struct {
	Server   ServerConfig   `json:"server" doc:"How dash listens and what it calls itself."`
	Settings SettingsConfig `json:"settings" doc:"The settings store behind the settings page."`
	DataDir  string         `json:"dataDir" dash:"restart" doc:"Where dash keeps its state (ui-settings.yaml, audit.jsonl) and looks for config.yaml. Demo mode uses none."`
	UI       pluginconf.UI  `json:"ui" doc:"What the web UI is told by the server."`
	Plugins  PluginsConfig  `json:"plugins" doc:"One section per plugin. An empty URL turns that source off."`
}

// ServerConfig is how dash listens and what it calls itself.
type ServerConfig struct {
	PublicURL     string              `json:"publicURL" dash:"url,restart" doc:"The URL people open, with scheme and host (https://dash.example.com). Required; the only Origin accepted on writes. In dev and demo mode it defaults to http://localhost:5173."`
	Listen        string              `json:"listen" dash:"restart" doc:"Address of the API and the web UI, reached through the identity proxy. Dev mode only accepts a loopback address."`
	HealthListen  string              `json:"healthListen" dash:"restart" doc:"Address of /healthz and /readyz. In dev and demo mode it defaults to 127.0.0.1:8081."`
	AllowedHosts  []string            `json:"allowedHosts" dash:"restart" doc:"Host names the API answers besides the host of publicURL: names or IP addresses without scheme or port. [\"*\"] accepts any. A JSON list in the environment."`
	ReadyRequires []string            `json:"readyRequires" dash:"restart" doc:"Plugins /readyz waits for: each listed plugin must have finished starting (its first poll or cache sync) before dash reports ready, up to readyTimeout. Empty: /readyz needs only that dash is serving and every plugin's start has returned. Names are plugins.<id> sections, e.g. [\"kubernetes\"]. A JSON list in the environment."`
	ReadyTimeout  pluginconf.Duration `json:"readyTimeout" dash:"restart" doc:"How long /readyz waits for the readyRequires plugins, counted from startup. After that dash reports ready anyway and logs a warning naming the plugins still starting."`
	Metrics       bool                `json:"metrics" dash:"restart" doc:"Serve dash's own Prometheus metrics at /metrics on the health listener. Off by default. The listener is unauthenticated and the metrics name plugins and their states, so keep it cluster-internal."`
	Limits        LimitsConfig        `json:"limits" doc:"What one client, and all clients together, may hold open or send."`
}

// LimitsConfig bounds what one client and all clients together may hold open or send (design
// K.5, L.5). The counts are connections; the defaults suit a homelab.
type LimitsConfig struct {
	StreamsPerUser  int                 `json:"streamsPerUser" dash:"restart" doc:"Open /api/v1/stream connections one user may hold; more get 429."`
	Streams         int                 `json:"streams" dash:"restart" doc:"Open /api/v1/stream connections in all; more get 503."`
	TailsPerUser    int                 `json:"tailsPerUser" dash:"restart" doc:"Open plugin stream connections (the log tail) one user may hold; more get 429."`
	Tails           int                 `json:"tails" dash:"restart" doc:"Open plugin stream connections (the log tail) in all; more get 503."`
	TailMaxDuration pluginconf.Duration `json:"tailMaxDuration" dash:"restart" doc:"A log tail ends after this long and the browser reconnects."`
	SSEWriteTimeout pluginconf.Duration `json:"sseWriteTimeout" dash:"restart" doc:"A stalled stream write fails after this long."`
	BodyReadTimeout pluginconf.Duration `json:"bodyReadTimeout" dash:"restart" doc:"A request body must arrive within this long."`
	MaxHeaderBytes  int                 `json:"maxHeaderBytes" dash:"restart" doc:"Largest request header block the API listener accepts, in bytes."`
}

var durationType = reflect.TypeFor[pluginconf.Duration]()

// Values of SettingsConfig.UI.
const (
	SettingsEditable = "editable"
	SettingsReadOnly = "readonly"
)

// SettingsConfig controls the UI settings store.
type SettingsConfig struct {
	// UI is SettingsEditable or SettingsReadOnly (fully managed, GitOps).
	UI string `json:"ui" dash:"restart,enum=editable|readonly" doc:"Whether the settings page may save values: editable, or readonly (fully managed, GitOps). A data directory that is missing or not writable is read-only either way."`
}

// PluginsConfig holds one section per plugin, each embedding PluginBase (plugins.<id>.type and
// .enabled). Until the plugins migrate (G3) the sections are the upstreams and cluster
// conventions the flat Config already carried.
type PluginsConfig struct {
	Prometheus   pluginconf.Prometheus   `json:"prometheus" doc:"VictoriaMetrics or Prometheus: the series behind charts, KPIs and host metrics."`
	Alertmanager pluginconf.Alertmanager `json:"alertmanager" doc:"Alertmanager: alerts and silences."`
	VictoriaLogs pluginconf.VictoriaLogs `json:"victorialogs" doc:"VictoriaLogs: the pod logs the UI shows."`
	Gatus        pluginconf.Gatus        `json:"gatus" doc:"Gatus: endpoint health."`
	Proxmox      pluginconf.Proxmox      `json:"proxmox" doc:"Proxmox VE: virtual machines, containers and the hypervisor."`
	Hardware     pluginconf.Hardware     `json:"hardware" doc:"The physical host behind the cluster."`
	Headlamp     pluginconf.Headlamp     `json:"headlamp" doc:"Headlamp: deep links into the Kubernetes UI."`
	Grafana      pluginconf.Grafana      `json:"grafana" doc:"Grafana: links to dashboards."`
	Launcher     pluginconf.Launcher     `json:"launcher" doc:"The launcher: links to your infrastructure."`
	Kubernetes   KubernetesConfig        `json:"kubernetes" doc:"Kubernetes: how dash reaches and reads the cluster."`
	Flux         FluxConfig              `json:"flux" doc:"Flux: how dash reads Flux objects."`
}

// KubernetesConfig is how dash reaches and reads the cluster.
type KubernetesConfig struct {
	pluginconf.PluginBase
	// Kubeconfig is used outside the cluster (dev); empty means in-cluster config.
	Kubeconfig  string                `json:"kubeconfig" doc:"Path of a kubeconfig file, used outside the cluster (development). Empty means the in-cluster configuration. The standard KUBECONFIG variable fills it too; this setting's own variable wins."`
	Conventions KubernetesConventions `json:"conventions" doc:"Labels dash reads from namespaces. None has a default: while one is empty the feature that depends on it is off."`
	// BackupJobs locate the NAS backup CronJob (name or name-*); at most one until G3.
	BackupJobs []BackupJob `json:"backupJobs" doc:"The CronJob behind the NAS backup age, as [{namespace, cronjob}]. One entry is supported for now. Empty hides the NAS backup row."`
}

// KubernetesConventions are the namespace labels dash reads.
type KubernetesConventions struct {
	// ProtectedLabel marks a namespace whose restarts must be confirmed by typing the name.
	ProtectedLabel string `json:"protectedLabel" doc:"Namespace label (value \"true\") that makes a restart ask you to type the namespace name. Empty protects no namespace."`
	// TenantLabel puts a namespace in the Tenants group; its value is the tenant.
	TenantLabel string `json:"tenantLabel" doc:"Namespace label that puts the namespace under Tenants; its value is the tenant. Empty turns the grouping off."`
	// AppsFluxPrefix puts namespaces applied by a Flux Kustomization with this name prefix in Apps.
	AppsFluxPrefix string `json:"appsFluxPrefix" doc:"Namespaces applied by a Flux Kustomization whose name starts with this prefix are listed under Apps. Empty turns the grouping off."`
}

// BackupJob is a CronJob whose last success is the NAS backup age.
type BackupJob struct {
	Namespace string `json:"namespace" doc:"Namespace of the backup CronJob."`
	CronJob   string `json:"cronjob" doc:"Name of the backup CronJob, or name-* to match every CronJob with that prefix."`
}

// FluxConfig is how dash reads Flux objects.
type FluxConfig struct {
	pluginconf.PluginBase
	// SuspendReasonAnnotation on a Flux object is shown as the reason it is suspended.
	SuspendReasonAnnotation string `json:"suspendReasonAnnotation" doc:"Flux object annotation whose value is shown as the reason the object is suspended. Empty hides the reason."`
}

// defaults is the built-in layer. Nothing site-specific is in it: an empty upstream URL or
// convention turns that source or feature off. Dev and demo (DASH_DEV, DASH_DEMO) listen for
// the Vite dev server: a public URL pointing at it, and a health listener on loopback.
func defaults(dev, demo bool) Tree {
	t := Tree{
		Server: ServerConfig{
			Listen:       "127.0.0.1:8080",
			HealthListen: "0.0.0.0:8081",
			ReadyTimeout: pluginconf.Duration(60 * time.Second),
			Limits: LimitsConfig{
				StreamsPerUser: 16, Streams: 256,
				TailsPerUser: 4, Tails: 32,
				TailMaxDuration: pluginconf.Duration(30 * time.Minute),
				SSEWriteTimeout: pluginconf.Duration(10 * time.Second),
				BodyReadTimeout: pluginconf.Duration(10 * time.Second),
				MaxHeaderBytes:  64 << 10,
			},
		},
		Settings: SettingsConfig{UI: SettingsEditable},
		DataDir:  "/data",
		Plugins: PluginsConfig{
			Prometheus:   pluginconf.DefaultPrometheus(),
			Alertmanager: pluginconf.DefaultAlertmanager(),
			VictoriaLogs: pluginconf.DefaultVictoriaLogs(),
			Gatus:        pluginconf.DefaultGatus(),
			Headlamp:     pluginconf.DefaultHeadlamp(),
		},
	}
	if dev || demo {
		t.Server.HealthListen, t.Server.PublicURL = "127.0.0.1:8081", "http://localhost:5173"
	}
	return t
}

// normalize trims what operators paste: surrounding spaces everywhere, the trailing slash of
// URLs, and the case and empty entries of the allowed host names.
func (t *Tree) normalize() {
	for _, l := range schema.leaves {
		f := l.field(t)
		switch {
		case l.typ.Kind() == reflect.String && l.url:
			f.SetString(strings.TrimRight(strings.TrimSpace(f.String()), "/"))
		case l.typ.Kind() == reflect.String:
			f.SetString(strings.TrimSpace(f.String()))
		}
	}
	t.Server.AllowedHosts = splitHosts(strings.Join(t.Server.AllowedHosts, ","))
	t.Server.ReadyRequires = splitHosts(strings.Join(t.Server.ReadyRequires, ","))
	for i := range t.Plugins.Hardware.Hosts {
		h := &t.Plugins.Hardware.Hosts[i]
		h.Nodename, h.Model = strings.TrimSpace(h.Nodename), strings.TrimSpace(h.Model)
	}
	for i := range t.Plugins.Kubernetes.BackupJobs {
		j := &t.Plugins.Kubernetes.BackupJobs[i]
		j.Namespace, j.CronJob = strings.TrimSpace(j.Namespace), strings.TrimSpace(j.CronJob)
	}
}

// config maps the tree onto the flat view the rest of dash reads.
func (t Tree) config(dev, demo bool) Config {
	p := t.Plugins
	c := Config{
		Dev:           dev,
		Demo:          demo,
		Listen:        t.Server.Listen,
		HealthListen:  t.Server.HealthListen,
		PublicURL:     t.Server.PublicURL,
		AllowedHosts:  t.Server.AllowedHosts,
		ReadyRequires: t.Server.ReadyRequires,
		ReadyTimeout:  time.Duration(t.Server.ReadyTimeout),
		Metrics:       t.Server.Metrics,
		Limits: Limits{
			StreamsPerUser: t.Server.Limits.StreamsPerUser, Streams: t.Server.Limits.Streams,
			TailsPerUser: t.Server.Limits.TailsPerUser, Tails: t.Server.Limits.Tails,
			TailMaxDuration: time.Duration(t.Server.Limits.TailMaxDuration),
			SSEWriteTimeout: time.Duration(t.Server.Limits.SSEWriteTimeout),
			BodyReadTimeout: time.Duration(t.Server.Limits.BodyReadTimeout),
			MaxHeaderBytes:  t.Server.Limits.MaxHeaderBytes,
		},
		DataDir:      t.DataDir,
		SettingsUI:   t.Settings.UI,
		ProxmoxURL:   p.Proxmox.URL,
		ProxmoxNode:  p.Proxmox.Node,
		HostNodename: p.Proxmox.Node, // overridden by hosts[0] below
		IconBaseURL:  t.UI.IconBaseURL,
		Kubeconfig:   p.Kubernetes.Kubeconfig,
		Conventions: Conventions{
			ProtectedLabel:          p.Kubernetes.Conventions.ProtectedLabel,
			TenantLabel:             p.Kubernetes.Conventions.TenantLabel,
			SuspendReasonAnnotation: p.Flux.SuspendReasonAnnotation,
			AppsFluxPrefix:          p.Kubernetes.Conventions.AppsFluxPrefix,
			ZFSScrubMetricPrefix:    p.Hardware.ZFSScrubMetricPrefix,
		},
	}
	c.ProxmoxTokenID, c.ProxmoxTokenSecret = p.Proxmox.TokenID, p.Proxmox.TokenSecret
	if len(p.Hardware.Hosts) > 0 {
		if h := p.Hardware.Hosts[0]; h.Nodename != "" {
			c.HostNodename = h.Nodename
		}
		c.HostModel = p.Hardware.Hosts[0].Model
	}
	if len(p.Kubernetes.BackupJobs) > 0 {
		c.Conventions.NASBackupNamespace = p.Kubernetes.BackupJobs[0].Namespace
		c.Conventions.NASBackupCronJob = p.Kubernetes.BackupJobs[0].CronJob
	}
	return c
}
