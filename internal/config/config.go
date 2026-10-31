// Package config reads dash's environment. Defaults are the in-cluster values; DASH_DEV=true
// switches the dev defaults (health listener on loopback, public URL of the Vite dev server).
// DASH_DEMO=true serves built-in fake data instead of a cluster and implies DASH_DEV.
//
// Nothing site-specific has a default: DASH_PUBLIC_URL is required outside dev, Proxmox needs
// DASH_PROXMOX_URL and DASH_PROXMOX_NODE once a token is set, and an empty Headlamp or Grafana URL
// drops those links.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/TechDufus/dash/internal/model"
)

// Config is the whole runtime configuration.
type Config struct {
	Dev  bool
	Demo bool // fake data from internal/demo; no cluster or upstreams are contacted

	Listen       string // API + SPA listener; loopback only, reached through oauth2-proxy.
	HealthListen string // /healthz and /readyz only.
	PublicURL    string

	VMURL           string
	AlertmanagerURL string
	VLogsURL        string
	GatusURL        string

	ProxmoxURL   string
	ProxmoxNode  string
	ProxmoxToken string // "<user>!<token>=<secret>"; empty marks Proxmox unavailable.

	// HostNodename is the nodename label of the Proxmox host's node-exporter and IPMI series;
	// it defaults to ProxmoxNode.
	HostNodename string
	// HostModel is shown next to the host's name, e.g. "Dell R730"; empty shows nothing.
	HostModel string

	HeadlampURL     string
	HeadlampCluster string
	GrafanaURL      string

	// LauncherFile is a YAML or JSON list of infrastructure links (see LauncherLink).
	LauncherFile string
	Launcher     []model.LauncherLink

	Conventions Conventions

	// Kubeconfig is used outside the cluster (dev); empty means in-cluster config.
	Kubeconfig string
}

// Conventions are the labels, annotations and metric names dash reads from the cluster.
type Conventions struct {
	// ProtectedLabel marks a namespace whose restarts must be confirmed by typing the name.
	ProtectedLabel string
	// TenantLabel puts a namespace in the Tenants group; its value is the tenant.
	TenantLabel string
	// SuspendReasonAnnotation on a Flux object is shown as the reason it is suspended.
	SuspendReasonAnnotation string
	// AppsFluxPrefix puts namespaces applied by a Flux Kustomization with this name prefix in Apps.
	AppsFluxPrefix string
	// ZFSScrubMetricPrefix names the scrub series: <prefix>_last_end_timestamp_seconds,
	// <prefix>_last_errors and <prefix>_in_progress.
	ZFSScrubMetricPrefix string
	// NASBackupNamespace and NASBackupCronJob locate the NAS backup CronJob(s) (name or name-*).
	NASBackupNamespace string
	NASBackupCronJob   string
}

