package ssh

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"

	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// Caps of what dash keeps of one host's inventory and sensors: enough for a large server, small
// enough that one host cannot swell the topic every viewer receives.
const (
	maxDisks       = 64
	maxFilesystems = 64
	maxPools       = 32
	maxContainers  = 200
	maxFailedUnits = 100
	maxSensors     = 64
	maxFans        = 32
	maxFieldBytes  = 256 // the longest name, model or status dash keeps
)

// clip cuts s to maxFieldBytes on a rune boundary and drops what is not printable.
func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0xfffd {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) <= maxFieldBytes {
		return s
	}
	for i := maxFieldBytes; i > 0; i-- {
		if !isContinuation(s[i]) {
			return s[:i]
		}
	}
	return ""
}

func isContinuation(b byte) bool { return b&0xc0 == 0x80 }

// cpuTimes are the aggregate jiffies of /proc/stat's cpu line.
type cpuTimes struct {
	total, idle, iowait uint64
	ok                  bool
}

// parseStat reads the aggregate "cpu" line: user nice system idle iowait irq softirq steal, then
// guest and guest_nice, which user and nice already count.
func parseStat(lines []string) cpuTimes {
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v [8]uint64
		n := 0
		for i := 1; i < len(f) && i <= 8; i++ {
			x, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return cpuTimes{}
			}
			v[i-1], n = x, i
		}
		var t cpuTimes
		for i := range n {
			t.total += v[i]
		}
		t.idle, t.iowait, t.ok = v[3], v[4], true
		return t
	}
	return cpuTimes{}
}

// parseLoad reads /proc/loadavg.
func parseLoad(lines []string) (l1, l5, l15 float64) {
	if len(lines) == 0 {
		return 0, 0, 0
	}
	f := strings.Fields(lines[0])
	if len(f) < 3 {
		return 0, 0, 0
	}
	return finite(f[0]), finite(f[1]), finite(f[2])
}

