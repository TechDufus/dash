// Package config reads dash's environment. Defaults are the in-cluster values. DASH_DEV=true
// signs every request in as "dev" without authentication, so it only runs on a loopback
// DASH_LISTEN, and switches the dev defaults (health listener on loopback, public URL of the
// Vite dev server). DASH_DEMO=true serves built-in fake data instead of a cluster with the
// same defaults but never signs in as "dev" (internal/demo injects the "demo" user).
//
// Nothing site-specific has a default: DASH_PUBLIC_URL is required outside dev and demo, Proxmox needs
// DASH_PROXMOX_URL once a token is set (DASH_PROXMOX_NODE is optional: with one node in the
// cluster dash discovers it from the API at startup), and an empty Headlamp or Grafana URL
// drops those links.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/TechDufus/dash/internal/model"
)

// Config is the whole runtime configuration.
type Config struct {
	// Dev signs every request in as "dev" without authentication, so Validate requires a
	// loopback Listen. Demo has the same defaults but never the "dev" user.
	Dev  bool
	Demo bool // fake data from internal/demo; no cluster or upstreams are contacted

	// Listen is the API + SPA listener, reached through oauth2-proxy. It defaults to loopback;
	// with Dev it must be a loopback address.
	Listen       string
	HealthListen string // /healthz and /readyz only.
	PublicURL    string
	// AllowedHosts are extra Host names the API answers besides PublicURL's host
	// (DASH_ALLOWED_HOSTS); "*" accepts any. See HostAllowed.
	AllowedHosts []string

	VMURL           string
	AlertmanagerURL string
	VLogsURL        string
	GatusURL        string

	ProxmoxURL string
	// ProxmoxNode is the node whose status and guests dash reads. Optional: when empty and a
	// token is set, dash discovers it from the API at startup if the cluster has exactly one node.
	ProxmoxNode  string
	ProxmoxToken string // "<user>!<token>=<secret>"; empty marks Proxmox unavailable.

	// HostNodename is the nodename label of the Proxmox host's node-exporter and IPMI series;
	// it defaults to DASH_PROXMOX_NODE, or to the discovered node when that is empty.
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
	dev := strings.EqualFold(strings.TrimSpace(getenv("DASH_DEV")), "true")
	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	healthDef, publicDef := "0.0.0.0:8081", ""
	if dev || demo {
		healthDef, publicDef = "127.0.0.1:8081", "http://localhost:5173"
	}
	node := str("DASH_PROXMOX_NODE", "")
	return Config{
		Dev:             dev,
		Demo:            demo,
		Listen:          str("DASH_LISTEN", "127.0.0.1:8080"),
		HealthListen:    str("DASH_HEALTH_LISTEN", healthDef),
		PublicURL:       strings.TrimRight(str("DASH_PUBLIC_URL", publicDef), "/"),
		AllowedHosts:    splitHosts(getenv("DASH_ALLOWED_HOSTS")),
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
	if c.PublicURL == "" && !c.Dev && !c.Demo {
		errs = append(errs, errors.New("DASH_PUBLIC_URL is required (the URL users open, e.g. https://dash.example.com)"))
	}
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			errs = append(errs, fmt.Errorf("DASH_PUBLIC_URL %q must be an http(s) URL with a host, e.g. https://dash.example.com", c.PublicURL))
		}
	}
	if c.Dev && !loopbackAddr(c.Listen) {
		errs = append(errs, fmt.Errorf("DASH_DEV=true signs requests in as %q without authentication, so DASH_LISTEN must be a loopback address such as 127.0.0.1:8080, not %q", "dev", c.Listen))
	}
	if c.ProxmoxToken != "" && c.ProxmoxURL == "" {
		errs = append(errs, errors.New("DASH_PROXMOX_URL is required with DASH_PROXMOX_TOKEN"))
	}
	if !metricNameRE.MatchString(c.Conventions.ZFSScrubMetricPrefix) {
		errs = append(errs, fmt.Errorf("DASH_ZFS_SCRUB_METRIC_PREFIX %q is not a metric name", c.Conventions.ZFSScrubMetricPrefix))
	}
	for _, h := range c.AllowedHosts {
		if h != "*" && net.ParseIP(h) == nil && !hostNameRE.MatchString(h) {
			errs = append(errs, fmt.Errorf("DASH_ALLOWED_HOSTS entry %q is not a host name or IP address (no scheme or port)", h))
		}
	}
	return errors.Join(errs...)
}

// hostNameRE matches a DNS host name: dot-separated labels of lowercase letters, digits and hyphens.
var hostNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// splitHosts splits a comma-separated list, trimming and lowercasing entries and dropping empty ones.
func splitHosts(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// HostAllowed reports whether the API answers requests addressed to host (a Host header value,
// with or without a port). It is HostMatcher()(host); callers on a hot path should keep the matcher.
func (c Config) HostAllowed(host string) bool { return c.HostMatcher()(host) }

// HostMatcher returns HostAllowed with the configuration resolved once. Ports are ignored: a
// DNS-rebinding attacker controls the host name, not the port. Allowed are PublicURL's host,
// every AllowedHosts entry ("*" allows any host) and, in dev and demo mode, localhost,
// 127.0.0.1 and ::1. Both sides are compared lowercased without a trailing dot.
func (c Config) HostMatcher() func(host string) bool {
	public := ""
	if u, err := url.Parse(c.PublicURL); err == nil {
		public = normalizeHost(u.Hostname())
	}
	listed := slices.Clone(c.AllowedHosts)
	anyHost := slices.Contains(listed, "*")
	local := c.Dev || c.Demo
	return func(host string) bool {
		host = normalizeHost(host)
		if anyHost || slices.Contains(listed, host) {
			return true
		}
		if host != "" && host == public {
			return true
		}
		return local && (host == "localhost" || host == "127.0.0.1" || host == "::1")
	}
}

// normalizeHost strips a port and IPv6 brackets, lowercases and drops a trailing dot.
func normalizeHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
}

// loopbackAddr reports whether a host:port listen address binds only the loopback interface.
// An empty host (":8080") binds every interface.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// PublicOrigin is scheme://host[:port] of PublicURL, the only Origin accepted on writes. The
// host is lowercased because browsers send a lowercase Origin.
func (c Config) PublicOrigin() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return c.PublicURL
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}
