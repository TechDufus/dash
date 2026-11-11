package ssh

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// testNonce is the marker nonce of the fixtures.
const testNonce = "0123456789abcdef0123456789abcdef"

// frameText renders sections (in the order given, "name\nline\nline") as one frame of the
// collector script.
func frameText(nonce string, seq int, sections ...string) string {
	var b strings.Builder
	m := "@@" + nonce
	b.WriteString(m + " frame " + strconv.Itoa(seq) + "\n")
	for _, s := range sections {
		name, body, _ := strings.Cut(s, "\n")
		b.WriteString(m + " " + name + "\n")
		if body != "" {
			b.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString(m + " end\n")
	return b.String()
}

// parseFrame reads one frame of text.
func parseFrame(t *testing.T, text string) frame {
	t.Helper()
	f, err := newFrameReader(strings.NewReader(text), testNonce).next()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// fastSections are the fast sections of a host whose counters have moved on by step: the CPU,
// network and disk counters grow by fixed amounts per step, and the uptime by 5 s.
func fastSections(step int, hwmon, thermal string) []string {
	n := uint64(step)
	u := func(v uint64) string { return strconv.Itoa(int(v)) }
	return []string{
		"stat\ncpu  " + u(1000+n*300) + " 10 " + u(500+n*100) + " " + u(80000+n*500) + " " + u(200+n*100) + " 0 5 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10",
		"loadavg\n0.52 0.58 0.59 1/234 5678",
		"meminfo\nMemTotal:       16000000 kB\nMemFree:         2000000 kB\nMemAvailable:    8000000 kB\nBuffers:          100000 kB\nCached:          4000000 kB\nSwapTotal:       2000000 kB\nSwapFree:        1500000 kB",
		"uptime\n" + u(1000+n*5) + ".25 3000.00",
		"netdev\nInter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n    lo: 999999 10 0 0 0 0 0 0 999999 10 0 0 0 0 0 0\nenp1s0: " + u(1_000_000+n*500_000) + " 100 0 0 0 0 0 0 " + u(2_000_000+n*100_000) + " 50 0 0 0 0 0 0\ndocker0: " + u(n*9_000_000) + " 1 0 0 0 0 0 0 " + u(n*9_000_000) + " 1 0 0 0 0 0 0\nwlan0: 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0",
		"diskstats\n 259       0 nvme0n1 100 0 " + u(2000+n*1000) + " 10 50 0 " + u(4000+n*2000) + " 20 0 30 30\n 259       1 nvme0n1p1 100 0 " + u(2000+n*1000) + " 10 50 0 " + u(4000+n*2000) + " 20 0 30 30\n   8       0 sda 1 0 " + u(n*100) + " 1 0 0 0 0 0 0 0\n   7       0 loop0 1 0 " + u(n*999999) + " 1 0 0 0 0 0 0 0",
		hwmon,
		thermal,
	}
}

// debianInventory is a Debian bare-metal x86_64 host with coretemp, docker allowed, systemd, no
// ZFS, a tmpfs, an overlay and a bind mount.
var debianInventory = []string{
	"uname\nLinux\n6.12.38+deb13-amd64\nx86_64\nalpha",
	"osrelease\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"13\"",
	"cpuinfo\nprocessor\t: 0\nmodel name\t: Intel(R) Core(TM) i5-8500T CPU @ 2.10GHz\nprocessor\t: 1\nmodel name\t: Intel(R) Core(TM) i5-8500T CPU @ 2.10GHz",
	"dmi\nsys_vendor Example Corp\nproduct_name Mini PC 1",
	"dtmodel\n",
	"virt\nnone",
	"block\nnvme0n1 976773168 0 Example NVMe 500GB\nsda 1953525168 1 Example HDD 1TB",
	"nic\nenp1s0",
	"mounts\n/dev/nvme0n1p2 / ext4 rw,relatime 0 0\n/dev/nvme0n1p1 /boot/efi vfat rw 0 0\ntmpfs /run tmpfs rw 0 0\n/dev/sda1 /srv/media\\040files ext4 rw 0 0\n/dev/sda1 /srv/media\\040files/bind ext4 rw 0 0\noverlay /var/lib/docker/overlay2/abc/merged overlay rw 0 0",
	"df\nFilesystem     1024-blocks      Used Available Capacity Mounted on\n/dev/nvme0n1p2   479596204  96000000 359000000      22% /\n/dev/nvme0n1p1      523248      6000    517248       2% /boot/efi\ntmpfs              1600000      1000   1599000       1% /run\n/dev/sda1        960000000 900000000  60000000      94% /srv/media files\n/dev/sda1        960000000 900000000  60000000      94% /srv/media files/bind\noverlay          479596204  96000000 359000000      22% /var/lib/docker/overlay2/abc/merged",
	"docker\nweb\tghcr.io/example/web:1.2\texited\tExited (0) 2 days ago\napi\tghcr.io/example/api:3.4\trunning\tUp 3 days",
	"running\nssh.service loaded active running OpenBSD Secure Shell server\ncron.service loaded active running Regular background program processing daemon\ndocker.service loaded active running Docker Application Container Engine",
	"failed",
}

const debianHwmon = "hwmon\nchip hwmon0 acpitz\ntemp1 27800 \nchip hwmon1 coretemp\ntemp1 52000 Package id 0\ntemp2 50000 Core 0\ntemp3 127000 Core 1\nchip hwmon2 nvme\ntemp1 38850 Composite\nchip hwmon3 nct6775\nfan1 1200 \nfan2 0 "

const debianThermal = "thermal\nthermal_zone0 27800 acpitz\nthermal_zone1 52000 x86_pkg_temp"

// piInventory is a Raspberry Pi: a device-tree model, no DMI, no hwmon chip, the CPU temperature
// from a thermal zone alone.
var piInventory = []string{
	"uname\nLinux\n6.6.51+rpt-rpi-v8\naarch64\npi-1",
	"osrelease\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"",
	"cpuinfo\nprocessor\t: 0\nprocessor\t: 1\nprocessor\t: 2\nprocessor\t: 3\nHardware\t: BCM2835\nModel\t\t: Raspberry Pi 4 Model B Rev 1.4",
	"dmi\nsys_vendor \nproduct_name ",
	"dtmodel\nRaspberry Pi 4 Model B Rev 1.4\x00",
	"virt\nnone",
	"block\nmmcblk0 124735488 0 ",
	"nic\neth0\nwlan0",
	"mounts\n/dev/mmcblk0p2 / ext4 rw 0 0\n/dev/mmcblk0p1 /boot/firmware vfat rw 0 0",
	"df\nFilesystem     1024-blocks    Used Available Capacity Mounted on\n/dev/root         60000000 9000000  48000000      16% /\n/dev/mmcblk0p1      522232   70000    452232      14% /boot/firmware",
	"running\nssh.service loaded active running OpenSSH",
	"failed",
}

// alpineInventory is an Alpine host on busybox: no systemd (neither systemd section), no
// systemd-detect-virt, no lsblk, no container runtime, no MemAvailable.
var alpineInventory = []string{
	"uname\nLinux\n6.12.13-0-lts\nx86_64\nedge-1",
	"osrelease\nNAME=\"Alpine Linux\"\nPRETTY_NAME=\"Alpine Linux v3.22\"",
	"cpuinfo\nprocessor\t: 0\nmodel name\t: QEMU Virtual CPU version 2.5+",
	"dmi\nsys_vendor QEMU\nproduct_name Standard PC (i440FX + PIIX, 1996)",
	"dtmodel\n",
	"virt",
	"block\nvda 20971520 1 ",
	"nic",
	"mounts\n/dev/vda3 / ext4 rw 0 0\ndevtmpfs /dev devtmpfs rw 0 0\nshm /dev/shm tmpfs rw 0 0",
	"df\nFilesystem           1024-blocks    Used Available Capacity Mounted on\n/dev/vda3              9000000  2000000   6500000  24% /\ndevtmpfs                 10240        0     10240   0% /dev\nshm                     500000        0    500000   0% /dev/shm",
}

func TestParseInventoryFixtures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sections []string
		want     sshmodel.Info
		disks    []string
		nics     []string
	}{
		{
			name: "debian x86_64 with coretemp", sections: debianInventory,
			want: sshmodel.Info{
				Hostname: "alpha", OS: "Debian GNU/Linux 13 (trixie)", Kernel: "6.12.38+deb13-amd64", Arch: "x86_64",
				Vendor: "Example Corp", Product: "Mini PC 1", CPUModel: "Intel(R) Core(TM) i5-8500T CPU @ 2.10GHz", CPUs: 2,
				Disks: []sshmodel.Disk{
					{Name: "nvme0n1", Model: "Example NVMe 500GB", SizeBytes: 976773168 * 512, Kind: "nvme"},
					{Name: "sda", Model: "Example HDD 1TB", SizeBytes: 1953525168 * 512, Kind: "hdd"},
				},
				Filesystems: []sshmodel.Filesystem{
					{Mount: "/", Device: "/dev/nvme0n1p2", Type: "ext4", UsedBytes: 96000000 * 1024, TotalBytes: 479596204 * 1024},
					{Mount: "/boot/efi", Device: "/dev/nvme0n1p1", Type: "vfat", UsedBytes: 6000 * 1024, TotalBytes: 523248 * 1024},
					{Mount: "/srv/media files", Device: "/dev/sda1", Type: "ext4", UsedBytes: 900000000 * 1024, TotalBytes: 960000000 * 1024},
				},
				Pools: []sshmodel.Pool{},
				Containers: &sshmodel.Containers{Runtime: "docker", Items: []sshmodel.Container{
					{Name: "api", Image: "ghcr.io/example/api:3.4", State: "running", Status: "Up 3 days"},
					{Name: "web", Image: "ghcr.io/example/web:1.2", State: "exited", Status: "Exited (0) 2 days ago"},
				}},
				Services: &sshmodel.Services{Running: 3, Failed: []string{}},
			},
			disks: []string{"nvme0n1", "sda"}, nics: []string{"enp1s0"},
		},
		{
			name: "raspberry pi with a device-tree model", sections: piInventory,
			want: sshmodel.Info{
				Hostname: "pi-1", OS: "Debian GNU/Linux 12 (bookworm)", Kernel: "6.6.51+rpt-rpi-v8", Arch: "aarch64",
				Product: "Raspberry Pi 4 Model B Rev 1.4", CPUModel: "BCM2835", CPUs: 4,
				Disks: []sshmodel.Disk{{Name: "mmcblk0", SizeBytes: 124735488 * 512, Kind: "ssd"}},
				Filesystems: []sshmodel.Filesystem{
					{Mount: "/", Device: "/dev/root", Type: "ext4", UsedBytes: 9000000 * 1024, TotalBytes: 60000000 * 1024},
					{Mount: "/boot/firmware", Device: "/dev/mmcblk0p1", Type: "vfat", UsedBytes: 70000 * 1024, TotalBytes: 522232 * 1024},
				},
				Pools:    []sshmodel.Pool{},
				Services: &sshmodel.Services{Running: 1, Failed: []string{}},
			},
			disks: []string{"mmcblk0"}, nics: []string{"eth0", "wlan0"},
		},
		{
			name: "alpine on busybox without systemd", sections: alpineInventory,
			want: sshmodel.Info{
				Hostname: "edge-1", OS: "Alpine Linux v3.22", Kernel: "6.12.13-0-lts", Arch: "x86_64",
				Vendor: "QEMU", Product: "Standard PC (i440FX + PIIX, 1996)", CPUModel: "QEMU Virtual CPU version 2.5+", CPUs: 1,
				Disks: []sshmodel.Disk{{Name: "vda", SizeBytes: 20971520 * 512, Kind: "hdd"}},
				Filesystems: []sshmodel.Filesystem{
					{Mount: "/", Device: "/dev/vda3", Type: "ext4", UsedBytes: 2000000 * 1024, TotalBytes: 9000000 * 1024},
				},
				Pools: []sshmodel.Pool{},
			},
			disks: []string{"vda"}, nics: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := parseInventory(parseFrame(t, frameText(testNonce, 0, tc.sections...)))
			if !reflect.DeepEqual(inv.info, tc.want) {
				t.Errorf("info =\n%+v\nwant\n%+v", inv.info, tc.want)
			}
			if !reflect.DeepEqual(inv.disks, tc.disks) || !reflect.DeepEqual(inv.nics, tc.nics) {
				t.Errorf("disks %v nics %v, want %v %v", inv.disks, inv.nics, tc.disks, tc.nics)
			}
		})
	}
}