// finite parses a non-negative finite number; anything else is 0.
func finite(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

// memInfo is /proc/meminfo in bytes.
type memInfo struct {
	total, available, swapTotal, swapFree int64
}

// parseMeminfo reads /proc/meminfo. A kernel before 3.14 has no MemAvailable: free, buffers and
// cached stand in for it.
func parseMeminfo(lines []string) memInfo {
	kv := map[string]int64{}
	for _, l := range lines {
		k, rest, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil || n < 0 || n > math.MaxInt64/1024 {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		kv[k] = n
	}
	m := memInfo{total: kv["MemTotal"], swapTotal: kv["SwapTotal"], swapFree: kv["SwapFree"]}
	if v, ok := kv["MemAvailable"]; ok {
		m.available = v
	} else {
		m.available = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	m.available = min(m.available, m.total)
	m.swapFree = min(m.swapFree, m.swapTotal)
	return m
}

// parseUptime reads /proc/uptime: seconds since boot, with hundredths.
func parseUptime(lines []string) float64 {
	if len(lines) == 0 {
		return 0
	}
	f := strings.Fields(lines[0])
	if len(f) == 0 {
		return 0
	}
	return finite(f[0])
}

// ioCounters are the byte counters of one interface or one disk.
type ioCounters struct{ in, out uint64 }

// parseNetDev reads /proc/net/dev: "  eth0: rxbytes rxpackets ... txbytes ...", where the
// transmit bytes are the ninth number.
func parseNetDev(lines []string) map[string]ioCounters {
	out := map[string]ioCounters{}
	for _, l := range lines {
		name, rest, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[strings.TrimSpace(name)] = ioCounters{in: rx, out: tx}
	}
	return out
}

// sectorBytes is the unit of /proc/diskstats' sector counts, whatever the disk's own sector size.
const sectorBytes = 512

// parseDiskstats reads /proc/diskstats: major minor name reads merged sectorsRead ms writes
// merged sectorsWritten ..., in bytes.
func parseDiskstats(lines []string) map[string]ioCounters {
	out := map[string]ioCounters{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 10 {
			continue
		}
		r, err1 := strconv.ParseUint(f[5], 10, 64)
		w, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil || r > math.MaxUint64/sectorBytes || w > math.MaxUint64/sectorBytes {
			continue
		}
		out[f[2]] = ioCounters{in: r * sectorBytes, out: w * sectorBytes}
	}
	return out
}

// Plausible readings: a temperature outside them is a placeholder or a broken sensor (127 °C and
// -273 °C are common; no part runs above 125 °C), a fan above them is a misread.
const (
	minTempC  = -40
	maxTempC  = 125
	maxFanRPM = 100000
)

// cpuChips are the hwmon chips whose temperatures are the CPU's.
var cpuChips = []string{"coretemp", "k10temp", "k8temp", "zenpower", "cpu_thermal", "cpu-thermal", "soc_thermal", "cpu0_thermal", "via_cputemp"}

// parseHwmon reads the hwmon section: "chip <hwmonN> <name>" lines, each followed by
// "<input> <value> [label]" lines (temperatures in millidegrees, fans in RPM). It returns every
// plausible reading, and the hottest reading of a CPU chip (NaN without one).
func parseHwmon(lines []string) ([]sshmodel.Sensor, []sshmodel.Fan, float64) {
	sensors, fans := []sshmodel.Sensor{}, []sshmodel.Fan{}
	cpu, chip := math.NaN(), ""
	for _, l := range lines {
		f := strings.SplitN(l, " ", 3)
		if f[0] == "chip" {
			chip = ""
			if len(f) >= 3 {
				chip = clip(f[2])
			}
			continue
		}
		if chip == "" || len(f) < 2 {
			continue
		}
		input, label := f[0], ""
		if len(f) > 2 {
			label = clip(f[2])
		}
		v, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(input, "temp"):
			c := float64(v) / 1000
			if c < minTempC || c > maxTempC || len(sensors) >= maxSensors {
				continue
			}
			sensors = append(sensors, sshmodel.Sensor{Chip: chip, Label: cmp.Or(label, clip(input)), TempC: c})
			if slices.Contains(cpuChips, chip) && (math.IsNaN(cpu) || c > cpu) {
				cpu = c
			}
		case strings.HasPrefix(input, "fan"):
			// A fan at 0 RPM is most often an empty header; a board reports several.
			if v <= 0 || v > maxFanRPM || len(fans) >= maxFans {
				continue
			}
			fans = append(fans, sshmodel.Fan{Chip: chip, Label: cmp.Or(label, clip(input)), RPM: int(v)})
		}
	}
	return sensors, fans, cpu
}

// parseThermal reads the thermal section, "<zone> <millidegrees> <type>", and returns the
// hottest zone that is the CPU's or the SoC's (NaN without one): what a board without a hwmon
// CPU chip reports, a Raspberry Pi for one.
func parseThermal(lines []string) float64 {
	cpu := math.NaN()
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		v, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		typ := strings.ToLower(f[2])
		if !strings.Contains(typ, "cpu") && !strings.Contains(typ, "soc") && typ != "x86_pkg_temp" {
			continue
		}
		if c := float64(v) / 1000; c >= minTempC && c <= maxTempC && (math.IsNaN(cpu) || c > cpu) {
			cpu = c
		}
	}
	return cpu
}

// inventory is what an inventory frame says, with what the rates need besides the published
// Info: the whole disks and the physical interfaces.
type inventory struct {
	info  sshmodel.Info
	disks []string // names of the whole disks
	nics  []string // names of the physical interfaces
}

// kernel is uname -s, "Linux" on a host dash supports.
func kernelOf(f frame) string {
	if l := f.sections["uname"]; len(l) > 0 {
		return strings.TrimSpace(l[0])
	}
	return ""
}

// placeholders are what firmware writes into the DMI fields it was not given.
var placeholders = []string{"to be filled by o.e.m.", "system manufacturer", "system product name", "default string", "not specified", "not applicable", "none", "o.e.m.", "oem"}

func dmiValue(s string) string {
	s = clip(s)
	if slices.Contains(placeholders, strings.ToLower(s)) {
		return ""
	}
	return s
}

// parseInventory reads an inventory frame.
func parseInventory(f frame) inventory {
	var inv inventory
	info := &inv.info
	if u := f.sections["uname"]; len(u) >= 4 {
		info.Kernel, info.Arch, info.Hostname = clip(u[1]), clip(u[2]), clip(u[3])
	}
	for _, l := range f.sections["osrelease"] {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			info.OS = clip(unquote(v))
		}
	}
	var hardware, model string
	for _, l := range f.sections["cpuinfo"] {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), clip(v)
		switch k {
		case "processor":
			info.CPUs++
		case "model name", "cpu model":
			if info.CPUModel == "" {
				info.CPUModel = v
			}
		case "Hardware":
			hardware = v
		case "Model":
			model = v
		}
	}
	if info.CPUModel == "" {
		info.CPUModel = hardware
	}
	for _, l := range f.sections["dmi"] {
		k, v, _ := strings.Cut(l, " ")
		switch k {
		case "sys_vendor":
			info.Vendor = dmiValue(v)
		case "product_name":
			info.Product = dmiValue(v)
		}
	}
	if info.Vendor == "" && info.Product == "" {
		dt := clip(strings.Join(f.sections["dtmodel"], " "))
		info.Product = cmp.Or(dt, model)
	}
	if v := f.sections["virt"]; len(v) > 0 {
		if s := clip(v[0]); s != "none" {
			info.Virtualization = s
		}
	}
	info.Disks, inv.disks = parseBlock(f.sections["block"])
	inv.nics = []string{}
	for _, l := range f.sections["nic"] {
		if n := strings.TrimSpace(l); n != "" && len(inv.nics) < 256 {
			inv.nics = append(inv.nics, n)
		}
	}
	info.Filesystems = parseDF(f.sections["df"], parseMounts(f.sections["mounts"]))
	info.Pools = parseZpool(f.sections["zpool"])
	info.Containers = parseContainers(f)
	info.Services = parseServices(f)
	return inv
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// parseBlock reads "<name> <sectors> <rotational> <model>" lines of the disks that are devices.
func parseBlock(lines []string) ([]sshmodel.Disk, []string) {
	disks, names := []sshmodel.Disk{}, []string{}
	for _, l := range lines {
		f := strings.SplitN(l, " ", 4)
		if len(f) < 3 || f[0] == "" || len(disks) >= maxDisks {
			continue
		}
		sectors, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || sectors < 0 || sectors > math.MaxInt64/sectorBytes {
			sectors = 0
		}
		d := sshmodel.Disk{Name: clip(f[0]), SizeBytes: sectors * sectorBytes}
		if len(f) == 4 {
			d.Model = clip(f[3])
		}
		switch {
		case strings.HasPrefix(d.Name, "nvme"):
			d.Kind = "nvme"
		case f[2] == "1":
			d.Kind = "hdd"
		case f[2] == "0":
			d.Kind = "ssd"
		}
		disks = append(disks, d)
		names = append(names, f[0])
	}
	return disks, names
}

