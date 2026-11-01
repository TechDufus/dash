package config

import (
	"reflect"
	"strings"

	"github.com/TechDufus/dash/internal/model"
)

// Tree is the typed configuration (design B.4). The json tags are the YAML keys and, converted
// to UPPER_SNAKE, the environment names (B.3). The dash tag holds comma-separated metadata:
//
//	secret   the value is a credential (never logged or shown; P1.4 resolves env:/file: refs)
//	restart  a change needs a restart (x-dash-restart, B.6)
//	url      the value is a URL: validated as http(s) and stored without a trailing slash
//
// The auth and ui sections are not here yet: a section appears with its first field (G4, P4.6).
type Tree struct {
	Server   ServerConfig   `json:"server"`
	Settings SettingsConfig `json:"settings"`
	DataDir  string         `json:"dataDir" dash:"restart"`
	Plugins  PluginsConfig  `json:"plugins"`
}

// ServerConfig is how dash listens and what it calls itself.
type ServerConfig struct {
	PublicURL    string   `json:"publicURL" dash:"url,restart"`
	Listen       string   `json:"listen" dash:"restart"`
	HealthListen string   `json:"healthListen" dash:"restart"`
	AllowedHosts []string `json:"allowedHosts" dash:"restart"`
}

// Values of SettingsConfig.UI.
const (
	SettingsEditable = "editable"
	SettingsReadOnly = "readonly"
)

// SettingsConfig controls the UI settings store.
type SettingsConfig struct {
	// UI is SettingsEditable or SettingsReadOnly (fully managed, GitOps).
	UI string `json:"ui"`
}

// PluginsConfig holds one section per plugin. Until the plugin framework (G2) these are the
// upstreams and cluster conventions the flat Config already carried.
type PluginsConfig struct {
	Prometheus   Endpoint         `json:"prometheus"`
	Alertmanager Endpoint         `json:"alertmanager"`
	VictoriaLogs Endpoint         `json:"victorialogs"`
	Gatus        Endpoint         `json:"gatus"`
	Proxmox      ProxmoxConfig    `json:"proxmox"`
	Hardware     HardwareConfig   `json:"hardware"`
	Headlamp     HeadlampConfig   `json:"headlamp"`
	Grafana      Endpoint         `json:"grafana"`
	Launcher     LauncherConfig   `json:"launcher"`
	Kubernetes   KubernetesConfig `json:"kubernetes"`
	Flux         FluxConfig       `json:"flux"`
}

// Endpoint is a plugin that only needs a base URL. Empty turns the source off.
type Endpoint struct {
	URL string `json:"url" dash:"url"`
}

// ProxmoxConfig is the Proxmox API. TokenID and TokenSecret are the two halves of an API token
// ("user@realm!name" and its secret), so that only the secret is secret.
type ProxmoxConfig struct {
	URL         string `json:"url" dash:"url"`
	Node        string `json:"node"`
	TokenID     string `json:"tokenID"`
	TokenSecret string `json:"tokenSecret" dash:"secret"`
}

// HardwareConfig is the physical host behind the cluster.
type HardwareConfig struct {
	// Hosts has at most one entry until the hardware plugin supports several (G3).
	Hosts []HardwareHost `json:"hosts"`
	// ZFSScrubMetricPrefix names the scrub series: <prefix>_last_end_timestamp_seconds,
	// <prefix>_last_errors and <prefix>_in_progress.
	ZFSScrubMetricPrefix string `json:"zfsScrubMetricPrefix"`
}

// HardwareHost is a machine whose node-exporter and IPMI series dash reads.
type HardwareHost struct {
	// Nodename is the nodename label of its series; empty falls back to the Proxmox node.
	Nodename string `json:"nodename"`
	// Model is shown next to the host's name, e.g. "Dell R730".
	Model string `json:"model"`
}

// HeadlampConfig is the Headlamp deep-link target.
type HeadlampConfig struct {
	URL     string `json:"url" dash:"url"`
	Cluster string `json:"cluster"`
}

