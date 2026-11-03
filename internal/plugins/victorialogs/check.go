package victorialogs

import (
	"context"
	"strings"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

var _ plugin.Checker = (*Plugin)(nil)

// checkName names the one probe of the connection check.
const checkName = "health"

// Check implements plugin.Checker: one read of /health with the section's TLS settings and
// credentials.
func (*Plugin) Check(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport {
	c, ok := cfg.(*pluginconf.VictoriaLogs)
	if !ok || c == nil || c.URL == "" {
		return plugin.CheckReport{Checks: []plugin.CheckResult{plugin.CheckNotConfigured(checkName)}}
	}
	hc := plugin.HTTPConfig{
		Timeout:            plugin.CheckTimeout,
		CAFile:             c.TLS.CAFile,
		InsecureSkipVerify: c.TLS.InsecureSkipVerify,
		Authorize:          authorize(c.Auth),
	}
	u := strings.TrimRight(c.URL, "/") + "/health"
	return plugin.CheckReport{Checks: []plugin.CheckResult{plugin.CheckRead(ctx, env, checkName, hc, u, nil)}}
}
