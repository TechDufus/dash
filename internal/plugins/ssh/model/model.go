// Package model holds the payload types only the ssh plugin publishes: the ssh.hosts topic and
// the history route. It is an import leaf: it may import nothing from dash
// (TestPluginModelPackagesAreImportLeaves), because internal/model.TopicTypes lists every
// topic's payload type and so imports this package.
//
// Nothing here says how dash reaches a host: no address, no user and no private key material.
// Every viewer reads this payload; the connection settings stay in the settings API.
package model

import "time"

// HostState is where dash stands with one host.
type HostState string

const (
	// StateConnecting: no connection attempt has finished yet.
	StateConnecting HostState = "connecting"
	// StateUnverified: the host has no pinned host key (plugins.ssh.hosts[].hostKey). dash has
	// read the key the host presents (OfferedKey) and sends it no credentials until an
	// administrator trusts that key.
	StateUnverified HostState = "unverified"
	// StateKeyChanged: the host presents a key other than the pinned one (OfferedKey). dash
	// refuses the connection.
	StateKeyChanged HostState = "key_changed"
	// StateUnauthorized: the host's key matches, and the host refuses dash's public key for
	// the configured user.
	StateUnauthorized HostState = "unauthorized"
	// StateUnreachable: the dial, the handshake or the session failed or timed out.
	StateUnreachable HostState = "unreachable"
	// StateUnsupported: the host answered, and it is not Linux.
	StateUnsupported HostState = "unsupported"
	// StateUp: samples arrive.
	StateUp HostState = "up"
)

// HostsData is the ssh.hosts topic.
type HostsData struct {
	// Hosts are the configured hosts in their configured order; never null.
	Hosts []Host `json:"hosts"`
	// Identity is the public half of the key dash signs in with: what an administrator adds to
	// a host's authorized_keys. Nil while dash has no key (the plugin is then not running, or it
	// is the demo twin's made-up one).
	Identity *Identity `json:"identity,omitempty"`
	// IntervalSeconds is how often a host that is up is sampled.
	IntervalSeconds int `json:"intervalSeconds"`
}

// Identity is dash's SSH public key.
type Identity struct {
	// PublicKey is one authorized_keys line: "ssh-ed25519 AAAA... dash".
	PublicKey string `json:"publicKey"`
	// Fingerprint is the key's SHA256 fingerprint, "SHA256:...".
	Fingerprint string `json:"fingerprint"`
}

// Host is one machine dash reads over SSH.
type Host struct {
	Ref   string    `json:"ref"` // ssh:Host/<name>
	Name  string    `json:"name"`
	State HostState `json:"state"`
	// Message says what State means for this host in fixed words; it never repeats what the
	// host or the network sent.
	Message string `json:"message"`
	// Health is ok, warn, crit or unknown: the worst of the host's state and what it reports
	// (a full file system, a failed unit, a pool that is not online).
	Health string `json:"health" tstype:"\"ok\" | \"warn\" | \"crit\" | \"unknown\""`
	// Since is when the host entered State.
	Since time.Time `json:"since"`
	// OfferedKey is the host key the host presents, while State is unverified or key_changed.
	OfferedKey *HostKey `json:"offeredKey,omitempty"`
	// Info is what dash discovered on the host: the last inventory read, kept while the host is
	// away. Nil until the first one.
	Info *Info `json:"info,omitempty"`
	// Stats is the latest sample. Nil unless State is up.
	Stats *Stats `json:"stats,omitempty"`
}

// HostKey is an SSH host key as a host presents it.
type HostKey struct {
	Type        string `json:"type"`        // ssh-ed25519, ecdsa-sha2-nistp256, rsa-sha2-512, ...
	Fingerprint string `json:"fingerprint"` // SHA256:...
	// Key is the key in authorized_keys form ("ssh-ed25519 AAAA..."): the value of
	// plugins.ssh.hosts[].hostKey that trusts it.
	Key string `json:"key"`
}

