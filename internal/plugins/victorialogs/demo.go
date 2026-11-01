package victorialogs

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/upstream"
)

// demoTailInterval is how often the demo tail emits a line.
const demoTailInterval = 2 * time.Second

// Demo implements plugin.Demo: the twin has the same manifest and routes (so the same
// validation, scrubbing and resume), answers from the fake world, and contacts nothing.
func (p *Plugin) Demo() plugin.Plugin { return &Plugin{demo: true} }

func (p *Plugin) startDemo(h plugin.Host) error {
	f := upstream.DefaultLogFields()
	p.st.Store(&running{src: demoSource{fields: f, now: h.Now}, fields: f, scrub: h.ScrubLog, now: h.Now})
	return nil
}

// demoSource is the source of the twin: synthetic lines from internal/demo/world.
type demoSource struct {
	fields upstream.LogFields
	now    func() time.Time
	every  time.Duration // between tail lines; demoTailInterval when zero
}

func (d demoSource) Query(_ context.Context, q upstream.LogQuery) ([]model.LogLine, error) {
	if _, err := upstream.BuildQuery(q, d.fields, false); err != nil {
		return nil, err
	}
	var out []model.LogLine
	for _, l := range world.LogsAt(q.Pod, q.Container, d.now()) {
		if strings.Contains(l.Msg, q.Text) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (d demoSource) Tail(ctx context.Context, q upstream.LogQuery, emit func(model.LogLine) error) error {
	if _, err := upstream.BuildQuery(q, d.fields, true); err != nil {
		return err
	}
	return world.TailLogs(ctx, q.Pod, q.Container, cmp.Or(d.every, demoTailInterval), d.now, func(l model.LogLine) error {
		if !strings.Contains(l.Msg, q.Text) {
			return nil
		}
		return emit(l)
	})
}
