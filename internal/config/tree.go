package config

import (
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/pluginconf"
)

// Tree is the typed configuration (design B.4). The json tags are the YAML keys and, converted
// to UPPER_SNAKE, the environment names (B.3). The dash tag holds comma-separated metadata:
//
//	secret   the value is a credential (a secret.Value, never logged or shown). Layers give a
//	         literal, env:NAME, file:/path or k8s:<ns>/<name>#<key>; resolve reads the reference once all layers are set
//	restart  a change needs a restart (x-dash-restart, B.6)
//	url      the value is a URL: validated as http(s) and stored without a trailing slash
//
// The auth section is in auth.go. The plugin sections and ui are defined in internal/pluginconf,
// so the plugin packages share them.
type Tree struct {
	Server   ServerConfig   `json:"server" doc:"How dash listens and what it calls itself."`
	Auth     AuthConfig     `json:"auth" doc:"How dash learns who is making a request and what they may do."`
	Settings SettingsConfig `json:"settings" doc:"The settings store behind the settings page."`
	DataDir  string         `json:"dataDir" dash:"restart" doc:"Where dash keeps its state (ui-settings.yaml, audit.jsonl, and credentials.yaml with the local account, the sessions' epochs and the personal access tokens) and looks for config.yaml. Without a writable one, sessions and tokens do not survive a restart, so creating a token is refused outside dev and demo mode. Demo mode uses none."`
	UI       pluginconf.UI  `json:"ui" doc:"What the web UI is told by the server."`
	MCP      MCPConfig      `json:"mcp" doc:"The read-only MCP server at /mcp, for AI clients holding an mcp-audience token."`
	Plugins  PluginsConfig  `json:"plugins" doc:"One section per plugin. An empty URL turns that source off."`
}

