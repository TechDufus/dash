package launcher

import (
	"context"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/plugin"
)

// Demo is the twin that runs under DASH_DEMO=true: the same manifest, publishing the fake lab's
// links (internal/demo/world) and reading no file.
func (*Plugin) Demo() plugin.Plugin { return twin{} }

type twin struct{}

func (twin) Manifest() plugin.Manifest { return manifest() }

func (twin) Start(_ context.Context, h plugin.Host, _ any) error {
	h.Publish(topicLinks, world.Launcher(), nil)
	return nil
}

func (twin) Stop(context.Context) error { return nil }
func (twin) Status() plugin.Status      { return plugin.Status{State: plugin.StateHealthy} }