// A runtime that refuses the SSH user is denied with no items; a ZFS host lists its pools.
func TestParseDeniedDockerAndZFS(t *testing.T) {
	denied := append(append([]string{}, debianInventory[:10]...), "docker", "docker-denied", "zpool\ntank\tONLINE\t5000000000\t8000000000\nbackup\tDEGRADED\t100\t200\nbroken line")
	info := parseInventory(parseFrame(t, frameText(testNonce, 0, denied...))).info
	if c := info.Containers; c == nil || c.Runtime != "docker" || !c.Denied || len(c.Items) != 0 || c.Items == nil {
		t.Errorf("containers = %+v", c)
	}
	want := []sshmodel.Pool{
		{Name: "tank", Health: "ONLINE", AllocBytes: 5000000000, SizeBytes: 8000000000},
		{Name: "backup", Health: "DEGRADED", AllocBytes: 100, SizeBytes: 200},
	}
	if !reflect.DeepEqual(info.Pools, want) {
		t.Errorf("pools = %+v", info.Pools)
	}
	if info.Services != nil {
		t.Errorf("services without a systemd section = %+v", info.Services)
	}
	podman := parseInventory(parseFrame(t, frameText(testNonce, 0, "uname\nLinux\n6\nx\nh", "podman\nweb\tquay.io/example/web\tRunning\tUp 2 hours"))).info
	if c := podman.Containers; c == nil || c.Runtime != "podman" || c.Denied || len(c.Items) != 1 || c.Items[0].State != "running" {
		t.Errorf("podman = %+v", c)
	}
}

