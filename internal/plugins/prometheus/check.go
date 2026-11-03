package prometheus

import (
	"context"
	"strings"

	"github.com/TechDufus/dash/internal/plugin"
)

var _ plugin.Checker = (*Plugin)(nil)

// checkName names the one probe of the connection check.
const checkName = "query"

// Check implements plugin.Checker: one instant query of the constant 1, below any path prefix of
// the URL, with the section's TLS settings, headers and credentials.
func (*Plugin) Check(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport {
	c, ok := conf(cfg)
	if !ok || c.URL == "" {
		return plugin.CheckReport{Checks: []plugin.CheckResult{plugin.CheckNotConfigured(checkName)}}
	}
	hc := plugin.HTTPConfig{
		Timeout:            plugin.CheckTimeout,
		CAFile:             c.TLS.CAFile,
		InsecureSkipVerify: c.TLS.InsecureSkipVerify,
		Authorize:          authorizer(c.Auth, c.Headers),
	}
	u := strings.TrimRight(c.URL, "/") + "/api/v1/query?query=1"
	return plugin.CheckReport{Checks: []plugin.CheckResult{plugin.CheckRead(ctx, env, checkName, hc, u, nil)}}
}
