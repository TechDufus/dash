package gatus

import (
	"net/url"
	"sort"
	"strings"

	"github.com/TechDufus/dash/internal/model"
)

// EndpointHealth is the health of one Gatus check: ok when it passes. A failing check is critical
// when its group is one of critical (compared without regard to case) and a warning otherwise,
// so with no critical groups every failure is a warning.
func EndpointHealth(group string, healthy bool, critical []string) model.Health {
	if healthy {
		return model.HealthOK
	}
	for _, g := range critical {
		if strings.EqualFold(g, group) {
			return model.HealthCrit
		}
	}
	return model.HealthWarn
}

// hostOf is the lowercased host name an endpoint's URL carries. Gatus reports the host its latest
// result probed: usually a bare name or address, which url.Parse reads as a path (or rejects, for
// host:port), so the whole string stands in for it then.
func hostOf(raw string) string {
	host := strings.ToLower(raw)
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		host = strings.ToLower(u.Hostname())
	}
	return host
}

// sorted returns a copy of eps ordered by group, then name, with every Results non-nil.
func sorted(eps []model.Endpoint) []model.Endpoint {
	out := append([]model.Endpoint{}, eps...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	for i := range out {
		if out[i].Results == nil {
			out[i].Results = []model.CheckResult{}
		}
	}
	return out
}
