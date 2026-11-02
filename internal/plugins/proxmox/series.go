package proxmox

import (
	"fmt"
	"strings"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/queries"
)

// seriesSpecs are the six VM charts. Each expression is built from the settings of the instance
// that runs (plugins.proxmox.series.vmSelector), read through p, so a reload or the demo twin
// answers with its own. Before Start there is no instance and no series.
func (p *Plugin) seriesSpecs() []plugin.SeriesSpec {
	build := func(name string, expr func(sel string) string) plugin.SeriesSpec {
		return plugin.SeriesSpec{Name: name, Expr: func(a plugin.SeriesArgs) (string, error) {
			r := p.rt.Load()
			if r == nil {
				return "", fmt.Errorf("%w: proxmox is not running", queries.ErrBadSeries)
			}
			sel, err := vmMatcher(r.cfg.VMSelector(), a.VMID)
			if err != nil {
				return "", err
			}
			return expr(sel), nil
		}}
	}
	return []plugin.SeriesSpec{
		build("vm.cpu", func(s string) string { return `pve_cpu_usage_ratio{` + s + `}` }),
		build("vm.mem", func(s string) string {
			return `pve_memory_usage_bytes{` + s + `} / pve_memory_size_bytes{` + s + `}`
		}),
		build("vm.netIn", func(s string) string { return `rate(pve_network_receive_bytes_total{` + s + `}[2m])` }),
		build("vm.netOut", func(s string) string { return `rate(pve_network_transmit_bytes_total{` + s + `}[2m])` }),
		build("vm.diskRead", func(s string) string { return `rate(pve_disk_read_bytes_total{` + s + `}[2m])` }),
		build("vm.diskWrite", func(s string) string { return `rate(pve_disk_written_bytes_total{` + s + `}[2m])` }),
	}
}

// vmMatcher puts the VM id into the selector. The id has passed ValidVMID, so it is digits only,
// and the selector has passed Validate when the configuration was loaded; a selector that does
// not pass it here is refused too, so nothing but one matcher can reach a query.
func vmMatcher(selector, vmid string) (string, error) {
	if !queries.ValidVMID(vmid) {
		return "", fmt.Errorf("%w: vmid must be 1-9 digits", queries.ErrBadSeries)
	}
	if err := pluginconf.ValidVMSelector(selector); err != nil {
		return "", fmt.Errorf("%w: plugins.proxmox.series.vmSelector: %v", queries.ErrBadSeries, err)
	}
	return strings.Replace(selector, "{vmid}", vmid, 1), nil
}
