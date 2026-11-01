// Package config resolves dash's configuration from layers (design B.2): built-in defaults, the
// UI settings store, a YAML file (DASH_CONFIG) and the environment, the later layer winning per
// field, into a typed Tree and the flat Config the rest of dash reads. DASH_DEV=true signs every
// request in as "dev" without authentication, so it only runs on a loopback listen address, and
// switches the dev defaults (health listener on loopback, public URL of the Vite dev server).
// DASH_DEMO=true serves built-in fake data instead of a cluster with the same defaults but never
// signs in as "dev" (internal/demo injects the "demo" user).
//
// Nothing site-specific has a default: server.publicURL is required outside dev and demo, Proxmox
// needs plugins.proxmox.url once a token is set (plugins.proxmox.node is optional: with one node
// in the cluster dash discovers it from the API at startup), and an empty Headlamp or Grafana URL
// drops those links.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
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
	// (server.allowedHosts); "*" accepts any. See HostAllowed.
	AllowedHosts []string
	// ReadyRequires are the plugins /readyz waits for, and ReadyTimeout how long it waits
	// (server.readyRequires, server.readyTimeout); ReadyTimeout is positive once resolved.
	ReadyRequires []string
	ReadyTimeout  time.Duration
	Metrics       bool // serve /metrics on the health listener (server.metrics)
	// Limits bound open streams and request reads (server.limits); always positive once resolved.
	Limits Limits

	// DataDir holds dash's state (settings.ui store, credentials); SettingsUI is SettingsEditable
	// or SettingsReadOnly.
	DataDir    string
	SettingsUI string

	VMURL           string
	AlertmanagerURL string
	VLogsURL        string
	GatusURL        string

	ProxmoxURL string
	// ProxmoxNode is the node whose status and guests dash reads. Optional: when empty and a
	// token is set, dash discovers it from the API at startup if the cluster has exactly one node.
	ProxmoxNode string
	// ProxmoxTokenID is "<user>@<realm>!<token>"; with ProxmoxTokenSecret it marks Proxmox
	// available. Both are empty when Proxmox is off.
	ProxmoxTokenID     string
	ProxmoxTokenSecret secret.Value

	// HostNodename is the nodename label of the Proxmox host's node-exporter and IPMI series;
	// it defaults to plugins.proxmox.node, or to the discovered node when that is empty.
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

// Limits is server.limits as the flat view reads it.
type Limits struct {
	StreamsPerUser, Streams int // /api/v1/stream connections per user and in all
	TailsPerUser, Tails     int // /api/logs/tail connections per user and in all
	TailMaxDuration         time.Duration
	SSEWriteTimeout         time.Duration
	BodyReadTimeout         time.Duration
	MaxHeaderBytes          int // of the API listener
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

// ProcessSources reads the layers the process itself provides: its environment and the config
// file it points at. The caller adds the UI layer (Sources.UI) once it has opened the settings
// store, whose location (the data directory) comes from these layers.
func ProcessSources(reg *secret.Registry) (Sources, error) {
	environ := os.Environ()
	f, err := readConfigFile(environ)
	if err != nil {
		return Sources{}, err
	}
	return Sources{Environ: environ, File: f, Registry: reg}, nil
}

// LoadLauncherFile reads the launcher file the configuration names, if any, into Config.Launcher.
func (r *Resolved) LoadLauncherFile() error {
	c := &r.Config
	if c.LauncherFile == "" {
		return nil
	}
	links, err := LoadLauncher(c.LauncherFile)
	if err != nil {
		return err
	}
	c.Launcher = links
	return nil
}

// Load resolves defaults and the given os.Environ-style variables, with no config file and no UI
// settings.
func Load(environ []string) (Config, error) {
	res, err := Resolve(Sources{Environ: environ})
	if err != nil {
		return Config{}, err
	}
	return res.Config, nil
}

// Defaults is the configuration with nothing set, outside dev and demo. It is not startable
// (server.publicURL is required): it is the baseline for tests.
func Defaults() Config { return defaults(false, false).config(false, false) }

// configFilePath is where the config file is looked for: DASH_CONFIG when set (explicit: it must
// exist), otherwise config.yaml in the data directory (DASH_DATA_DIR, default /data).
func configFilePath(environ []string) (path string, explicit bool) {
	env := envMap(environ)
	if p := strings.TrimSpace(env["DASH_CONFIG"]); p != "" {
		return p, true
	}
	dir := strings.TrimSpace(env["DASH_DATA_DIR"])
	if dir == "" {
		dir = defaults(false, false).DataDir
	}
	return filepath.Join(dir, "config.yaml"), false
}

// readConfigFile reads the config file at configFilePath; the default one may be absent.
func readConfigFile(environ []string) (*File, error) {
	p, explicit := configFilePath(environ)
	data, err := os.ReadFile(p)
	switch {
	case explicit && err != nil:
		return nil, fmt.Errorf("DASH_CONFIG: %w", err)
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("config file: %w", err)
	}
	return &File{Path: p, Data: data}, nil
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
	if err := validateLinks(links); err != nil {
		return nil, fmt.Errorf("launcher file %s: %w", path, err)
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
