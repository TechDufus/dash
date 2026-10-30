package queries

import (
	"errors"
	"testing"
	"time"
)

func TestSeries(t *testing.T) {
	a := Args{HostNodename: "pve-730", VMID: "2020", Instance: "10.0.20.20:9100", Namespace: "homepage", Pod: "homepage-abc"}
	tests := []struct {
		name string
		args Args
		want string
	}{
		{"host.power", a, `ipmi_power_watts{nodename="pve-730"}`},
		{"host.cpuTemp", a, `max(node_hwmon_temp_celsius{nodename="pve-730",chip=~"platform_coretemp_.*",sensor="temp1"})`},
		{"host.cpu", a, `1 - avg(rate(node_cpu_seconds_total{nodename="pve-730",mode="idle"}[2m]))`},
		{"host.mem", a, `1 - node_memory_MemAvailable_bytes{nodename="pve-730"} / node_memory_MemTotal_bytes{nodename="pve-730"}`},
		{"host.temps", a, `ipmi_temperature_celsius{nodename="pve-730"}`},
		{"host.fans", a, `ipmi_fan_speed_rpm{nodename="pve-730"}`},
		{"vm.cpu", a, `pve_cpu_usage_ratio{id="qemu/2020"}`},
		{"vm.mem", a, `pve_memory_usage_bytes{id="qemu/2020"} / pve_memory_size_bytes{id="qemu/2020"}`},
		{"vm.netIn", a, `rate(pve_network_receive_bytes_total{id="qemu/2020"}[2m])`},
		{"vm.netOut", a, `rate(pve_network_transmit_bytes_total{id="qemu/2020"}[2m])`},
		{"vm.diskRead", a, `rate(pve_disk_read_bytes_total{id="qemu/2020"}[2m])`},
		{"vm.diskWrite", a, `rate(pve_disk_written_bytes_total{id="qemu/2020"}[2m])`},
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
		{"vm.cpu", Args{VMID: ""}},
		{"vm.cpu", Args{VMID: "20x"}},
		{"vm.cpu", Args{VMID: `1"}or{`}},
		{"vm.cpu", Args{VMID: "1234567890"}},
		{"node.cpu", Args{}},
		{"pod.cpu", Args{Namespace: "ok"}},
		{"pod.mem", Args{Namespace: "Bad", Pod: "p"}},
		{"pod.mem", Args{Namespace: "ns", Pod: `p"}`}},
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
