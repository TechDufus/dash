package plugin_test

import (
	"context"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// bare implements Plugin and nothing else.
type bare struct{ id plugin.ID }

func (b bare) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: b.id, Name: string(b.id), Version: "1.0.0"}
}
func (bare) Start(context.Context, plugin.Host, any) error { return nil }
func (bare) Stop(context.Context) error                    { return nil }
func (bare) Status() plugin.Status                         { return plugin.Status{State: plugin.StateHealthy} }

// attentive adds an overview source to bare.
type attentive struct{ bare }

func (attentive) Attention(time.Time) []model.AttentionItem { return nil }

func TestContributorsAreRunningPluginsWithASource(t *testing.T) {
	off := false
	fake := plugintest.New("fk")
	e := newEnv(t, nil,
		func() plugin.Plugin { return bare{"bb"} },
		func() plugin.Plugin { return attentive{bare{"at"}} },
		fake.Factory(),
		plugintest.New("of").Factory(),
	)
	e.set.Set(plugin.InstanceConfig{Name: "of", Enabled: &off})
	e.run(t)

	got := e.sup.Contributors()
	var ids []plugin.ID
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if len(ids) != 2 || ids[0] != "at" || ids[1] != "fk" {
		t.Fatalf("contributors = %v, want [at fk] in registry order (not bb: no source; not of: disabled)", ids)
	}
	if got[0].Attention == nil || got[0].KPIs != nil || got[0].Topology != nil {
		t.Errorf("at implements only AttentionSource: %+v", got[0])
	}
	if got[1].Attention == nil || got[1].KPIs == nil || got[1].Topology == nil {
		t.Errorf("the fake implements all three: %+v", got[1])
	}
}