// The fast sections: the hottest CPU chip reading, sensors without placeholders or empty fan
// headers, and rates only from the second frame on, of the physical interface and the whole
// disks.
func TestCollectorSamplesAndRates(t *testing.T) {
	c := &collector{}
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	first := c.frame(parseFrame(t, frameText(testNonce, 0, append(debianInventory, fastSections(0, debianHwmon, debianThermal)...)...)), t0)
	if first.CPURatio != 0 || first.NetRxBytesPerSec != 0 || first.DiskWriteBytesPerSec != 0 {
		t.Errorf("the first sample has rates: %+v", first)
	}
	if first.MemTotalBytes != 16000000*1024 || first.MemUsedBytes != 8000000*1024 || first.SwapUsedBytes != 500000*1024 || first.UptimeSeconds != 1000 {
		t.Errorf("memory and uptime = %+v", first)
	}
	if first.Load1 != 0.52 || first.Load15 != 0.59 {
		t.Errorf("load = %+v", first)
	}
	if first.CPUTempC == nil || *first.CPUTempC != 52 {
		t.Errorf("cpu temp = %v, want the coretemp package", first.CPUTempC)
	}
	wantSensors := []sshmodel.Sensor{
		{Chip: "acpitz", Label: "temp1", TempC: 27.8}, {Chip: "coretemp", Label: "Package id 0", TempC: 52},
		{Chip: "coretemp", Label: "Core 0", TempC: 50}, {Chip: "nvme", Label: "Composite", TempC: 38.85},
	}
	if !reflect.DeepEqual(first.Sensors, wantSensors) {
		t.Errorf("sensors = %+v", first.Sensors)
	}
	if !reflect.DeepEqual(first.Fans, []sshmodel.Fan{{Chip: "nct6775", Label: "fan1", RPM: 1200}}) {
		t.Errorf("fans = %+v", first.Fans)
	}

	second := c.frame(parseFrame(t, frameText(testNonce, 1, fastSections(1, debianHwmon, debianThermal)...)), t0.Add(time.Hour))
	// 5 s of host uptime between the frames, whatever dash's clock says.
	if second.NetRxBytesPerSec != 100_000 || second.NetTxBytesPerSec != 20_000 {
		t.Errorf("net = %v / %v, want enp1s0 alone (not docker0 or lo)", second.NetRxBytesPerSec, second.NetTxBytesPerSec)
	}
	if second.DiskReadBytesPerSec != (1000+100)*512/5.0 || second.DiskWriteBytesPerSec != 2000*512/5.0 {
		t.Errorf("disk = %v / %v, want nvme0n1 and sda, not a partition or a loop", second.DiskReadBytesPerSec, second.DiskWriteBytesPerSec)
	}
	// 1000 jiffies: 300 user, 100 system, 500 idle, 100 iowait.
	if math.Abs(second.CPURatio-0.4) > 1e-9 || math.Abs(second.IOWaitRatio-0.1) > 1e-9 {
		t.Errorf("cpu = %v, iowait = %v", second.CPURatio, second.IOWaitRatio)
	}
}

