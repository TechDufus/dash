package gatus

import (
	"context"
	"fmt"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Context implements plugin.Objects: the endpoint and how its recent checks went.
func (p *Plugin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return endpointContext(&p.ObjectStore, ref)
}

// Context implements plugin.Objects for the twin, like the plugin.
func (t *twin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return endpointContext(&t.ObjectStore, ref)
}

func endpointContext(s *plugin.ObjectStore, ref string) (model.ContextPart, error) {
	part, obj, err := s.Part(ref)
	if err != nil {
		return part, err
	}
	if ep, ok := obj.(model.Endpoint); ok && len(ep.Results) > 0 {
		failed := 0
		for _, r := range ep.Results {
			if !r.Success {
				failed++
			}
		}
		last := ep.Results[len(ep.Results)-1]
		part.Facts = append(part.Facts,
			model.ContextFact{Name: "recentChecks", Value: fmt.Sprintf("%d of %d failed", failed, len(ep.Results))},
			model.ContextFact{Name: "lastCheck", Value: fmt.Sprintf("success %t at %s in %.0f ms", last.Success, last.Timestamp.UTC().Format("2006-01-02T15:04:05Z"), last.DurationMs)},
		)
	}
	return part, nil
}

var (
	_ plugin.Objects = (*Plugin)(nil)
	_ plugin.Objects = (*twin)(nil)
)