// LauncherConfig is the launcher's list of infrastructure links, from a file or inline, not both.
type LauncherConfig struct {
	File  string               `json:"file"`
	Links []model.LauncherLink `json:"links"`
}

// KubernetesConfig is how dash reaches and reads the cluster.
type KubernetesConfig struct {
	// Kubeconfig is used outside the cluster (dev); empty means in-cluster config.
	Kubeconfig  string                `json:"kubeconfig"`
	Conventions KubernetesConventions `json:"conventions"`
	// BackupJobs locate the NAS backup CronJob (name or name-*); at most one until G3.
	BackupJobs []BackupJob `json:"backupJobs"`
}

// KubernetesConventions are the namespace labels dash reads.
type KubernetesConventions struct {
	// ProtectedLabel marks a namespace whose restarts must be confirmed by typing the name.
	ProtectedLabel string `json:"protectedLabel"`
	// TenantLabel puts a namespace in the Tenants group; its value is the tenant.
	TenantLabel string `json:"tenantLabel"`
	// AppsFluxPrefix puts namespaces applied by a Flux Kustomization with this name prefix in Apps.
	AppsFluxPrefix string `json:"appsFluxPrefix"`
}

// BackupJob is a CronJob whose last success is the NAS backup age.
type BackupJob struct {
	Namespace string `json:"namespace"`
	CronJob   string `json:"cronjob"`
}

// FluxConfig is how dash reads Flux objects.
type FluxConfig struct {
	// SuspendReasonAnnotation on a Flux object is shown as the reason it is suspended.
	SuspendReasonAnnotation string `json:"suspendReasonAnnotation"`
}

// defaults is the built-in layer. Nothing site-specific is in it: an empty upstream URL or
// convention turns that source or feature off. Dev and demo (DASH_DEV, DASH_DEMO) listen for
// the Vite dev server: a public URL pointing at it, and a health listener on loopback.
func defaults(dev, demo bool) Tree {
	t := Tree{
		Server: ServerConfig{
			Listen:       "127.0.0.1:8080",
			HealthListen: "0.0.0.0:8081",
		},
		Settings: SettingsConfig{UI: SettingsEditable},
		DataDir:  "/data",
		Plugins:  PluginsConfig{Headlamp: HeadlampConfig{Cluster: "main"}},
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
		Dev:             dev,
		Demo:            demo,
		Listen:          t.Server.Listen,
		HealthListen:    t.Server.HealthListen,
		PublicURL:       t.Server.PublicURL,
		AllowedHosts:    t.Server.AllowedHosts,
		DataDir:         t.DataDir,
		SettingsUI:      t.Settings.UI,
		VMURL:           p.Prometheus.URL,
		AlertmanagerURL: p.Alertmanager.URL,
		VLogsURL:        p.VictoriaLogs.URL,
		GatusURL:        p.Gatus.URL,
		ProxmoxURL:      p.Proxmox.URL,
		ProxmoxNode:     p.Proxmox.Node,
		HostNodename:    p.Proxmox.Node, // overridden by hosts[0] below
		HeadlampURL:     p.Headlamp.URL,
		HeadlampCluster: p.Headlamp.Cluster,
		GrafanaURL:      p.Grafana.URL,
		LauncherFile:    p.Launcher.File,
		Launcher:        p.Launcher.Links,
		Kubeconfig:      p.Kubernetes.Kubeconfig,
		Conventions: Conventions{
			ProtectedLabel:          p.Kubernetes.Conventions.ProtectedLabel,
			TenantLabel:             p.Kubernetes.Conventions.TenantLabel,
			SuspendReasonAnnotation: p.Flux.SuspendReasonAnnotation,
			AppsFluxPrefix:          p.Kubernetes.Conventions.AppsFluxPrefix,
			ZFSScrubMetricPrefix:    p.Hardware.ZFSScrubMetricPrefix,
		},
	}
	if p.Proxmox.TokenID != "" && p.Proxmox.TokenSecret != "" {
		c.ProxmoxToken = p.Proxmox.TokenID + "=" + p.Proxmox.TokenSecret
	}
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
