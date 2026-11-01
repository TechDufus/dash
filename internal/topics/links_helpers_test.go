package topics

import (
	"context"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/grafana"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
)

// providers is a LinkResolver over started link providers, in order. It stands in for
// Supervisor.Links where a test has no supervisor.
type providers []plugin.LinkProvider

func (ps providers) Resolve(req plugin.LinkRequest) []model.Link {
	out := []model.Link{}
	for _, p := range ps {
		out = append(out, p.Links(req)...)
	}
	return out
}

// labLinks is the resolver of a lab with Headlamp (cluster "demo") and Grafana on example.com:
// the real plugins, started on their own, so the links are the ones a running dash builds.
func labLinks(t *testing.T) plugin.LinkResolver {
	t.Helper()
	hl, gr := headlamp.New(), grafana.New()
	if err := hl.Start(context.Background(), nil, &pluginconf.Headlamp{URL: "https://headlamp.example.com", Cluster: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := gr.Start(context.Background(), nil, &pluginconf.Grafana{URL: "https://grafana.example.com"}); err != nil {
		t.Fatal(err)
	}
	return providers{hl.(plugin.LinkProvider), gr.(plugin.LinkProvider)}
}
