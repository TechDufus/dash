package cnpg

import (
	"context"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Context implements plugin.Objects: the cluster and its namespace.
func (p *Plugin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return clusterContext(&p.ObjectStore, ref)
}

// Context implements plugin.Objects for the twin, like the plugin.
func (t *twin) Context(_ context.Context, ref string, _ model.ContextOptions) (model.ContextPart, error) {
	return clusterContext(&t.ObjectStore, ref)
}

func clusterContext(s *plugin.ObjectStore, ref string) (model.ContextPart, error) {
	part, obj, err := s.Part(ref)
	if err != nil {
		return part, err
	}
	if c, ok := obj.(model.CNPGCluster); ok {
		part.Related = append(part.Related, "kubernetes:Namespace/"+c.Namespace)
	}
	return part, nil
}

var (
	_ plugin.Objects = (*Plugin)(nil)
	_ plugin.Objects = (*twin)(nil)
)
