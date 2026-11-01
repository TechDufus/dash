package headlamp

import (
	"context"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Demo is the twin that runs under DASH_DEMO=true: the same manifest and links, pointed at the
// fake lab's Headlamp on example.com whatever the configuration says.
func (*Plugin) Demo() plugin.Plugin { return &twin{} }

type twin struct{ p Plugin }

func (*twin) Manifest() plugin.Manifest { return manifest() }

func (t *twin) Start(context.Context, plugin.Host, any) error {
	t.p.set(world.HeadlampURL, world.HeadlampCluster)
	return nil
}

func (t *twin) Stop(ctx context.Context) error { return t.p.Stop(ctx) }
func (t *twin) Status() plugin.Status          { return t.p.Status() }

func (t *twin) Links(req plugin.LinkRequest) []model.Link { return t.p.Links(req) }

var _ plugin.LinkProvider = (*twin)(nil)
