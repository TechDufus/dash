package flux

import (
	"context"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Context implements plugin.Objects: the object, its Ready condition and its namespace.
func (p *Plugin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return objectContext(&p.ObjectStore, ref)
}

// Context implements plugin.Objects for the twin, like the plugin.
func (t *twin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return objectContext(&t.ObjectStore, ref)
}

func objectContext(s *plugin.ObjectStore, ref string) (model.ContextPart, error) {
	part, obj, err := s.Part(ref)
	if err != nil {
		return part, err
	}
	if o, ok := obj.(model.FluxObj); ok {
		ready := "False"
		if o.Ready {
			ready = "True"
		}
		part.Conditions = append(part.Conditions, model.Condition{Type: "Ready", Status: ready})
		part.Related = append(part.Related, "kubernetes:Namespace/"+o.Namespace)
	}
	return part, nil
}

var (
	_ plugin.Objects = (*Plugin)(nil)
	_ plugin.Objects = (*twin)(nil)
)
