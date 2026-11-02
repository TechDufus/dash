package kubernetes

import (
	"errors"
	"testing"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/queries"
)

// seriesExpr is the expression of the named series, through the plugin's own spec table.
func seriesExpr(p *Plugin, name string, a plugin.SeriesArgs) (string, error) {
	for _, s := range p.seriesSpecs() {
		if s.Name == name {
			return s.Expr(a)
		}
	}
	return "", errors.New("no series " + name)
}

func TestSeriesExpressionsArePinned(t *testing.T) {
	e := newRestartEnv(t, pluginconf.Kubernetes{}, node("n1", "10.0.0.5", true))
	const inst = `"10.0.0.5:9100"`
	nodeArgs := plugin.SeriesArgs{Instance: "n1"}
	pod := plugin.SeriesArgs{Namespace: "media", Pod: "plex-0"}
	tests := []struct {
		name string
		args plugin.SeriesArgs
		want string
	}{
		{"node.cpu", nodeArgs, `1 - avg by (instance)(rate(node_cpu_seconds_total{job="node-exporter",instance=` + inst + `,mode="idle"}[2m]))`},
		{"node.mem", nodeArgs, `1 - node_memory_MemAvailable_bytes{job="node-exporter",instance=` + inst + `} / node_memory_MemTotal_bytes{job="node-exporter",instance=` + inst + `}`},
		{"pod.cpu", pod, `sum(rate(container_cpu_usage_seconds_total{metrics_path="/metrics/cadvisor",container!="",container!="POD",namespace="media",pod="plex-0"}[2m]))`},
		{"pod.mem", pod, `sum(container_memory_working_set_bytes{metrics_path="/metrics/cadvisor",container!="",container!="POD",namespace="media",pod="plex-0"})`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := seriesExpr(e.p, tc.name, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("expression\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSeriesAreDeclaredByName(t *testing.T) {
	var names []string
	for _, s := range (&Plugin{}).Manifest().Series {
		names = append(names, s.Name)
	}
	if len(names) != 4 || names[0] != "node.cpu" || names[1] != "node.mem" || names[2] != "pod.cpu" || names[3] != "pod.mem" {
		t.Fatalf("series = %v", names)
	}
}

// The node is asked for by name, and the plugin finds its address: a node the cache does not
// know is not found (404), not a bad request.
func TestSeriesUnknownNode(t *testing.T) {
	e := newRestartEnv(t, pluginconf.Kubernetes{}, node("n1", "10.0.0.5", true), node("no-ip", "", true))
	for _, name := range []string{"node.cpu", "node.mem"} {
		for _, unknown := range []string{"ghost", "n2", "no-ip"} {
			_, err := seriesExpr(e.p, name, plugin.SeriesArgs{Instance: unknown})
			if !errors.Is(err, plugin.ErrNotFound) || errors.Is(err, queries.ErrBadSeries) {
				t.Errorf("%s %q: err = %v, want plugin.ErrNotFound and not ErrBadSeries", name, unknown, err)
			}
		}
	}
	_, err := seriesExpr(e.p, "node.cpu", plugin.SeriesArgs{Instance: "ghost"})
	if err == nil || err.Error() != "unknown node" {
		t.Errorf("message = %v, want the bare unknown node (it is the 404 body)", err)
	}
}

// A node name that is not a DNS name is a bad request and is never looked up, even when the
// cache holds a node of that name.
func TestSeriesMalformedNodeIsNotResolved(t *testing.T) {
	e := newRestartEnv(t, pluginconf.Kubernetes{}, node("Weird_Node", "10.0.0.9", true))
	for _, name := range []string{"node.cpu", "node.mem"} {
		for _, bad := range []string{"Weird_Node", `n1"} or vector(1) #`, "10.0.0.5:9100", "a b", "../x", "-x"} {
			_, err := seriesExpr(e.p, name, plugin.SeriesArgs{Instance: bad})
			if !errors.Is(err, queries.ErrBadSeries) || errors.Is(err, plugin.ErrNotFound) {
				t.Errorf("%s %q: err = %v, want ErrBadSeries", name, bad, err)
			}
		}
	}
}

func TestSeriesBadArguments(t *testing.T) {
	e := newRestartEnv(t, pluginconf.Kubernetes{}, node("n1", "10.0.0.5", true))
	for _, tc := range []struct {
		name string
		args plugin.SeriesArgs
	}{
		{"node.cpu", plugin.SeriesArgs{}},
		{"node.mem", plugin.SeriesArgs{Namespace: "media", Pod: "plex-0"}},
		{"pod.cpu", plugin.SeriesArgs{}},
		{"pod.cpu", plugin.SeriesArgs{Namespace: "media"}},
		{"pod.cpu", plugin.SeriesArgs{Pod: "plex-0"}},
		{"pod.cpu", plugin.SeriesArgs{Namespace: "Media", Pod: "plex-0"}},
		{"pod.mem", plugin.SeriesArgs{Namespace: "media", Pod: `x"} or vector(1) #`}},
		{"pod.mem", plugin.SeriesArgs{Namespace: "media", Pod: "a/b"}},
		{"pod.mem", plugin.SeriesArgs{Instance: "n1"}},
	} {
		if got, err := seriesExpr(e.p, tc.name, tc.args); !errors.Is(err, queries.ErrBadSeries) {
			t.Errorf("%s %+v = %q, %v; want ErrBadSeries", tc.name, tc.args, got, err)
		}
	}
}

// Before Start there is no cluster to resolve a node in; the pod series need none.
func TestSeriesBeforeStart(t *testing.T) {
	p := &Plugin{}
	for _, name := range []string{"node.cpu", "node.mem"} {
		if _, err := seriesExpr(p, name, plugin.SeriesArgs{Instance: "n1"}); !errors.Is(err, queries.ErrBadSeries) {
			t.Errorf("%s before Start: err = %v, want ErrBadSeries", name, err)
		}
		if _, err := seriesExpr(p, name, plugin.SeriesArgs{}); !errors.Is(err, queries.ErrBadSeries) {
			t.Errorf("%s without a node: err = %v, want ErrBadSeries", name, err)
		}
	}
	if _, err := seriesExpr(p, "pod.cpu", plugin.SeriesArgs{Namespace: "media", Pod: "plex-0"}); err != nil {
		t.Errorf("pod.cpu before Start: %v", err)
	}
}
