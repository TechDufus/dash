package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/TechDufus/dash/internal/model"
)

var metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// hostNameRE matches a DNS host name: dot-separated labels of lowercase letters, digits and hyphens.
var hostNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// validator collects every problem, each named by its path and the layer that set it, so an
// operator knows which variable or file to fix.
type validator struct {
	prov map[string]Provenance
	errs []error
}

// where says which layer set the value at path: " (env DASH_VM_URL)", " (file /etc/dash/config.yaml)".
func (v *validator) where(path string) string {
	p := v.prov[path]
	switch p.Source {
	case SourceEnv, SourceFile:
		return fmt.Sprintf(" (%s %s)", p.Source, p.Ref)
	case SourceUI:
		return " (UI settings)"
	}
	return ""
}

func (v *validator) fail(path, format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s%s: %s", path, v.where(path), fmt.Sprintf(format, args...)))
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
	if t.Settings.UI != SettingsEditable && t.Settings.UI != SettingsReadOnly {
		v.fail("settings.ui", "%q must be %q or %q", t.Settings.UI, SettingsEditable, SettingsReadOnly)
	}

	px := p.Proxmox
	if (px.TokenID == "") != (px.TokenSecret == "") {
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

	if p.Launcher.File != "" && len(p.Launcher.Links) > 0 {
		v.fail("plugins.launcher.links", "cannot be set together with plugins.launcher.file")
	}
	if err := validateLinks(p.Launcher.Links); err != nil {
		v.fail("plugins.launcher.links", "%v", err)
	}
	return errors.Join(v.errs...)
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

// validateLinks checks launcher entries; the error counts from 1.
func validateLinks(links []model.LauncherLink) error {
	for i, l := range links {
		u, err := url.Parse(l.Href)
		if l.Name == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("entry %d needs a name and an http(s) href", i+1)
		}
	}
	return nil
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
