// Package world is the fake homelab behind demo mode: one coherent, invented set of payloads (a
// Proxmox host, three Kubernetes nodes, a few namespaces, alerts, checks) that every demo plugin
// twin builds its topics, and so the overview's contributions, from. Nothing here talks to a network.
//
// Every name, address and serial is invented. Addresses come from the documentation ranges
// (192.0.2.0/24, 198.51.100.0/24) and every link points at example.com.
package world

import (
	"hash/fnv"
	"math"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// HostName is the name of the first fake Proxmox host; SecondNodeName is the second.
const HostName = "pve-demo"

// LinkSource supplies the deep links the fixtures attach to objects, so the demo can hand over
// the link provider of a plugin. It returns an empty, non-nil slice when the target tool is not
// configured.
type LinkSource interface {
	// Headlamp links to an object in Headlamp; kind is a Kubernetes kind and namespace is empty
	// for cluster-scoped objects.
	Headlamp(kind, namespace, name string) []model.Link
}

// World builds the payloads of the fake homelab. It holds no state besides its inputs: every
// builder derives its values from the clock reading and the start time, so two builds at the same
// instant are equal.
type World struct {
	clock func() time.Time
	start time.Time
	links LinkSource
}

// New returns a World. clock supplies "now" (read once per builder call) and start is when the
// demo process began, which the uptimes count from.
func New(clock func() time.Time, start time.Time, links LinkSource) *World {
	return &World{clock: clock, start: start, links: links}
}

// Snapshot is every topic payload of the world at one instant.
type Snapshot struct {
	Hosts   []model.HostData
	VMs     model.VMsData
	Cluster model.ClusterData
	Flux    model.FluxData
	Storage model.StorageData
	Alerts  model.AlertsData
	Status  model.StatusData
	Apps    model.AppsData
}

// Build returns every payload at one clock reading.
func (w *World) Build() Snapshot {
	now := w.clock()
	return Snapshot{
		Hosts:   w.hosts(now),
		VMs:     w.vms(now),
		Cluster: w.cluster(now),
		Flux:    w.flux(now),
		Storage: w.storage(now),
		Alerts:  w.alerts(now),
		Status:  w.status(now),
		Apps:    w.Apps(),
	}
}

// Wave is a smooth, repeatable drift in [-1, 1] for a key at time t.
func Wave(key string, t time.Time) float64 {
	f := fnv.New32a()
	_, _ = f.Write([]byte(key))
	phase := float64(f.Sum32()%1000) / 1000 * 2 * math.Pi
	x := float64(t.Unix())
	// A daily swing, an hourly one and a little chatter, so every chart range has some shape.
	return 0.5*math.Sin(2*math.Pi*x/86400+phase) + 0.35*math.Sin(x/1700+2*phase) + 0.15*math.Sin(x/240+3*phase)
}

// Around returns base drifting by up to ±spread.
func Around(key string, t time.Time, base, spread float64) float64 {
	return base + spread*Wave(key, t)
}

func ago(now time.Time, d time.Duration) time.Time { return now.Add(-d).UTC() }

func agoP(now time.Time, d time.Duration) *time.Time { return new(ago(now, d)) }

func gib(v float64) int64 { return int64(v * (1 << 30)) }
