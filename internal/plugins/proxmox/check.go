package proxmox

import (
	"context"
	"net/http"
	"strings"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
)

var _ plugin.Checker = (*Plugin)(nil)

// checkName names the one probe of the connection check.
const checkName = "version"

// Check implements plugin.Checker: one read of /api2/json/version with the section's TLS
// settings and the API token, sent in the header upstream.NewProxmox sends. Without a URL or
// either half of the token there is nothing to check.
func (*Plugin) Check(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport {
	c, err := settings(cfg)
	if err != nil || c.URL == "" || c.TokenID == "" || !c.TokenSecret.IsSet() {
		return plugin.CheckReport{Checks: []plugin.CheckResult{plugin.CheckNotConfigured(checkName)}}
	}
	hc := plugin.HTTPConfig{
		Timeout:            plugin.CheckTimeout,
		CAFile:             c.TLS.CAFile,
		InsecureSkipVerify: c.TLS.InsecureSkipVerify,
		Authorize:          tokenAuthorizer(c.TokenID, c.TokenSecret),
	}
	u := strings.TrimRight(c.URL, "/") + "/api2/json/version"
	return plugin.CheckReport{Checks: []plugin.CheckResult{plugin.CheckRead(ctx, env, checkName, hc, u, nil)}}
}

// tokenAuthorizer sets the Authorization header of upstream.Proxmox, reading the secret per
// request.
func tokenAuthorizer(id string, s secret.Value) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "PVEAPIToken="+id+"="+s.Reveal())
	}
}
