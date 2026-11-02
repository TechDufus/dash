package world

import (
	"fmt"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// HostSensors are the BMC temperature sensors of the fake host and their resting values. Read
// only: the host payload and the temperature series both drift around these.
var HostSensors = []model.Sensor{
	{Name: "Inlet Temp", ValueC: 22},
	{Name: "Exhaust Temp", ValueC: 34},
	{Name: "CPU1 Temp", ValueC: 56},
	{Name: "CPU2 Temp", ValueC: 53},
}

// Host is the hardware payload of the fake Proxmox host: sensors, fans, PSUs, disks and pools.
func (w *World) Host() model.HostData { return w.host(w.clock()) }

func (w *World) host(now time.Time) model.HostData {
	temps := []float64{Around("pkg0", now, 57, 5), Around("pkg1", now, 54, 5)}
	sensors := make([]model.Sensor, len(HostSensors))
	for i, x := range HostSensors {
		sensors[i] = model.Sensor{Name: x.Name, ValueC: Around(x.Name, now, x.ValueC, 2)}
	}
	power := Around("power", now, 214, 26)
	memTotal := gib(256)
	memUsed := int64(float64(memTotal) * Around("hostmem", now, 0.41, 0.02))
	h := model.HostData{
		Model: HostModel, Name: HostName,
		CPURatio: Around("hostcpu", now, 0.19, 0.07), MemUsedBytes: memUsed, MemTotalBytes: memTotal,
		PowerWatts: power, PackageTempsC: temps, Sensors: sensors,
		PSUs:         []model.PSU{{Name: "PS1 Status", State: model.HealthOK}, {Name: "PS2 Status", State: model.HealthOK}},
		SELFreeBytes: 14 << 10,
		ScrubTracked: true,
		Pools: []model.Pool{
			{Name: "rpool", State: "ONLINE", LastScrubEnd: agoP(now, 5*24*time.Hour)},
			{Name: "tank", State: "ONLINE", LastScrubEnd: agoP(now, 5*24*time.Hour-3*time.Hour)},
		},
		Links: []model.Link{{Label: "Proxmox", Href: "https://pve.example.com:8006", Kind: model.LinkKindProxmox}},
	}
	for i := range 6 {
		h.Fans = append(h.Fans, model.Fan{Name: fmt.Sprintf("Fan%d", i+1), RPM: Around(fmt.Sprint("fan", i), now, 5900+float64(i%3)*60, 180)})
	}
	for i := range 8 {
		life := 100 - float64(3+i%4)
		if i >= 6 {
			life = 0 // spinning disks report no wear
		}
		d := model.Disk{
			Bay: fmt.Sprintf("Bay %d", i), Serial: fmt.Sprintf("DEMO%04dX", 1000+i*37),
			Device: fmt.Sprintf("/dev/sd%c", 'a'+i), Healthy: true, TempC: new(Around(fmt.Sprint("disk", i), now, 33+float64(i%3), 1.5)),
		}
		if life > 0 {
			d.LifeRemainingPct = new(life)
		}
		h.Disks = append(h.Disks, d)
	}
	return h
}
