package kubernetes

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

// nodeMemSel is the selector of one node-exporter instance on the memory gauges.
const nodeMemSel = `{job="node-exporter",instance=%s}`

// seriesSpecs are the four charts of the node and pod pages. The node series are asked for by
// the node's name: the plugin finds its address in its own node cache, so the API never has to
// know which IP a node-exporter instance carries. Before Start there is no cluster and no series.
func (p *Plugin) seriesSpecs() []plugin.SeriesSpec {
	return []plugin.SeriesSpec{
		{Name: "node.cpu", Expr: func(a plugin.SeriesArgs) (string, error) {
			instance, err := p.nodeInstance(a.Instance)
			if err != nil {
				return "", err
			}
			return strings.Replace(queryNodeCPU, `job="node-exporter",mode="idle"`,
				`job="node-exporter",instance=`+strconv.Quote(instance)+`,mode="idle"`, 1), nil
		}},
		{Name: "node.mem", Expr: func(a plugin.SeriesArgs) (string, error) {
			instance, err := p.nodeInstance(a.Instance)
			if err != nil {
				return "", err
			}
			sel := fmt.Sprintf(nodeMemSel, strconv.Quote(instance))
			return `1 - node_memory_MemAvailable_bytes` + sel + ` / node_memory_MemTotal_bytes` + sel, nil
		}},
		{Name: "pod.cpu", Expr: func(a plugin.SeriesArgs) (string, error) {
			sel, err := podSelector(a)
			if err != nil {
				return "", err
			}
			return `sum(rate(container_cpu_usage_seconds_total{metrics_path="/metrics/cadvisor",container!="",container!="POD",` + sel + `}[2m]))`, nil
		}},
		{Name: "pod.mem", Expr: func(a plugin.SeriesArgs) (string, error) {
			sel, err := podSelector(a)
			if err != nil {
				return "", err
			}
			return `sum(container_memory_working_set_bytes{metrics_path="/metrics/cadvisor",container!="",container!="POD",` + sel + `})`, nil
		}},
	}
}

// nodeInstance is the node-exporter instance label of the node called name. A name that is not
// a DNS name is a bad request and never looked up; a node the cache does not know is not found.
func (p *Plugin) nodeInstance(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: node required", queries.ErrBadSeries)
	}
	if !queries.ValidDNS(name) {
		return "", fmt.Errorf("%w: node is not a valid name", queries.ErrBadSeries)
	}
	r := p.rt.Load()
	if r == nil {
		return "", fmt.Errorf("%w: kubernetes is not running", queries.ErrBadSeries)
	}
	ip, ok := r.nodeAddress(name)
	if !ok {
		return "", plugin.NotFound("unknown node")
	}
	return kube.InstanceOf(ip), nil
}

// podSelector is the label matcher of one pod; both names have passed ValidDNS.
func podSelector(a plugin.SeriesArgs) (string, error) {
	if !queries.ValidDNS(a.Namespace) || !queries.ValidDNS(a.Pod) {
		return "", fmt.Errorf("%w: namespace and pod required", queries.ErrBadSeries)
	}
	return `namespace=` + strconv.Quote(a.Namespace) + `,pod=` + strconv.Quote(a.Pod), nil
}
