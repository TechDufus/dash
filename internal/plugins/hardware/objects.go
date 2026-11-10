package hardware

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
)

// Context implements plugin.Objects: a host adds its charts and its disks and pools; a disk or a
// pool names its host; a machine adds its node-exporter stats.
func (p *Plugin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return hostContext(&p.ObjectStore, ref)
}

// Context implements plugin.Objects for the twin, like the plugin.
func (t *twin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return hostContext(&t.ObjectStore, ref)
}

func hostContext(s *plugin.ObjectStore, ref string) (model.ContextPart, error) {
	part, obj, err := s.Part(ref)
	if err != nil {
		return part, err
	}
	var hosts model.HostsData
	if v, ok := s.Latest(topicHosts); ok {
		hosts, _ = v.(model.HostsData)
	}
	switch obj.(type) {
	case model.HostData:
		for _, host := range hosts.Hosts {
			if host.Ref != ref {
				continue
			}
			// The series ask for this host by its node, the argument the plugin resolves first,
			// lowercased because the API accepts only lowercase node names and the plugin
			// compares them ignoring case.
			for _, name := range []string{"host.cpu", "host.mem", "host.power", "host.cpuTemp"} {
				part.Series = append(part.Series, model.SeriesRef{Name: name, Args: map[string]string{"node": strings.ToLower(host.Node)}})
			}
			for _, d := range host.Disks {
				part.Related = append(part.Related, d.Ref)
			}
			for _, pl := range host.Pools {
				part.Related = append(part.Related, pl.Ref)
			}
		}
	case model.Disk, model.Pool:
		for _, host := range hosts.Hosts {
			if slices.ContainsFunc(host.Disks, func(d model.Disk) bool { return d.Ref == ref }) ||
				slices.ContainsFunc(host.Pools, func(p model.Pool) bool { return p.Ref == ref }) {
				part.Related = append(part.Related, host.Ref)
			}
		}
	case hwmodel.Machine:
		// The store's facts skip struct pointers, so the stats are added here as stats.<name>.
		if m := obj.(hwmodel.Machine); m.Stats != nil {
			part.Facts = append(part.Facts, statsFacts(*m.Stats)...)
		}
	}
	return part, nil
}

// statsFacts are a machine's stats as context facts named stats.<json name>, in field order. A
// temperature the machine does not report is left out.
func statsFacts(s hwmodel.MachineStats) []model.ContextFact {
	var out []model.ContextFact
	v, t := reflect.ValueOf(s), reflect.TypeFor[hwmodel.MachineStats]()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		f := v.Field(i)
		if f.Kind() == reflect.Pointer {
			if f.IsNil() {
				continue
			}
			f = f.Elem()
		}
		var val string
		switch f.Kind() {
		case reflect.String:
			val = f.String()
		case reflect.Int, reflect.Int64:
			val = strconv.FormatInt(f.Int(), 10)
		case reflect.Float64:
			val = strconv.FormatFloat(f.Float(), 'g', 6, 64)
		}
		out = append(out, model.ContextFact{Name: "stats." + name, Value: val})
	}
	return out
}

var (
	_ plugin.Objects = (*Plugin)(nil)
	_ plugin.Objects = (*twin)(nil)
)
