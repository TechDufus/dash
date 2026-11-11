package world

import (
	"math"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// SSHIntervalSeconds is how often the fake SSH hosts sample, as plugins.ssh.intervalSeconds
// defaults.
const SSHIntervalSeconds = 5

// sshSpec is a fake SSH host: its inventory and the resting values of its readings.
type sshSpec struct {
	name   string
	info   sshmodel.Info
	upDays int64
	cpu    float64 // resting CPU ratio
	load   float64 // resting 1-minute load
	memGiB float64
	mem    float64 // resting used share of memory
	swap   float64 // swap size, GiB
	net    float64 // resting received bytes per second; sent is a third of it
	disk   float64 // resting read bytes per second; written is half of it
	// tempC is the resting CPU temperature; 0 reports none, as a VM does. hwmon says it comes
	// from a coretemp chip with sensors and fans; without it, from a thermal zone alone.
	tempC float64
	hwmon bool
}

// sshSpecs are the boxes the fake homelab reads over SSH: a NAS with a ZFS pool and containers,
// a Raspberry Pi, and a VM with a failed unit (an attention item on the Overview) whose docker
// refuses the SSH user.
var sshSpecs = []sshSpec{
	{
		name: "nas-1", upDays: 41, cpu: 0.14, load: 1.1, memGiB: 32, mem: 0.46, swap: 4, net: 2.6e6, disk: 4.2e6, tempC: 47, hwmon: true,
		info: sshmodel.Info{
			Hostname: "nas-1", OS: "Debian GNU/Linux 13 (trixie)", Kernel: "6.12.38+deb13-amd64", Arch: "x86_64",
			Vendor: "Example Systems", Product: "NS-4 storage server", CPUModel: "Intel(R) Xeon(R) E-2314 CPU @ 2.80GHz", CPUs: 4,
			Disks: []sshmodel.Disk{
				{Name: "nvme0n1", Model: "Example NVMe 500GB", SizeBytes: 500107862016, Kind: "nvme"},
				{Name: "sda", Model: "Example HDD 8TB", SizeBytes: 8001563222016, Kind: "hdd"},
				{Name: "sdb", Model: "Example HDD 8TB", SizeBytes: 8001563222016, Kind: "hdd"},
				{Name: "sdc", Model: "Example HDD 8TB", SizeBytes: 8001563222016, Kind: "hdd"},
				{Name: "sdd", Model: "Example HDD 8TB", SizeBytes: 8001563222016, Kind: "hdd"},
			},
			Filesystems: []sshmodel.Filesystem{
				{Mount: "/", Device: "/dev/nvme0n1p2", Type: "ext4", UsedBytes: gib(38), TotalBytes: gib(457)},
				{Mount: "/boot/efi", Device: "/dev/nvme0n1p1", Type: "vfat", UsedBytes: gib(0.01), TotalBytes: gib(0.5)},
				{Mount: "/tank", Device: "tank", Type: "zfs", UsedBytes: gib(9400), TotalBytes: gib(20800)},
			},
			Pools: []sshmodel.Pool{{Name: "tank", Health: "ONLINE", AllocBytes: gib(12530), SizeBytes: gib(29100)}},
			Containers: &sshmodel.Containers{Runtime: "docker", Items: []sshmodel.Container{
				{Name: "backup", Image: "ghcr.io/example/backup:2.4", State: "running", Status: "Up 6 days"},
				{Name: "media", Image: "ghcr.io/example/media:1.12", State: "running", Status: "Up 6 days"},
				{Name: "photos", Image: "ghcr.io/example/photos:3.1", State: "running", Status: "Up 2 days"},
				{Name: "sync", Image: "ghcr.io/example/sync:1.27", State: "running", Status: "Up 6 days"},
				{Name: "report-job", Image: "ghcr.io/example/report:0.9", State: "exited", Status: "Exited (0) 3 hours ago"},
			}},
			Services: &sshmodel.Services{Running: 31, Failed: []string{}},
		},
	},
	{
		name: "pi-1", upDays: 12, cpu: 0.09, load: 0.35, memGiB: 4, mem: 0.38, net: 3.1e5, disk: 6e4, tempC: 51,
		info: sshmodel.Info{
			Hostname: "pi-1", OS: "Debian GNU/Linux 12 (bookworm)", Kernel: "6.6.51+rpt-rpi-v8", Arch: "aarch64",
			Product: "Raspberry Pi 4 Model B Rev 1.4", CPUModel: "BCM2835", CPUs: 4,
			Disks: []sshmodel.Disk{{Name: "mmcblk0", SizeBytes: 63864569856, Kind: "ssd"}},
			Filesystems: []sshmodel.Filesystem{
				{Mount: "/", Device: "/dev/mmcblk0p2", Type: "ext4", UsedBytes: gib(9.6), TotalBytes: gib(58)},
				{Mount: "/boot/firmware", Device: "/dev/mmcblk0p1", Type: "vfat", UsedBytes: gib(0.07), TotalBytes: gib(0.5)},
			},
			Pools:    []sshmodel.Pool{},
			Services: &sshmodel.Services{Running: 22, Failed: []string{}},
		},
	},
	{
		name: "vm-1", upDays: 3, cpu: 0.27, load: 0.9, memGiB: 8, mem: 0.61, swap: 2, net: 8.4e5, disk: 1.3e6,
		info: sshmodel.Info{
			Hostname: "vm-1", OS: "Ubuntu 24.04.3 LTS", Kernel: "6.8.0-79-generic", Arch: "x86_64",
			Vendor: "QEMU", Product: "Standard PC (Q35 + ICH9, 2009)", Virtualization: "kvm",
			CPUModel: "QEMU Virtual CPU version 2.5+", CPUs: 4,
			Disks: []sshmodel.Disk{{Name: "vda", SizeBytes: 107374182400, Kind: "hdd"}},
			Filesystems: []sshmodel.Filesystem{
				{Mount: "/", Device: "/dev/vda1", Type: "ext4", UsedBytes: gib(71), TotalBytes: gib(97)},
			},
			Pools:      []sshmodel.Pool{},
			Containers: &sshmodel.Containers{Runtime: "docker", Denied: true, Items: []sshmodel.Container{}},
			Services:   &sshmodel.Services{Running: 27, Failed: []string{"backup-sync.service"}},
		},
	},
}

// SSHHosts are the hosts the ssh plugin's twin publishes, all of them up: their inventory and a
// sample that drifts with the clock. The twin adds the state's message and the health.
func (w *World) SSHHosts() []sshmodel.Host {
	now := w.clock()
	out := make([]sshmodel.Host, len(sshSpecs))
	for i, s := range sshSpecs {
		info := cloneSSHInfo(s.info)
		st := s.stats(now, w.start)
		out[i] = sshmodel.Host{
			Ref: plugin.Ref("ssh", "Host", s.name), Name: s.name, State: sshmodel.StateUp, Since: w.start.UTC(),
			Info: &info, Stats: &st,
		}
	}
	return out
}

// SSHHistory is the hour of samples of host name before the clock, one every
// SSHIntervalSeconds, false for a name that is no fake host. Like every builder it is a pure
// function of the clock.
func (w *World) SSHHistory(name string) (sshmodel.History, bool) {
	now := w.clock()
	for _, s := range sshSpecs {
		if s.name != name {
			continue
		}
		step := time.Duration(SSHIntervalSeconds) * time.Second
		end := now.Truncate(step)
		samples := make([]sshmodel.Sample, 0, int(time.Hour/step))
		for t := end.Add(-time.Hour + step); !t.After(end); t = t.Add(step) {
			samples = append(samples, s.sample(t))
		}
		return sshmodel.History{IntervalSeconds: SSHIntervalSeconds, Samples: samples}, true
	}
	return sshmodel.History{}, false
}

func (s sshSpec) key(k string) string { return "ssh/" + s.name + "/" + k }

func (s sshSpec) stats(now, start time.Time) sshmodel.Stats {
	mem, swap := gib(s.memGiB), gib(s.swap)
	load := math.Max(0, Around(s.key("load"), now, s.load, s.load/3))
	st := sshmodel.Stats{
		SampledAt:            now.UTC(),
		CPURatio:             clampRatio(Around(s.key("cpu"), now, s.cpu, s.cpu/2)),
		IOWaitRatio:          clampRatio(Around(s.key("iowait"), now, 0.01, 0.008)),
		Load1:                load,
		Load5:                load * 0.9,
		Load15:               load * 0.8,
		MemUsedBytes:         int64(float64(mem) * clampRatio(Around(s.key("mem"), now, s.mem, 0.03))),
		MemTotalBytes:        mem,
		SwapUsedBytes:        swap / 20,
		SwapTotalBytes:       swap,
		UptimeSeconds:        int64(now.Sub(start).Seconds()) + s.upDays*86400,
		NetRxBytesPerSec:     math.Max(0, Around(s.key("rx"), now, s.net, s.net*0.7)),
		NetTxBytesPerSec:     math.Max(0, Around(s.key("tx"), now, s.net/3, s.net/4)),
		DiskReadBytesPerSec:  math.Max(0, Around(s.key("read"), now, s.disk, s.disk*0.8)),
		DiskWriteBytesPerSec: math.Max(0, Around(s.key("write"), now, s.disk/2, s.disk/3)),
		Sensors:              []sshmodel.Sensor{},
		Fans:                 []sshmodel.Fan{},
	}
	if s.tempC > 0 {
		t := Around(s.key("temp"), now, s.tempC, 4)
		st.CPUTempC = &t
	}
	if s.hwmon {
		pkg := *st.CPUTempC
		st.Sensors = append(st.Sensors, sshmodel.Sensor{Chip: "coretemp", Label: "Package id 0", TempC: pkg})
		for i, name := range []string{"Core 0", "Core 1", "Core 2", "Core 3"} {
			st.Sensors = append(st.Sensors, sshmodel.Sensor{Chip: "coretemp", Label: name, TempC: pkg - 2 - float64(i)})
		}
		st.Sensors = append(st.Sensors, sshmodel.Sensor{Chip: "nvme", Label: "Composite", TempC: Around(s.key("nvme"), now, 41, 3)})
		st.Fans = append(st.Fans,
			sshmodel.Fan{Chip: "nct6775", Label: "fan1", RPM: int(Around(s.key("fan1"), now, 1100, 150))},
			sshmodel.Fan{Chip: "nct6775", Label: "fan2", RPM: int(Around(s.key("fan2"), now, 820, 120))})
	}
	return st
}

// sample is the history point of the host at t: the sample the stats would read then.
func (s sshSpec) sample(t time.Time) sshmodel.Sample {
	st := s.stats(t, t)
	out := sshmodel.Sample{
		T: t.Unix(), CPURatio: st.CPURatio, Load1: st.Load1,
		NetRxBytesPerSec: st.NetRxBytesPerSec, NetTxBytesPerSec: st.NetTxBytesPerSec,
		DiskReadBytesPerSec: st.DiskReadBytesPerSec, DiskWriteBytesPerSec: st.DiskWriteBytesPerSec,
		CPUTempC: st.CPUTempC,
	}
	if st.MemTotalBytes > 0 {
		out.MemRatio = float64(st.MemUsedBytes) / float64(st.MemTotalBytes)
	}
	return out
}

func clampRatio(v float64) float64 { return math.Min(1, math.Max(0, v)) }

// cloneSSHInfo copies a spec's inventory, so no payload shares a slice with the spec.
func cloneSSHInfo(in sshmodel.Info) sshmodel.Info {
	out := in
	out.Disks = append([]sshmodel.Disk{}, in.Disks...)
	out.Filesystems = append([]sshmodel.Filesystem{}, in.Filesystems...)
	out.Pools = append([]sshmodel.Pool{}, in.Pools...)
	if in.Containers != nil {
		c := *in.Containers
		c.Items = append([]sshmodel.Container{}, c.Items...)
		out.Containers = &c
	}
	if in.Services != nil {
		sv := *in.Services
		sv.Failed = append([]string{}, sv.Failed...)
		out.Services = &sv
	}
	return out
}