// A Raspberry Pi's CPU temperature comes from its thermal zone.
func TestCollectorThermalZoneOnly(t *testing.T) {
	c := &collector{}
	st := c.frame(parseFrame(t, frameText(testNonce, 0, append(piInventory, fastSections(0, "hwmon", "thermal\nthermal_zone0 51540 cpu-thermal\nthermal_zone1 30000 gpu-thermal")...)...)), time.Now())
	if st.CPUTempC == nil || *st.CPUTempC != 51.54 || len(st.Sensors) != 0 || st.Sensors == nil || st.Fans == nil {
		t.Errorf("stats = %+v", st)
	}
}

// Counters that go backwards (a reboot, a 32-bit wrap) or jump beyond any link give zero, never
// a negative or absurd rate.
func TestCollectorSurvivesResetsAndWraps(t *testing.T) {
	c := &collector{}
	now := time.Now()
	c.frame(parseFrame(t, frameText(testNonce, 0, append(debianInventory, fastSections(10, "hwmon", "thermal")...)...)), now)
	// A reboot: uptime and every counter start again.
	st := c.frame(parseFrame(t, frameText(testNonce, 1, fastSections(0, "hwmon", "thermal")...)), now.Add(5*time.Second))
	if st.CPURatio != 0 || st.NetRxBytesPerSec != 0 || st.DiskReadBytesPerSec != 0 {
		t.Errorf("after a reboot: %+v", st)
	}
	// One counter wraps while the others move on; another jumps by more than any link carries.
	c.frame(parseFrame(t, frameText(testNonce, 2, fastSections(1, "hwmon", "thermal")...)), now)
	wrapped := fastSections(2, "hwmon", "thermal")
	wrapped[4] = "netdev\nenp1s0: 10 1 0 0 0 0 0 0 " + strconv.Itoa(1_000_000_000_000_000) + " 1 0 0 0 0 0 0"
	st = c.frame(parseFrame(t, frameText(testNonce, 3, wrapped...)), now)
	if st.NetRxBytesPerSec != 0 || st.NetTxBytesPerSec != 0 {
		t.Errorf("wrapped or jumped counters: rx %v tx %v", st.NetRxBytesPerSec, st.NetTxBytesPerSec)
	}
	if st.CPURatio <= 0 || st.CPURatio > 1 || st.DiskReadBytesPerSec <= 0 {
		t.Errorf("the other counters: %+v", st)
	}
}