// Info is a host's inventory, read about once a minute. An empty string, a zero count or a zero
// size means the host does not report that fact, and the UI leaves it out.
type Info struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`     // os-release PRETTY_NAME: "Debian GNU/Linux 13 (trixie)"
	Kernel   string `json:"kernel"` // uname -r
	Arch     string `json:"arch"`   // uname -m: x86_64, aarch64
	// Vendor and Product are the DMI system vendor and product name, or the device-tree model
	// on a board without DMI.
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
	// Virtualization is what systemd-detect-virt names (kvm, lxc, docker, ...); empty on bare
	// metal and when the host cannot tell.
	Virtualization string `json:"virtualization"`
	CPUModel       string `json:"cpuModel"`
	CPUs           int    `json:"cpus"` // logical CPUs
	// Disks, Filesystems and Pools are never null.
	Disks       []Disk       `json:"disks"`
	Filesystems []Filesystem `json:"filesystems"`
	Pools       []Pool       `json:"pools"`
	// Containers is nil when the host has neither docker nor podman.
	Containers *Containers `json:"containers,omitempty"`
	// Services is nil when the host does not run systemd.
	Services *Services `json:"services,omitempty"`
}

// Disk is a whole block device.
type Disk struct {
	Name      string `json:"name"` // sda, nvme0n1
	Model     string `json:"model"`
	SizeBytes int64  `json:"sizeBytes"`
	Kind      string `json:"kind" tstype:"\"hdd\" | \"ssd\" | \"nvme\" | \"\""`
}

// Filesystem is a mounted file system that holds data (no tmpfs, overlay or other virtual one).
type Filesystem struct {
	Mount      string `json:"mount"`
	Device     string `json:"device"`
	Type       string `json:"type"`
	UsedBytes  int64  `json:"usedBytes"`
	TotalBytes int64  `json:"totalBytes"`
}

// Pool is a ZFS pool.
type Pool struct {
	Name       string `json:"name"`
	Health     string `json:"health"` // ONLINE, DEGRADED, FAULTED, ...
	AllocBytes int64  `json:"allocBytes"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// Containers are the containers of the host's container runtime.
type Containers struct {
	Runtime string `json:"runtime" tstype:"\"docker\" | \"podman\""`
	// Denied is set when the runtime is there and the SSH user may not ask it; Items is then
	// empty.
	Denied bool `json:"denied"`
	// Items is never null; running ones first, then by name.
	Items []Container `json:"items"`
}

// Container is one container.
type Container struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`  // running, exited, paused, ...
	Status string `json:"status"` // "Up 3 days", as the runtime words it
}

// Services are the host's systemd services.
type Services struct {
	Running int `json:"running"`
	// Failed are the names of the failed units, sorted; never null.
	Failed []string `json:"failed"`
}

// Stats is one sample of a host that is up. A rate is over the time since the sample before, so
// the first sample of a connection has zero rates.
type Stats struct {
	SampledAt   time.Time `json:"sampledAt"`
	CPURatio    float64   `json:"cpuRatio"`    // 0 to 1, all CPUs
	IOWaitRatio float64   `json:"ioWaitRatio"` // 0 to 1, the part of CPU time spent waiting for I/O
	Load1       float64   `json:"load1"`
	Load5       float64   `json:"load5"`
	Load15      float64   `json:"load15"`
	// MemUsedBytes is MemTotal - MemAvailable.
	MemUsedBytes   int64 `json:"memUsedBytes"`
	MemTotalBytes  int64 `json:"memTotalBytes"`
	SwapUsedBytes  int64 `json:"swapUsedBytes"`
	SwapTotalBytes int64 `json:"swapTotalBytes"`
	UptimeSeconds  int64 `json:"uptimeSeconds"`
	// The network rates sum the host's physical interfaces; the disk rates its whole disks.
	NetRxBytesPerSec     float64 `json:"netRxBytesPerSec"`
	NetTxBytesPerSec     float64 `json:"netTxBytesPerSec"`
	DiskReadBytesPerSec  float64 `json:"diskReadBytesPerSec"`
	DiskWriteBytesPerSec float64 `json:"diskWriteBytesPerSec"`
	// CPUTempC is the hottest CPU reading; nil when the host exposes none.
	CPUTempC *float64 `json:"cpuTempC,omitempty"`
	// Sensors and Fans are what the host's hwmon chips report; never null.
	Sensors []Sensor `json:"sensors"`
	Fans    []Fan    `json:"fans"`
}

// Sensor is one temperature reading.
type Sensor struct {
	Chip  string  `json:"chip"`  // coretemp, k10temp, nvme, ...
	Label string  `json:"label"` // "Package id 0", "Composite"; the input's name when it has no label
	TempC float64 `json:"tempC"`
}

// Fan is one fan reading.
type Fan struct {
	Chip  string `json:"chip"`
	Label string `json:"label"`
	RPM   int    `json:"rpm"`
}

// History is GET /api/v1/plugins/ssh/hosts/{name}/history: the samples dash kept of a host since
// it started, at most an hour of them. dash keeps them in memory only.
type History struct {
	IntervalSeconds int `json:"intervalSeconds"`
	// Samples are oldest first; never null. A gap in T is a time the host was away.
	Samples []Sample `json:"samples"`
}

// Sample is one point of a host's history: Stats cut down to what the charts draw.
type Sample struct {
	T                    int64    `json:"t"` // unix seconds
	CPURatio             float64  `json:"cpuRatio"`
	MemRatio             float64  `json:"memRatio"` // used / total, 0 to 1
	Load1                float64  `json:"load1"`
	NetRxBytesPerSec     float64  `json:"netRxBytesPerSec"`
	NetTxBytesPerSec     float64  `json:"netTxBytesPerSec"`
	DiskReadBytesPerSec  float64  `json:"diskReadBytesPerSec"`
	DiskWriteBytesPerSec float64  `json:"diskWriteBytesPerSec"`
	CPUTempC             *float64 `json:"cpuTempC,omitempty"`
}
