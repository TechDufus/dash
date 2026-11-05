package prometheus

import (
	"context"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/svc"
)

// Demo is the twin that runs under DASH_DEMO=true: the same manifest, no upstream, and a
// Querier that draws synthetic series shaped like the real queries' results. Its instant
// values are read at the host's clock (demo mode's fixed clock, when set).
func (*Plugin) Demo() plugin.Plugin { return &twin{q: demoQuerier{now: time.Now}} }

type twin struct{ q demoQuerier }

func (*twin) Manifest() plugin.Manifest { return manifest() }
func (t *twin) Start(_ context.Context, h plugin.Host, _ any) error {
	t.q = demoQuerier{now: h.Now}
	return nil
}
func (*twin) Stop(context.Context) error { return nil }
func (*twin) Status() plugin.Status      { return plugin.Status{State: plugin.StateHealthy} }
func (t *twin) Service() any             { return svc.Querier(t.q) }

var _ plugin.Provider = (*twin)(nil)

// demoQuerier answers from internal/demo/world, the same fake lab the demo's topics come from,
// so a chart and the number next to it agree.
type demoQuerier struct{ now func() time.Time }

func (q demoQuerier) Instant(_ context.Context, expr string) ([]svc.Sample, error) {
	return world.Instant(expr, q.now()), nil
}

func (demoQuerier) Range(_ context.Context, expr string, start, end time.Time, step time.Duration) ([]model.SeriesLine, error) {
	return world.Series(expr, start, end, step), nil
}