// Every parser takes garbage without a panic and without a negative value.
func TestParsersTakeGarbage(t *testing.T) {
	junk := []string{"", " ", "cpu", "cpu x y z w v", "a:b", ":", "\x00\xff", strings.Repeat("9", 400), "-1 -2 -3", "eth0: -5 0 0 0 0 0 0 0 -9"}
	if parseStat(junk).ok {
		t.Error("stat from junk")
	}
	l1, l5, l15 := parseLoad(junk)
	m := parseMeminfo(junk)
	if l1 < 0 || l5 < 0 || l15 < 0 || m.total < 0 || m.available < 0 || parseUptime(junk) < 0 {
		t.Error("a negative value from junk")
	}
	parseNetDev(junk)
	parseDiskstats(junk)
	parseHwmon(append([]string{"chip x y"}, junk...))
	parseThermal(junk)
	inv := parseInventory(frame{sections: map[string][]string{"uname": junk, "block": junk, "df": junk, "zpool": junk, "docker": junk, "running": junk, "failed": junk, "cpuinfo": junk}})
	for _, d := range inv.info.Disks {
		if d.SizeBytes < 0 {
			t.Errorf("disk %+v", d)
		}
	}
	if long := clip(strings.Repeat("é", 300)); len(long) > maxFieldBytes || !strings.HasPrefix(long, "é") {
		t.Errorf("clip = %q", long)
	}
}

