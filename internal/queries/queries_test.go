package queries

import (
	"errors"
	"testing"
	"time"
)

func TestSeries(t *testing.T) {
	a := Args{VMID: "2020", Instance: "10.0.20.20:9100", Namespace: "homepage", Pod: "homepage-abc"}
	tests := []struct {
		name string
		args Args
		want string
	}{
		{"node.cpu", a, `1 - avg by (instance)(rate(node_cpu_seconds_total{job="node-exporter",instance="10.0.20.20:9100",mode="idle"}[2m]))`},
		{"node.mem", a, `1 - node_memory_MemAvailable_bytes{job="node-exporter",instance="10.0.20.20:9100"} / node_memory_MemTotal_bytes{job="node-exporter",instance="10.0.20.20:9100"}`},
		{"pod.cpu", a, `sum(rate(container_cpu_usage_seconds_total{metrics_path="/metrics/cadvisor",container!="",container!="POD",namespace="homepage",pod="homepage-abc"}[2m]))`},
		{"pod.mem", a, `sum(container_memory_working_set_bytes{metrics_path="/metrics/cadvisor",container!="",container!="POD",namespace="homepage",pod="homepage-abc"})`},
	}
	for _, tc := range tests {
		got, err := Series(tc.name, tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%s:\n got %q (err %v)\nwant %q", tc.name, got, err, tc.want)
		}
	}
}

func TestSeriesRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args Args
	}{
		{"nope", Args{}},
		{"node.cpu", Args{}},
		{"pod.cpu", Args{Namespace: "ok"}},
		{"pod.mem", Args{Namespace: "Bad", Pod: "p"}},
		{"pod.mem", Args{Namespace: "ns", Pod: `p"}`}},
		{"host.power", Args{}}, // the host series belong to the hardware plugin
		{"host.cpu", Args{}},
	}
	for _, tc := range tests {
		if _, err := Series(tc.name, tc.args); !errors.Is(err, ErrBadSeries) {
			t.Errorf("%s %+v: err = %v, want ErrBadSeries", tc.name, tc.args, err)
		}
	}
}

func TestRange(t *testing.T) {
	tests := []struct {
		name         string
		window, step time.Duration
		ok           bool
	}{
		{"15m", 15 * time.Minute, 15 * time.Second, true},
		{"1h", time.Hour, 30 * time.Second, true},
		{"6h", 6 * time.Hour, 2 * time.Minute, true},
		{"24h", 24 * time.Hour, 5 * time.Minute, true},
		{"7d", 7 * 24 * time.Hour, 30 * time.Minute, true},
		{"2d", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tc := range tests {
		w, s, ok := Range(tc.name)
		if w != tc.window || s != tc.step || ok != tc.ok {
			t.Errorf("Range(%q) = %v %v %v", tc.name, w, s, ok)
		}
	}
}
