package hardware

import (
	"fmt"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

// seriesNames are the host.* series, in the order the manifest lists them.
var seriesNames = []string{"host.power", "host.cpuTemp", "host.cpu", "host.mem", "host.temps", "host.fans"}

// seriesSpecs declares the host.* series; expr builds the expression of one for the host's
// nodename. A SeriesSpec takes the validated arguments of the request, which carry nothing the
// host series need: they ask for the one host.
func seriesSpecs(expr func(name string) (string, error)) []plugin.SeriesSpec {
	out := make([]plugin.SeriesSpec, len(seriesNames))
	for i, name := range seriesNames {
		out[i] = plugin.SeriesSpec{Name: name, Expr: func(plugin.SeriesArgs) (string, error) { return expr(name) }}
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

// seriesExpr answers a series from the running plugin: its nodename is read at the call, so a
// node that is discovered later works at once. Before Start, with no host configured and while
// the nodename is unknown it is a bad series request (400), never an expression with an empty
// nodename that would match nothing and draw an empty chart.
func (p *Plugin) seriesExpr(name string) (string, error) {
	rt := p.rt.Load()
	if rt == nil {
		return "", fmt.Errorf("%w: the hardware plugin is not running", queries.ErrBadSeries)
	}
	if !rt.hasHost() {
		return "", fmt.Errorf("%w: no host is configured (plugins.hardware.hosts)", queries.ErrBadSeries)
	}
	n, ok := rt.nodename()
	if !ok {
		return "", fmt.Errorf("%w: the host's nodename is not known yet", queries.ErrBadSeries)
	}
	return hostSeries(rt.q, name, n)
}
