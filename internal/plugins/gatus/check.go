package gatus

import (
	"context"
	"strings"

	"github.com/TechDufus/dash/internal/plugin"
)

var _ plugin.Checker = (*Plugin)(nil)

// checkName names the one probe of the connection check.
const checkName = "statuses"

// Check implements plugin.Checker: one read of the first page of endpoint statuses with the
// section's TLS settings and credentials.
func (*Plugin) Check(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport {
	c, err := settings(cfg)
	if err != nil || c.URL == "" {
		return plugin.CheckReport{Checks: []plugin.CheckResult{plugin.CheckNotConfigured(checkName)}}
	}
	hc := plugin.HTTPConfig{
		Timeout:            plugin.CheckTimeout,
		CAFile:             c.TLS.CAFile,
		InsecureSkipVerify: c.TLS.InsecureSkipVerify,
		Authorize:          authorize(c.Auth),
	}
	u := strings.TrimRight(c.URL, "/") + "/api/v1/endpoints/statuses?page=1"
	return plugin.CheckReport{Checks: []plugin.CheckResult{plugin.CheckRead(ctx, env, checkName, hc, u, nil)}}
}
