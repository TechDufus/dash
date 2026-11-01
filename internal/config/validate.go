package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/pluginconf"
)

var metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// hostNameRE matches a DNS host name: dot-separated labels of lowercase letters, digits and hyphens.
var hostNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// validator collects every problem, each named by its path and the layer that set it, so an
// operator knows which variable or file to fix.
type validator struct {
	prov   map[string]Provenance
	errs   []error
	failed map[string]bool // the paths that already have a problem
}

// where says which layer set the value at path: " (env DASH_PLUGINS_PROMETHEUS_URL)", " (file /etc/dash/config.yaml)".
func where(prov map[string]Provenance, path string) string {
	p := prov[path]
	switch p.Source {
	case SourceEnv, SourceFile:
		return fmt.Sprintf(" (%s %s)", p.Source, p.Ref)
	case SourceUI:
		return " (UI settings)"
	}
	return ""
}

func (v *validator) fail(path, format string, args ...any) {
	if v.failed == nil {
		v.failed = map[string]bool{}
	}
	v.failed[path] = true
	v.errs = append(v.errs, fmt.Errorf("%s%s: %s", path, where(v.prov, path), fmt.Sprintf(format, args...)))
}

// section reports what a pluginconf section's Validate found, each problem at its full path
// under prefix. A setting the generic checks already rejected before the section ran is not
// reported twice; problems of the section itself at one path all are, as they were before the
// sections moved to pluginconf.
func (v *validator) section(prefix string, err error) {
	earlier := maps.Clone(v.failed)
	for _, fe := range pluginconf.Problems(err) {
		path := prefix
		if fe.Path != "" {
			path += "." + fe.Path
		}
		if !earlier[path] {
			v.fail(path, "%s", fe.Msg)
		}
	}
}

// validate reports settings that are missing or unusable.
func validate(t Tree, dev, demo bool, prov map[string]Provenance) error {
	v := &validator{prov: prov}
	s, p := t.Server, t.Plugins

	if s.PublicURL == "" && !dev && !demo {
		v.fail("server.publicURL", "is required (the URL users open, e.g. https://dash.example.com); set %s or server.publicURL in the config file", envName("server.publicURL"))
	}
	for _, l := range schema.leaves {
		if !l.url {
			continue
		}
		if u := l.field(&t).String(); u != "" && !httpURL(u) {
			v.fail(l.path, "%q must be an http(s) URL with a host, e.g. https://example.com", u)
		}
	}
	if dev && !loopbackAddr(s.Listen) {
		v.fail("server.listen", "DASH_DEV=true signs requests in as %q without authentication, so the listen address must be a loopback address such as 127.0.0.1:8080, not %q", "dev", s.Listen)
	}
	for _, h := range s.AllowedHosts {
		if h != "*" && net.ParseIP(h) == nil && !hostNameRE.MatchString(h) {
			v.fail("server.allowedHosts", "entry %q is not a host name or IP address (no scheme or port)", h)
		}
	}
	if s.ReadyTimeout <= 0 {
		v.fail("server.readyTimeout", "must be greater than zero")
	}
	known := map[string]bool{}
	for _, sec := range t.PluginSections() {
		known[sec.Name] = true
	}
	for _, id := range s.ReadyRequires {
		if !known[id] {
			v.fail("server.readyRequires", "entry %q is not a plugin (one of: %s)", id, strings.Join(slices.Sorted(maps.Keys(known)), ", "))
		}
	}
	validateLimits(v, s.Limits)
	if t.Settings.UI != SettingsEditable && t.Settings.UI != SettingsReadOnly {
		v.fail("settings.ui", "%q must be %q or %q", t.Settings.UI, SettingsEditable, SettingsReadOnly)
	}
	for _, sec := range t.PluginSections() {
		if sec.Type != sec.Name {
			v.fail("plugins."+sec.Name+".type", "%q must be empty or %q: dash runs one instance per plugin type, and the instance name is the type (design J.16)", sec.Type, sec.Name)
		}
	}

	px := p.Proxmox
	if (px.TokenID == "") != !px.TokenSecret.IsSet() {
		v.fail("plugins.proxmox.tokenSecret", "tokenID and tokenSecret must be set together")
	}
	if px.TokenID != "" && px.URL == "" {
		v.fail("plugins.proxmox.url", "is required with a Proxmox token")
	}

	if len(p.Hardware.Hosts) > 1 {
		v.fail("plugins.hardware.hosts", "has %d entries; one host is supported until the hardware plugin handles several", len(p.Hardware.Hosts))
	}
	if pre := p.Hardware.ZFSScrubMetricPrefix; pre != "" && !metricNameRE.MatchString(pre) { // empty hides the scrub panel
		v.fail("plugins.hardware.zfsScrubMetricPrefix", "%q is not a metric name", p.Hardware.ZFSScrubMetricPrefix)
	}

	if len(p.Kubernetes.BackupJobs) > 1 {
		v.fail("plugins.kubernetes.backupJobs", "has %d entries; one job is supported until G3", len(p.Kubernetes.BackupJobs))
	}
	for i, j := range p.Kubernetes.BackupJobs {
		if j.Namespace == "" || j.CronJob == "" {
			v.fail("plugins.kubernetes.backupJobs", "entry %d needs both namespace and cronjob", i+1)
		}
	}

	// Every section that knows its own cross-field rules says so (auth, header names, launcher
	// links, ...); the ones still in this package are checked above.
	v.section("ui", t.UI.Validate())
	for _, sec := range t.PluginSections() {
		if val, ok := sec.Value.(interface{ Validate() error }); ok {
			v.section("plugins."+sec.Name, val.Validate())
		}
	}
	return errors.Join(v.errs...)
}

// validateLimits needs every limit above zero (zero would turn the stream cap into "none may
// open", or net/http's default header size into a silent surprise) and a per-user cap that fits
// inside the total.
func validateLimits(v *validator, l LimitsConfig) {
	for _, c := range []struct {
		path string
		n    int64
	}{
		{"streamsPerUser", int64(l.StreamsPerUser)}, {"streams", int64(l.Streams)},
		{"tailsPerUser", int64(l.TailsPerUser)}, {"tails", int64(l.Tails)},
		{"tailMaxDuration", int64(l.TailMaxDuration)}, {"sseWriteTimeout", int64(l.SSEWriteTimeout)},
		{"bodyReadTimeout", int64(l.BodyReadTimeout)}, {"maxHeaderBytes", int64(l.MaxHeaderBytes)},
	} {
		if c.n <= 0 {
			v.fail("server.limits."+c.path, "must be greater than zero")
		}
	}
	if l.StreamsPerUser > l.Streams {
		v.fail("server.limits.streamsPerUser", "%d is more than server.limits.streams (%d)", l.StreamsPerUser, l.Streams)
	}
	if l.TailsPerUser > l.Tails {
		v.fail("server.limits.tailsPerUser", "%d is more than server.limits.tails (%d)", l.TailsPerUser, l.Tails)
	}
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

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