// unescapeMount undoes the octal escapes of /proc/mounts (\040 is a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseMounts reads /proc/self/mounts: the file system type of each mount point (the last mount
// on a point wins, as it is the one df sees).
func parseMounts(lines []string) map[string]string {
	out := map[string]string{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 3 {
			out[unescapeMount(f[1])] = f[2]
		}
	}
	return out
}

// virtualFS are the file system types that hold no data of their own.
var virtualFS = []string{
	"tmpfs", "devtmpfs", "overlay", "squashfs", "proc", "sysfs", "cgroup", "cgroup2", "devpts", "mqueue",
	"efivarfs", "autofs", "fuse.lxcfs", "nsfs", "tracefs", "debugfs", "securityfs", "pstore", "bpf", "configfs",
	"ramfs", "hugetlbfs", "fusectl", "binfmt_misc", "rpc_pipefs", "selinuxfs", "fuse.gvfsd-fuse", "fuse.portal", "nfsd",
	"rootfs", "zram", "iso9660",
}

// parseDF reads df -Pk ("<device> <1024-blocks> <used> <available> <capacity> <mount>") and keeps
// the file systems that hold data, each device once (a bind mount repeats one), shortest mount
// first.
func parseDF(lines []string, types map[string]string) []sshmodel.Filesystem {
	out := []sshmodel.Filesystem{}
	seen := map[string]int{}
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 6 || f[0] == "Filesystem" {
			continue
		}
		total, err1 := strconv.ParseInt(f[1], 10, 64)
		used, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || total <= 0 || used < 0 || total > math.MaxInt64/1024 || used > total {
			continue
		}
		mount := strings.Join(f[5:], " ")
		typ := types[mount]
		if slices.Contains(virtualFS, typ) || strings.HasPrefix(mount, "/proc") || strings.HasPrefix(mount, "/sys") ||
			strings.HasPrefix(mount, "/dev") || strings.HasPrefix(mount, "/run") {
			continue
		}
		fs := sshmodel.Filesystem{Mount: clip(mount), Device: clip(f[0]), Type: clip(typ), UsedBytes: used * 1024, TotalBytes: total * 1024}
		if i, dup := seen[f[0]]; dup && f[0] != "none" {
			if len(fs.Mount) < len(out[i].Mount) {
				out[i] = fs
			}
			continue
		}
		if len(out) >= maxFilesystems {
			continue
		}
		seen[f[0]] = len(out)
		out = append(out, fs)
	}
	slices.SortStableFunc(out, func(a, b sshmodel.Filesystem) int { return cmp.Compare(a.Mount, b.Mount) })
	return out
}

