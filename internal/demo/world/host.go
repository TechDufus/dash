package world

import (
	"fmt"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// HostSensors are the BMC temperature sensors of the first fake host and their resting values.
// Read only: the host payload and the temperature series both drift around these.
var HostSensors = []model.Sensor{
	{Name: "Inlet Temp", ValueC: 22},
	{Name: "Exhaust Temp", ValueC: 34},
	{Name: "CPU1 Temp", ValueC: 56},
	{Name: "CPU2 Temp", ValueC: 53},
}

// SecondHostSensors are the BMC temperature sensors of the second fake host, a one-socket box.
var SecondHostSensors = []model.Sensor{
	{Name: "Inlet Temp", ValueC: 24},
	{Name: "Exhaust Temp", ValueC: 31},
	{Name: "CPU1 Temp", ValueC: 47},
}

// hostSpec is what a fake hardware host is: its parts and the resting values of its readings.
// The CPU and memory keys are its Proxmox node's (pveNodes), so the two pages agree.
type hostSpec struct {
	name, model             string
	key                     string // prefix of its drift keys; "" for the first host's historic keys
	sensors                 []model.Sensor
	pkgs                    []float64 // resting CPU package temperatures, one per socket
	power, powerSpread      float64
	memGiB, cpu, mem        float64
	cpuKey, memKey          string
	psus                    int
	pools                   []string
	fans                    int
	fanBase                 float64
	disks, spinning, serial int // disks; how many of the last ones spin (no wear); first serial number
}

// hostSpecs are the fake hosts: the cluster's two Proxmox nodes, in the order the Hardware page
// shows them.
var hostSpecs = []hostSpec{
	{
		name: HostName, model: HostModel, sensors: HostSensors, pkgs: []float64{57, 54}, power: 214, powerSpread: 26,
		memGiB: 256, cpu: 0.19, mem: 0.41, cpuKey: "hostcpu", memKey: "hostmem",
		psus: 2, pools: []string{"rpool", "tank"}, fans: 6, fanBase: 5900, disks: 8, spinning: 2, serial: 1000,
	},
	{
		name: SecondNodeName, model: SecondHostModel, key: SecondNodeName + "/", sensors: SecondHostSensors,
		pkgs: []float64{48}, power: 96, powerSpread: 12, memGiB: 64, cpu: 0.27, mem: 0.48, cpuKey: "node2cpu", memKey: "node2mem",
		psus: 1, pools: []string{"rpool"}, fans: 4, fanBase: 4200, disks: 2, serial: 2000,
	},
}

// Hosts are the hardware payloads of the fake hosts: sensors, fans, PSUs, disks and pools.
func (w *World) Hosts() []model.HostData { return w.hosts(w.clock()) }

func (w *World) hosts(now time.Time) []model.HostData {
	out := make([]model.HostData, len(hostSpecs))
	for i, s := range hostSpecs {
		out[i] = s.build(now)
	}
	return out
}

func (s hostSpec) build(now time.Time) model.HostData {
	k := func(name string) string { return s.key + name }
	temps := make([]float64, len(s.pkgs))
	for i, t := range s.pkgs {
		temps[i] = Around(k(fmt.Sprint("pkg", i)), now, t, 5)
	}
	sensors := make([]model.Sensor, len(s.sensors))
	for i, x := range s.sensors {
		sensors[i] = model.Sensor{Name: x.Name, ValueC: Around(k(x.Name), now, x.ValueC, 2)}
	}
	memTotal := gib(s.memGiB)
	h := model.HostData{
		Model: s.model, Name: s.name,
		CPURatio: Around(s.cpuKey, now, s.cpu, 0.07), MemUsedBytes: int64(float64(memTotal) * Around(s.memKey, now, s.mem, 0.02)), MemTotalBytes: memTotal,
		PowerWatts: Around(k("power"), now, s.power, s.powerSpread), PackageTempsC: temps, Sensors: sensors,
		SELFreeBytes: 14 << 10,
		ScrubTracked: true,
		Links:        []model.Link{{Label: "Proxmox", Href: "https://pve.example.com:8006", Kind: model.LinkKindProxmox}},
	}
	for i := range s.psus {
		h.PSUs = append(h.PSUs, model.PSU{Name: fmt.Sprintf("PS%d Status", i+1), State: model.HealthOK})
	}
	for i, p := range s.pools {
		h.Pools = append(h.Pools, model.Pool{Name: p, State: "ONLINE", LastScrubEnd: agoP(now, 5*24*time.Hour-time.Duration(i*3)*time.Hour)})
	}
	for i := range s.fans {
		h.Fans = append(h.Fans, model.Fan{Name: fmt.Sprintf("Fan%d", i+1), RPM: Around(k(fmt.Sprint("fan", i)), now, s.fanBase+float64(i%3)*60, 180)})
	}
	for i := range s.disks {
		d := model.Disk{
			Bay: fmt.Sprintf("Bay %d", i), Serial: fmt.Sprintf("DEMO%04dX", s.serial+i*37),
			Device: fmt.Sprintf("/dev/sd%c", 'a'+i), Healthy: true, TempC: new(Around(k(fmt.Sprint("disk", i)), now, 33+float64(i%3), 1.5)),
		}
		if i < s.disks-s.spinning { // spinning disks report no wear
			d.LifeRemainingPct = new(100 - float64(3+i%4))
		}
		h.Disks = append(h.Disks, d)
	}
	return h
}
