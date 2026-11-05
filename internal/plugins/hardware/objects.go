package hardware

import (
	"context"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Context implements plugin.Objects: the host adds its charts and its disks and pools; a disk or
// a pool names its host.
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
	var host model.HostData
	if v, ok := s.Latest(topicHosts); ok {
		host, _ = v.(model.HostData)
	}
	switch obj.(type) {
	case model.HostData:
		for _, name := range []string{"host.cpu", "host.mem", "host.power", "host.cpuTemp"} {
			part.Series = append(part.Series, model.SeriesRef{Name: name, Args: map[string]string{}})
		}
		for _, d := range host.Disks {
			part.Related = append(part.Related, d.Ref)
		}
		for _, pl := range host.Pools {
			part.Related = append(part.Related, pl.Ref)
		}
	case model.Disk, model.Pool:
		if host.Ref != "" {
			part.Related = append(part.Related, host.Ref)
		}
	}
	return part, nil
}

var (
	_ plugin.Objects = (*Plugin)(nil)
	_ plugin.Objects = (*twin)(nil)
)