// The frame reader: a preamble before the first frame is skipped, and every way of breaking the
// protocol is an errProtocol.
func TestFrameReader(t *testing.T) {
	ok := "Welcome to the host\n" + frameText(testNonce, 0, "uptime\n5.00 1.00") + frameText(testNonce, 1, "uptime\n6.00 1.00")
	fr := newFrameReader(strings.NewReader(ok), testNonce)
	for i := range 2 {
		f, err := fr.next()
		if err != nil || f.seq != strconv.Itoa(i) || f.sections["uptime"][0] == "" {
			t.Fatalf("frame %d = %+v, %v", i, f, err)
		}
	}
	if _, err := fr.next(); err == nil {
		t.Error("no error at the end of the output")
	}
	m := "@@" + testNonce
	for name, text := range map[string]string{
		"line too long":       m + " frame 0\n" + m + " x\n" + strings.Repeat("a", maxLineBytes+1) + "\n",
		"frame too large":     m + " frame 0\n" + m + " x\n" + strings.Repeat(strings.Repeat("a", 1000)+"\n", maxFrameBytes/1000+2),
		"no section":          m + " frame 0\nstray\n",
		"nested frame":        m + " frame 0\n" + m + " frame 1\n",
		"section twice":       m + " frame 0\n" + m + " a\n" + m + " a\n",
		"preamble too long":   strings.Repeat("motd\n", maxPreambleLines+1) + m + " frame 0\n",
		"section before":      m + " uptime\n",
		"stray between":       frameText(testNonce, 0, "x") + "stray\n" + frameText(testNonce, 1, "x"),
		"another nonce frame": "@@ffffffffffffffffffffffffffffffff frame 0\n" + strings.Repeat("x\n", maxPreambleLines+1),
	} {
		fr := newFrameReader(strings.NewReader(text), testNonce)
		var err error
		for range 2 {
			if _, err = fr.next(); err != nil {
				break
			}
		}
		if err == nil || !strings.Contains(err.Error(), errProtocol.Error()) {
			t.Errorf("%s: err = %v, want a protocol violation", name, err)
		}
	}
}
