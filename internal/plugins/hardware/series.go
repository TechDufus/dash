package hardware

import (
	"fmt"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

// seriesNames are the host.* series, in the order the manifest lists them.
var seriesNames = []string{"host.power", "host.cpuTemp", "host.cpu", "host.mem", "host.temps", "host.fans"}

// seriesSpecs declares the host.* series; expr builds the expression of one from the request's
// arguments, whose node (Args.Instance) picks the host.
func seriesSpecs(expr func(name string, a plugin.SeriesArgs) (string, error)) []plugin.SeriesSpec {
	out := make([]plugin.SeriesSpec, len(seriesNames))
	for i, name := range seriesNames {
		out[i] = plugin.SeriesSpec{Name: name, Expr: func(a plugin.SeriesArgs) (string, error) { return expr(name, a) }}
	}
	return out
}

// hostSeries is the expression of the host series name for nodename. The caller has checked
// nodename.
func hostSeries(q exprs, name, nodename string) (string, error) {
	switch name {
	case "host.power":
		return q.power(nodename), nil
	case "host.cpuTemp":
		return q.cpuTempMax(nodename), nil
	case "host.cpu":
		return q.cpu(nodename), nil
	case "host.mem":
		return q.mem(nodename), nil
	case "host.temps":
		return q.sensors(nodename), nil
	case "host.fans":
		return q.fans(nodename), nil
	}
	return "", fmt.Errorf("%w: unknown series %q", queries.ErrBadSeries, name)
}

// seriesOf answers a series over the hosts that can be read (hosts) out of configured ones.
//
// The node argument picks a host: the first whose Proxmox node equals it, else whose nodename
// does, else whose name does, ignoring case. The API only lets lowercase DNS names through, so the
// web asks by node; a value that names no host is not found (404). Without the argument one
// configured host is that host, as it always was. With several, host.power is the sum of every
// host and host.cpuTemp the hottest package of all (the Overview tiles' sparks), and the other
// series draw one line per host, each with its nodename label. While no host's nodename is known
// it is a bad series request (400), never an expression with an empty nodename that would match
// nothing and draw an empty chart.
func seriesOf(q exprs, hosts []hostID, configured int, name string, a plugin.SeriesArgs) (string, error) {
	if !slices.Contains(seriesNames, name) {
		return "", fmt.Errorf("%w: unknown series %q", queries.ErrBadSeries, name)
	}
	if configured == 0 {
		return "", fmt.Errorf("%w: no host is configured (plugins.hardware.hosts)", queries.ErrBadSeries)
	}
	if a.Instance != "" {
		for _, field := range []func(hostID) string{
			func(h hostID) string { return h.node },
			func(h hostID) string { return h.nodename },
			func(h hostID) string { return h.name },
		} {
			for _, h := range hosts {
				if strings.EqualFold(field(h), a.Instance) {
					return hostSeries(q, name, h.nodename)
				}
			}
		}
		return "", plugin.NotFound("unknown host")
	}
	if configured == 1 {
		if len(hosts) == 0 {
			return "", fmt.Errorf("%w: the host's nodename is not known yet", queries.ErrBadSeries)
		}
		return hostSeries(q, name, hosts[0].nodename)
	}
	if len(hosts) == 0 {
		return "", fmt.Errorf("%w: no host's nodename is known yet", queries.ErrBadSeries)
	}
	nodenames := make([]string, len(hosts))
	for i, h := range hosts {
		nodenames[i] = h.nodename
	}
	switch name {
	case "host.power":
		return q.powerSum(nodenames), nil
	case "host.cpuTemp":
		return q.cpuTempMaxOf(nodenames), nil
	}
	return q.eachHost(name, nodenames), nil
}

// seriesExpr answers a series from the running plugin: the hosts are resolved at the call, so a
// node that is discovered later works at once.
func (p *Plugin) seriesExpr(name string, a plugin.SeriesArgs) (string, error) {
	rt := p.rt.Load()
	if rt == nil {
		return "", fmt.Errorf("%w: the hardware plugin is not running", queries.ErrBadSeries)
	}
	return seriesOf(rt.q, slices.DeleteFunc(rt.hostIDs(), func(h hostID) bool { return !h.runnable() }), len(rt.cfg.Hosts), name, a)
}
