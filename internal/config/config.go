// Package config reads dash's environment. Defaults are the in-cluster values; DASH_DEV=true
// switches the dev defaults (health listener on loopback, public URL of the Vite dev server).
package config

import (
	"net/url"
	"os"
	"strings"
)

// Config is the whole runtime configuration.
type Config struct {
	Dev bool

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

	// HostNodename is the nodename label of the Proxmox host's node-exporter and IPMI series.
	HostNodename string

	HeadlampURL     string
	HeadlampCluster string
	GrafanaURL      string

	// Kubeconfig is used outside the cluster (dev); empty means in-cluster config.
	Kubeconfig string
}

// Load reads the environment through getenv (os.Getenv in production).
func Load(getenv func(string) string) Config {
	dev := strings.EqualFold(strings.TrimSpace(getenv("DASH_DEV")), "true")
	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	healthDef, publicDef := "0.0.0.0:8081", "https://dash.home.techdufus.com"
	if dev {
		healthDef, publicDef = "127.0.0.1:8081", "http://localhost:5173"
	}
	return Config{
		Dev:             dev,
		Listen:          str("DASH_LISTEN", "127.0.0.1:8080"),
		HealthListen:    str("DASH_HEALTH_LISTEN", healthDef),
		PublicURL:       strings.TrimRight(str("DASH_PUBLIC_URL", publicDef), "/"),
		VMURL:           strings.TrimRight(str("DASH_VM_URL", "http://vmsingle-vm.monitoring.svc:8428"), "/"),
		AlertmanagerURL: strings.TrimRight(str("DASH_ALERTMANAGER_URL", "http://vmalertmanager-vm.monitoring.svc:9093"), "/"),
		VLogsURL:        strings.TrimRight(str("DASH_VLOGS_URL", "http://vl-server.monitoring.svc:9428"), "/"),
		GatusURL:        strings.TrimRight(str("DASH_GATUS_URL", "http://gatus.gatus.svc:8080"), "/"),
		ProxmoxURL:      strings.TrimRight(str("DASH_PROXMOX_URL", "https://proxmox.home.techdufus.com:8006"), "/"),
		ProxmoxNode:     str("DASH_PROXMOX_NODE", "pve-730"),
		ProxmoxToken:    strings.TrimSpace(getenv("DASH_PROXMOX_TOKEN")),
		HostNodename:    str("DASH_HOST_NODENAME", "pve-730"),
		HeadlampURL:     strings.TrimRight(str("DASH_HEADLAMP_URL", "https://headlamp.home.techdufus.com"), "/"),
		HeadlampCluster: str("DASH_HEADLAMP_CLUSTER", "main"),
		GrafanaURL:      strings.TrimRight(str("DASH_GRAFANA_URL", "https://grafana.home.techdufus.com"), "/"),
		Kubeconfig:      strings.TrimSpace(getenv("KUBECONFIG")),
	}
}

// FromEnv is Load(os.Getenv).
func FromEnv() Config { return Load(os.Getenv) }

// PublicOrigin is scheme://host[:port] of PublicURL, the only Origin accepted on writes.
func (c Config) PublicOrigin() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return c.PublicURL
	}
	return u.Scheme + "://" + u.Host
}
