package ssh

import (
	"math"
	"slices"
	"strings"
	"time"

	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// Rates above these are a counter that jumped, not traffic: 100 GB/s is more than any link or
// array a homelab host has.
const (
	maxNetRate  = 100e9
	maxDiskRate = 100e9
)

// virtualNICs are the interface name prefixes that are never a host's own link: the fallback
// list of interfaces to sum skips them when no interface is a device (in a container, say).
var virtualNICs = []string{"lo", "veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "vxlan", "kube", "tun", "tap", "wg", "zt", "tailscale", "podman", "lxc"}

// counters are one frame's raw counters, which the next frame's rates are computed against.
type counters struct {
	at     time.Time // when dash read the frame
	uptime float64   // the host's seconds since boot: the clock of the rates
	cpu    cpuTimes
	net    map[string]ioCounters
	disk   map[string]ioCounters
}

// collector turns the frames of one connection into samples. A new connection starts a new
// collector, so its first sample has zero rates.
type collector struct {
	inv  *inventory
	prev *counters
}

// frame reads one frame: a new inventory when it carries one, and a sample. at is when dash
// read it.
func (c *collector) frame(f frame, at time.Time) sshmodel.Stats {
	if f.has("uname") {
		inv := parseInventory(f)
		c.inv = &inv
	}
	cur := &counters{
		at: at, uptime: parseUptime(f.sections["uptime"]), cpu: parseStat(f.sections["stat"]),
		net: parseNetDev(f.sections["netdev"]), disk: parseDiskstats(f.sections["diskstats"]),
	}
	mem := parseMeminfo(f.sections["meminfo"])
	st := sshmodel.Stats{
		SampledAt:      at.UTC(),
		MemUsedBytes:   mem.total - mem.available,
		MemTotalBytes:  mem.total,
		SwapUsedBytes:  mem.swapTotal - mem.swapFree,
		SwapTotalBytes: mem.swapTotal,
		UptimeSeconds:  int64(cur.uptime),
	}
	st.Load1, st.Load5, st.Load15 = parseLoad(f.sections["loadavg"])
	sensors, fans, cpuTemp := parseHwmon(f.sections["hwmon"])
	st.Sensors, st.Fans = sensors, fans
	if math.IsNaN(cpuTemp) {
		cpuTemp = parseThermal(f.sections["thermal"])
	}
	if !math.IsNaN(cpuTemp) {
		st.CPUTempC = &cpuTemp
	}
	if p := c.prev; p != nil {
		dt := cur.uptime - p.uptime
		if cur.uptime == 0 || p.uptime == 0 {
			dt = cur.at.Sub(p.at).Seconds()
		}
		// A host that rebooted between two frames has a smaller uptime and counters that started
		// again from zero: that sample has no rates.
		if dt > 0 {
			st.CPURatio, st.IOWaitRatio = cpuRatios(p.cpu, cur.cpu)
			st.NetRxBytesPerSec, st.NetTxBytesPerSec = rates(p.net, cur.net, c.nics(cur.net), dt, maxNetRate)
			var disks []string
			if c.inv != nil {
				disks = c.inv.disks
			}
			st.DiskReadBytesPerSec, st.DiskWriteBytesPerSec = rates(p.disk, cur.disk, disks, dt, maxDiskRate)
		}
	}
	c.prev = cur
	return st
}

// info is the last inventory read on this connection, nil before the first.
func (c *collector) info() *sshmodel.Info {
	if c.inv == nil {
		return nil
	}
	info := c.inv.info
	return &info
}

// nics are the interfaces whose traffic is the host's: the physical ones, or, when the host
// names none (a container, a VM whose NICs have no device link), every interface that is not a
// loopback, bridge, tunnel or container veth.
func (c *collector) nics(cur map[string]ioCounters) []string {
	if c.inv != nil && len(c.inv.nics) > 0 {
		return c.inv.nics
	}
	var out []string
	for name := range cur {
		if !slices.ContainsFunc(virtualNICs, func(p string) bool { return strings.HasPrefix(name, p) }) {
			out = append(out, name)
		}
	}
	return out
}

// cpuRatios are the busy and I/O wait shares of the CPU time between two readings, 0 to 1. A
// reading that went backwards (a reboot) gives zero.
func cpuRatios(prev, cur cpuTimes) (busy, iowait float64) {
	if !prev.ok || !cur.ok || cur.total <= prev.total || cur.idle < prev.idle || cur.iowait < prev.iowait {
		return 0, 0
	}
	total := float64(cur.total - prev.total)
	idle, wait := float64(cur.idle-prev.idle), float64(cur.iowait-prev.iowait)
	return clamp01((total - idle - wait) / total), clamp01(wait / total)
}

func clamp01(v float64) float64 {
	switch {
	case math.IsNaN(v) || v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

// rates sums the byte counters of names between two readings, per second. A counter that went
// backwards (a reset, a 32-bit wrap) adds nothing, and a sum above limit is a jump, not traffic:
// that direction reads zero.
func rates(prev, cur map[string]ioCounters, names []string, dt, limit float64) (in, out float64) {
	var dIn, dOut uint64
	for _, n := range names {
		p, okP := prev[n]
		c, okC := cur[n]
		if !okP || !okC {
			continue
		}
		if c.in >= p.in {
			dIn += c.in - p.in
		}
		if c.out >= p.out {
			dOut += c.out - p.out
		}
	}
	in, out = float64(dIn)/dt, float64(dOut)/dt
	if in > limit {
		in = 0
	}
	if out > limit {
		out = 0
	}
	return in, out
}

// sampleOf is a sample of the history: Stats cut down to what the charts draw.
func sampleOf(st sshmodel.Stats) sshmodel.Sample {
	s := sshmodel.Sample{
		T: st.SampledAt.Unix(), CPURatio: st.CPURatio, Load1: st.Load1,
		NetRxBytesPerSec: st.NetRxBytesPerSec, NetTxBytesPerSec: st.NetTxBytesPerSec,
		DiskReadBytesPerSec: st.DiskReadBytesPerSec, DiskWriteBytesPerSec: st.DiskWriteBytesPerSec,
	}
	if st.MemTotalBytes > 0 {
		s.MemRatio = clamp01(float64(st.MemUsedBytes) / float64(st.MemTotalBytes))
	}
	if st.CPUTempC != nil {
		t := *st.CPUTempC
		s.CPUTempC = &t
	}
	return s
}
