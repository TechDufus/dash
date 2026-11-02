package overview

import (
	"testing"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

func TestMergeSources(t *testing.T) {
	ok := func(n string) model.SourceStatus { return model.SourceStatus{Name: n, OK: true} }
	bad := func(n, e string) model.SourceStatus { return model.SourceStatus{Name: n, Error: e} }
	for _, c := range []struct {
		name        string
		base, extra []model.SourceStatus
		want        []model.SourceStatus
	}{
		{"nothing", nil, nil, []model.SourceStatus{}},
		{"new names follow, base order kept", []model.SourceStatus{ok("a")}, []model.SourceStatus{ok("c"), ok("b")}, []model.SourceStatus{ok("a"), ok("c"), ok("b")}},
		{"a failing duplicate wins", []model.SourceStatus{ok("a")}, []model.SourceStatus{bad("a", "timeout")}, []model.SourceStatus{bad("a", "timeout")}},
		{"an ok duplicate does not hide a failure", []model.SourceStatus{bad("a", "tls")}, []model.SourceStatus{ok("a")}, []model.SourceStatus{bad("a", "tls")}},
		{"duplicates inside extra", nil, []model.SourceStatus{ok("a"), bad("a", "auth")}, []model.SourceStatus{bad("a", "auth")}},
	} {
		got := mergeSources(c.base, c.extra)
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
				break
			}
		}
	}
}

// sourceNamed finds a source of an envelope by name.
func sourceNamed(srcs []model.SourceStatus, name string) (model.SourceStatus, bool) {
	for _, s := range srcs {
		if s.Name == name {
			return s, true
		}
	}
	return model.SourceStatus{}, false
}

// A contributor's source health reaches core.overview.sources, so a dead upstream of a plugin still
// shows on the Overview panels; a plugin that contributes no source adds no chip.
func TestOverviewCarriesContributorSources(t *testing.T) {
	h := hub.New()
	src := &source{items: []model.AttentionItem{{ID: "fake:1", Severity: model.SeverityWarn}}}
	failing := true
	m := testPublisherWith(plugintest.AllRunning(h), func() []plugin.Contributor {
		return []plugin.Contributor{{ID: "fake", Attention: src, Sources: func() []model.SourceStatus {
			if failing {
				return []model.SourceStatus{{Name: "fakeupstream", Error: "unreachable"}}
			}
			return []model.SourceStatus{{Name: "fakeupstream", OK: true}}
		}}}
	})
	m.rebuild()
	env, ok := latest(h, model.TopicOverview)
	if !ok {
		t.Fatal("no overview")
	}
	if s, found := sourceNamed(env.Sources, "fakeupstream"); !found || s.OK || s.Error != "unreachable" {
		t.Fatalf("sources = %+v", env.Sources)
	}
	if len(env.Sources) != 1 {
		t.Errorf("only what the plugins report is on the overview, no chip of the publisher's own: %+v", env.Sources)
	}

	failing = false
	m.rebuild()
	env, _ = latest(h, model.TopicOverview)
	if s, found := sourceNamed(env.Sources, "fakeupstream"); !found || !s.OK {
		t.Fatalf("after recovery: %+v", env.Sources)
	}
}