// Load reads the environment through getenv (os.Getenv in production).
func Load(getenv func(string) string) Config {
	demo := strings.EqualFold(strings.TrimSpace(getenv("DASH_DEMO")), "true")
	dev := demo || strings.EqualFold(strings.TrimSpace(getenv("DASH_DEV")), "true")
	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	healthDef, publicDef := "0.0.0.0:8081", ""
	if dev {
		healthDef, publicDef = "127.0.0.1:8081", "http://localhost:5173"
	}
	node := str("DASH_PROXMOX_NODE", "")
	return Config{
		Dev:             dev,
		Demo:            demo,
		Listen:          str("DASH_LISTEN", "127.0.0.1:8080"),
		HealthListen:    str("DASH_HEALTH_LISTEN", healthDef),
		PublicURL:       strings.TrimRight(str("DASH_PUBLIC_URL", publicDef), "/"),
		VMURL:           strings.TrimRight(str("DASH_VM_URL", "http://vmsingle-vm.monitoring.svc:8428"), "/"),
		AlertmanagerURL: strings.TrimRight(str("DASH_ALERTMANAGER_URL", "http://vmalertmanager-vm.monitoring.svc:9093"), "/"),
		VLogsURL:        strings.TrimRight(str("DASH_VLOGS_URL", "http://vl-server.monitoring.svc:9428"), "/"),
		GatusURL:        strings.TrimRight(str("DASH_GATUS_URL", "http://gatus.gatus.svc:8080"), "/"),
		ProxmoxURL:      strings.TrimRight(str("DASH_PROXMOX_URL", ""), "/"),
		ProxmoxNode:     node,
		ProxmoxToken:    strings.TrimSpace(getenv("DASH_PROXMOX_TOKEN")),
		HostNodename:    str("DASH_HOST_NODENAME", node),
		HostModel:       str("DASH_HOST_MODEL", ""),
		HeadlampURL:     strings.TrimRight(str("DASH_HEADLAMP_URL", ""), "/"),
		HeadlampCluster: str("DASH_HEADLAMP_CLUSTER", "main"),
		GrafanaURL:      strings.TrimRight(str("DASH_GRAFANA_URL", ""), "/"),
		LauncherFile:    str("DASH_LAUNCHER_FILE", ""),
		Conventions: Conventions{
			ProtectedLabel:          str("DASH_LABEL_PROTECTED", "homelab.io/protected"),
			TenantLabel:             str("DASH_LABEL_TENANT", "homelab.io/tenant"),
			SuspendReasonAnnotation: str("DASH_ANNOTATION_SUSPEND_REASON", "homelab.io/suspended"),
			AppsFluxPrefix:          str("DASH_APPS_FLUX_PREFIX", "app-"),
			ZFSScrubMetricPrefix:    str("DASH_ZFS_SCRUB_METRIC_PREFIX", "homelab_zfs_scrub"),
			NASBackupNamespace:      str("DASH_NAS_BACKUP_NAMESPACE", "rustfs"),
			NASBackupCronJob:        str("DASH_NAS_BACKUP_CRONJOB", "nas-backup"),
		},
		Kubeconfig: strings.TrimSpace(getenv("KUBECONFIG")),
	}
}

// FromEnv is Load(os.Getenv) plus the launcher file, validated.
func FromEnv() (Config, error) {
	c := Load(os.Getenv)
	if err := c.Validate(); err != nil {
		return c, err
	}
	if c.LauncherFile != "" {
		links, err := LoadLauncher(c.LauncherFile)
		if err != nil {
			return c, err
		}
		c.Launcher = links
	}
	return c, nil
}

var metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// Validate reports settings that are missing or unusable.
func (c Config) Validate() error {
	var errs []error
	if c.PublicURL == "" && !c.Dev {
		errs = append(errs, errors.New("DASH_PUBLIC_URL is required (the URL users open, e.g. https://dash.example.com)"))
	}
	if c.ProxmoxToken != "" && (c.ProxmoxURL == "" || c.ProxmoxNode == "") {
		errs = append(errs, errors.New("DASH_PROXMOX_URL and DASH_PROXMOX_NODE are required with DASH_PROXMOX_TOKEN"))
	}
	if !metricNameRE.MatchString(c.Conventions.ZFSScrubMetricPrefix) {
		errs = append(errs, fmt.Errorf("DASH_ZFS_SCRUB_METRIC_PREFIX %q is not a metric name", c.Conventions.ZFSScrubMetricPrefix))
	}
	return errors.Join(errs...)
}

// LoadLauncher reads a YAML or JSON list of launcher links.
func LoadLauncher(path string) ([]model.LauncherLink, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("launcher file: %w", err)
	}
	var links []model.LauncherLink
	if err := yaml.UnmarshalStrict(raw, &links); err != nil {
		return nil, fmt.Errorf("launcher file %s: %w", path, err)
	}
	for i, l := range links {
		u, err := url.Parse(l.Href)
		if l.Name == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("launcher file %s: entry %d needs a name and an http(s) href", path, i+1)
		}
	}
	return links, nil
}

// PublicOrigin is scheme://host[:port] of PublicURL, the only Origin accepted on writes.
func (c Config) PublicOrigin() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return c.PublicURL
	}
	return u.Scheme + "://" + u.Host
}