// parseZpool reads zpool list -Hp -o name,health,allocated,size (tab-separated, bytes).
func parseZpool(lines []string) []sshmodel.Pool {
	out := []sshmodel.Pool{}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) < 4 || len(out) >= maxPools {
			continue
		}
		alloc, err1 := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		size, err2 := strconv.ParseInt(strings.TrimSpace(f[3]), 10, 64)
		if err1 != nil || err2 != nil || size < 0 || alloc < 0 {
			continue
		}
		out = append(out, sshmodel.Pool{Name: clip(f[0]), Health: clip(f[1]), AllocBytes: alloc, SizeBytes: size})
	}
	return out
}

// parseContainers reads the docker or podman section: tab-separated name, image, state and
// status, and the runtime's -denied marker when it refused the SSH user. Nil without either.
func parseContainers(f frame) *sshmodel.Containers {
	for _, rt := range []string{"docker", "podman"} {
		lines, ok := f.sections[rt]
		if !ok {
			continue
		}
		c := &sshmodel.Containers{Runtime: rt, Denied: f.has(rt + "-denied"), Items: []sshmodel.Container{}}
		if c.Denied {
			return c
		}
		for _, l := range lines {
			p := strings.Split(l, "\t")
			if len(p) < 4 || len(c.Items) >= maxContainers {
				continue
			}
			c.Items = append(c.Items, sshmodel.Container{Name: clip(p[0]), Image: clip(p[1]), State: strings.ToLower(clip(p[2])), Status: clip(p[3])})
		}
		slices.SortStableFunc(c.Items, func(a, b sshmodel.Container) int {
			if ar, br := a.State == "running", b.State == "running"; ar != br {
				if ar {
					return -1
				}
				return 1
			}
			return cmp.Compare(a.Name, b.Name)
		})
		return c
	}
	return nil
}

// parseServices reads the running and failed sections of systemctl list-units --plain: the unit
// is the first field. Nil when the host does not run systemd (the script prints neither).
func parseServices(f frame) *sshmodel.Services {
	if !f.has("running") && !f.has("failed") {
		return nil
	}
	s := &sshmodel.Services{Failed: []string{}}
	for _, l := range f.sections["running"] {
		if strings.TrimSpace(l) != "" {
			s.Running++
		}
	}
	for _, l := range f.sections["failed"] {
		if u := strings.Fields(l); len(u) > 0 && len(s.Failed) < maxFailedUnits {
			s.Failed = append(s.Failed, clip(u[0]))
		}
	}
	slices.Sort(s.Failed)
	return s
}
