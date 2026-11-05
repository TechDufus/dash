package alertmanager

import (
	"context"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

// Context implements plugin.Objects. An alert adds the silences that hold it and, when its
// labels name a pod, that pod and its logs; a silence adds the alerts it holds.
func (p *Plugin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return alertContext(&p.ObjectStore, ref)
}

// Context implements plugin.Objects for the twin, like the plugin.
func (t *twin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return alertContext(&t.ObjectStore, ref)
}

func alertContext(s *plugin.ObjectStore, ref string) (model.ContextPart, error) {
	part, obj, err := s.Part(ref)
	if err != nil {
		return part, err
	}
	var d model.AlertsData
	if v, ok := s.Latest(topicAlerts); ok {
		d, _ = v.(model.AlertsData)
	}
	switch v := obj.(type) {
	case model.Alert:
		for _, id := range v.SilencedBy {
			for _, sl := range d.Silences {
				if sl.ID == id {
					part.Related = append(part.Related, sl.Ref)
				}
			}
		}
		ns, pod := v.Labels["namespace"], v.Labels["pod"]
		if queries.ValidDNS(ns) && queries.ValidDNS(pod) {
			part.Related = append(part.Related, "kubernetes:Pod/"+ns+"/"+pod)
			container := v.Labels["container"]
			if !queries.ValidDNS(container) {
				container = ""
			}
			part.Logs = &model.LogsRef{Namespace: ns, Pod: pod, Container: container}
		}
	case model.Silence:
		for _, a := range d.Alerts {
			for _, id := range a.SilencedBy {
				if id == v.ID {
					part.Related = append(part.Related, a.Ref)
				}
			}
		}
	}
	return part, nil
}

var (
	_ plugin.Objects = (*Plugin)(nil)
	_ plugin.Objects = (*twin)(nil)
)