// ServerConfig is how dash listens and what it calls itself.
type ServerConfig struct {
	PublicURL     string              `json:"publicURL" dash:"url,restart" doc:"The URL people open, with scheme and host (https://dash.example.com). Required; the only Origin accepted on writes. In dev mode it defaults to http://localhost:5173 (the Vite dev server); in demo mode without dev mode to http:// and the listen address, with 127.0.0.1 for a listener on every address (http://127.0.0.1:8080 by default)."`
	DirectURL     string              `json:"directURL" dash:"url,restart" doc:"Behind an identity proxy (auth.mode=proxy): a second origin, such as https://dash-direct.example.com, that reaches dash without the proxy, for OAuth and bearer clients the proxy would send to its login page. When set, dash answers its host, it is the OAuth issuer and the base of the token, revoke and /.well-known documents and of the api and mcp resources, while /oauth/authorize stays on publicURL behind the proxy. An origin only (no path) on another host than publicURL's (a port does not tell them apart), https unless the host is loopback. The Helm chart sets it with routes.direct. Refused in auth.mode=none."`
	Listen        string              `json:"listen" dash:"restart" doc:"Address of the API and the web UI. The default 0.0.0.0:8080 accepts connections from any address, so keep authentication on (auth.mode local, proxy or oidc); behind a sidecar identity proxy set 127.0.0.1:8080. In dev and demo mode it defaults to 127.0.0.1:8080, and dev mode only accepts a loopback address."`
	HealthListen  string              `json:"healthListen" dash:"restart" doc:"Address of /healthz and /readyz. In dev and demo mode it defaults to 127.0.0.1:8081."`
	AllowedHosts  []string            `json:"allowedHosts" dash:"restart" doc:"Host names the API answers besides the host of publicURL: names or IP addresses without scheme or port. [\"*\"] accepts any. A JSON list in the environment."`
	CORSOrigins   []string            `json:"corsOrigins" dash:"restart" doc:"Origins of browser-based bearer clients (an MCP inspector, a web tool) that may call the API cross-origin, as scheme://host[:port] with no path; never \"*\". Empty (the default) sends no Access-Control-* header at all. A listed origin gets preflight answers and Access-Control-Allow-Origin on bearer requests and on 401s, never Access-Control-Allow-Credentials; requests from the web UI stay same-origin. A JSON list in the environment."`
	ReadyRequires []string            `json:"readyRequires" dash:"restart" doc:"Plugins /readyz waits for: each listed plugin must have finished starting (its first poll or cache sync) before dash reports ready, up to readyTimeout. Empty: /readyz needs only that dash is serving and every plugin's start has returned. Names are plugins.<id> sections, e.g. [\"kubernetes\"]. A JSON list in the environment."`
	ReadyTimeout  pluginconf.Duration `json:"readyTimeout" dash:"restart" doc:"How long /readyz waits for the readyRequires plugins, counted from startup. After that dash reports ready anyway and logs a warning naming the plugins still starting."`
	Metrics       bool                `json:"metrics" dash:"restart" doc:"Serve dash's own Prometheus metrics at /metrics on the health listener. Off by default. The listener is unauthenticated and the metrics name plugins and their states, so keep it cluster-internal."`
	CSP           string              `json:"csp" dash:"restart,enum=enforce|report-only|off" doc:"Whether dash sends a Content-Security-Policy header. enforce (the default): the browser blocks what the policy does not allow. report-only: the browser only reports violations in its console, so a page that the policy would break keeps working while you find the cause. off: no policy header at all, except on /sw.js and /offline.html, whose own policies are always enforced. This is the kill switch for a browser that blanks the web UI under enforce; X-Content-Type-Options and Referrer-Policy are sent in every mode. Prefer DASH_SERVER_CSP: an image rollback to a release before this setting only warns about the unknown variable, while it refuses an unknown key in a config file. In a YAML file quote the value (csp: \"off\"): a bare off is YAML false and is refused."`
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

// MCPConfig is the MCP server (design N). Its section is live: a change applies to the next
// request, without a restart.
type MCPConfig struct {
	Enabled bool `json:"enabled" doc:"Serve the MCP server at POST /mcp: read-only tools, resources and prompts for AI clients that present a token with the mcp audience. Off by default; when off, /mcp and /.well-known/oauth-protected-resource/mcp answer 404. Applies live and can be switched from the settings page. Never available with auth.mode none, where dash accepts no token."`
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
	// SecretNamespaces widens where a k8s: reference saved from the UI may point (design D16).
	SecretNamespaces []string `json:"secretNamespaces" dash:"restart" doc:"Kubernetes namespaces, besides the one dash runs in, that a k8s:<namespace>/<name>#<key> secret reference saved from the settings page may name. Empty: only dash's own namespace. References in the config file or the environment are the operator's and may name any namespace. It cannot be set from the settings page. A JSON list in the environment."`
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
	Kubernetes   pluginconf.Kubernetes   `json:"kubernetes" doc:"Kubernetes: how dash reaches and reads the cluster."`
	Flux         pluginconf.Flux         `json:"flux" doc:"Flux: how dash reads Flux objects."`
	CNPG         pluginconf.CNPG         `json:"cnpg" doc:"CloudNativePG: how dash reads the PostgreSQL clusters and the age of their last backup."`
	RustFS       pluginconf.RustFS       `json:"rustfs" doc:"RustFS: the buckets and capacity of the object store, read from Prometheus. Off unless enabled is true."`
}

// defaults is the built-in layer. Nothing site-specific is in it: an empty upstream URL or
// convention turns that source or feature off. The API listens on every address so a container
// port mapping reaches it. Dev and demo (DASH_DEV, DASH_DEMO) put the API and health listeners on
// loopback. Dev serves the UI from the Vite dev server, so its public URL points there; demo alone
// serves the embedded UI itself, so its public URL is the API listener (see demoPublicURL).
func defaults(dev, demo bool) Tree {
	t := Tree{
		Server: ServerConfig{
			Listen:       "0.0.0.0:8080",
			HealthListen: "0.0.0.0:8081",
			ReadyTimeout: pluginconf.Duration(60 * time.Second),
			CSP:          CSPEnforce,
			Limits: LimitsConfig{
				StreamsPerUser: 16, Streams: 256,
				TailsPerUser: 4, Tails: 32,
				TailMaxDuration: pluginconf.Duration(30 * time.Minute),
				SSEWriteTimeout: pluginconf.Duration(10 * time.Second),
				BodyReadTimeout: pluginconf.Duration(10 * time.Second),
				MaxHeaderBytes:  64 << 10,
			},
		},
		Auth:     DefaultAuth(),
		Settings: SettingsConfig{UI: SettingsEditable},
		DataDir:  "/data",
		Plugins: PluginsConfig{
			Prometheus:   pluginconf.DefaultPrometheus(),
			Alertmanager: pluginconf.DefaultAlertmanager(),
			VictoriaLogs: pluginconf.DefaultVictoriaLogs(),
			Gatus:        pluginconf.DefaultGatus(),
			Proxmox:      pluginconf.DefaultProxmox(),
			Hardware:     pluginconf.DefaultHardware(),
			Headlamp:     pluginconf.DefaultHeadlamp(),
			Kubernetes:   pluginconf.DefaultKubernetes(),
			Flux:         pluginconf.DefaultFlux(),
			CNPG:         pluginconf.DefaultCNPG(),
			RustFS:       pluginconf.DefaultRustFS(),
		},
	}
	if dev || demo {
		t.Server.Listen, t.Server.HealthListen = "127.0.0.1:8080", "127.0.0.1:8081"
	}
	switch {
	case dev:
		t.Server.PublicURL = "http://localhost:5173"
	case demo:
		t.Server.PublicURL = demoPublicURL(t.Server.Listen)
	}
	return t
}

// demoPublicURL is the default public URL of demo mode without dev mode: the embedded UI is
// served on the API listener, so the browser's Origin is that address and writes are refused
// as csrf_failed unless the public URL names it. A listener on every address is opened as
// 127.0.0.1, and an IPv6 zone is escaped as %25. An address it cannot name as a URL (one that
// does not parse, a named port, port 0) keeps the default before demo mode had its own,
// http://localhost:5173, so a default never adds a startup error: the listener reports a bad
// address itself.
func demoPublicURL(listen string) string {
	const fallback = "http://localhost:5173"
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fallback
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return fallback
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(strings.ReplaceAll(host, "%", "%25"), port)
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
	t.Server.CORSOrigins = normalizeOrigins(t.Server.CORSOrigins)
	t.Server.ReadyRequires = splitHosts(strings.Join(t.Server.ReadyRequires, ","))
	t.Auth.normalize()
	for i := range t.Plugins.Kubernetes.BackupJobs {
		j := &t.Plugins.Kubernetes.BackupJobs[i]
		j.Namespace, j.CronJob = strings.TrimSpace(j.Namespace), strings.TrimSpace(j.CronJob)
	}
}

// config maps the tree onto the flat view the rest of dash reads.
func (t Tree) config(dev, demo bool) Config {
	c := Config{
		Dev:           dev,
		Demo:          demo,
		Listen:        t.Server.Listen,
		HealthListen:  t.Server.HealthListen,
		PublicURL:     t.Server.PublicURL,
		DirectURL:     t.Server.DirectURL,
		AllowedHosts:  t.Server.AllowedHosts,
		CORSOrigins:   t.Server.CORSOrigins,
		ReadyRequires: t.Server.ReadyRequires,
		ReadyTimeout:  time.Duration(t.Server.ReadyTimeout),
		Metrics:       t.Server.Metrics,
		CSP:           t.Server.CSP,
		Limits: Limits{
			StreamsPerUser: t.Server.Limits.StreamsPerUser, Streams: t.Server.Limits.Streams,
			TailsPerUser: t.Server.Limits.TailsPerUser, Tails: t.Server.Limits.Tails,
			TailMaxDuration: time.Duration(t.Server.Limits.TailMaxDuration),
			SSEWriteTimeout: time.Duration(t.Server.Limits.SSEWriteTimeout),
			BodyReadTimeout: time.Duration(t.Server.Limits.BodyReadTimeout),
			MaxHeaderBytes:  t.Server.Limits.MaxHeaderBytes,
		},
		DataDir:     t.DataDir,
		SettingsUI:  t.Settings.UI,
		IconBaseURL: t.UI.IconBaseURL,
	}
	return c
}
