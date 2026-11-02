package plugin_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/queries"
)

// selectorFactory builds, on every call, a plugin whose series expression closes over the
// selector its own Start received, the way a plugin reads its settings (R3-6).
func selectorFactory(starts *atomic.Int32) plugin.Factory {
	return func() plugin.Plugin {
		var sel atomic.Pointer[string]
		f := plugintest.New("aa", plugintest.Config(cfgOf))
		f.M.Series = []plugin.SeriesSpec{{Name: "vm.cpu", Expr: func(plugin.SeriesArgs) (string, error) {
			s := sel.Load()
			if s == nil {
				return "", errors.New("not started")
			}
			return "cpu{" + *s + "}", nil
		}}}
		f.StartFn = func(_ context.Context, _ plugin.Host, c any) error {
			starts.Add(1)
			s := c.(*cfg).URL
			sel.Store(&s)
			return nil
		}
		return f
	}
}

func exprOf(t *testing.T, e *env, name string) string {
	t.Helper()
	sp, _, ok := e.sup.Series(name)
	if !ok {
		t.Fatalf("series %q not found", name)
	}
	q, err := sp.Expr(plugin.SeriesArgs{})
	if err != nil {
		t.Fatalf("Expr: %v", err)
	}
	return q
}

// A series that depends on the plugin's settings must follow a reload: the restarted instance is
// a new plugin, and the expression of the one captured when the instance was added belongs to
// the probe that was started first.
func TestSeriesFollowsTheInstanceAfterAReload(t *testing.T) {
	var starts atomic.Int32
	e := newEnv(t, nil, selectorFactory(&starts))
	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "one"}})
	e.run(t)
	if got := exprOf(t, e, "vm.cpu"); got != "cpu{one}" {
		t.Fatalf("first start: %q", got)
	}

	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "two"}})
	e.reloadUntil(t, "the restart", func() bool { return starts.Load() == 2 })
	if got := exprOf(t, e, "vm.cpu"); got != "cpu{two}" {
		t.Fatalf("after the reload the series still reads the previous instance: %q", got)
	}

	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "three"}})
	e.reloadUntil(t, "the second restart", func() bool { return starts.Load() == 3 })
	if got := exprOf(t, e, "vm.cpu"); got != "cpu{three}" {
		t.Fatalf("second reload: %q", got)
	}

	// Stopped, the series is gone.
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.reloadUntil(t, "the stop", func() bool { _, _, ok := e.sup.Series("vm.cpu"); return !ok })
}

// In demo mode the running plugin is the twin, which is never the probe: its series must be the
// ones served, not the real plugin's.
func TestSeriesComeFromTheDemoTwin(t *testing.T) {
	spec := func(q string) []plugin.SeriesSpec {
		return []plugin.SeriesSpec{{Name: "vm.cpu", Expr: func(plugin.SeriesArgs) (string, error) { return q, nil }}}
	}
	real := plugintest.New("aa", func(f *plugintest.Fake) { f.M.Series = spec("real") })
	twin := plugintest.New("aa", func(f *plugintest.Fake) { f.M.Series = spec("twin") })
	e := newEnv(t, func(o *plugin.Options) { o.Demo = true }, plugintest.WithDemo(real, twin)).run(t)
	if got := exprOf(t, e, "vm.cpu"); got != "twin" {
		t.Errorf("demo series = %q, want the twin's", got)
	}

	// Without demo mode the real plugin's series are served.
	e2 := start(t, plugintest.WithDemo(real, twin))
	if got := exprOf(t, e2, "vm.cpu"); got != "real" {
		t.Errorf("series = %q, want the real plugin's", got)
	}
}

// plugin.ErrNotFound survives the wrapping an Expr gives it and is not confused with the
// "bad series" class the other Expr errors belong to.
func TestSeriesExprCanSayNotFound(t *testing.T) {
	f := plugintest.New("aa", func(f *plugintest.Fake) {
		f.M.Series = []plugin.SeriesSpec{{Name: "vm.cpu", Expr: func(a plugin.SeriesArgs) (string, error) {
			return "", fmt.Errorf("%w: unknown node %q", plugin.ErrNotFound, a.Instance)
		}}}
	})
	e := start(t, f.Factory())
	sp, _, ok := e.sup.Series("vm.cpu")
	if !ok {
		t.Fatal("series not found")
	}
	_, err := sp.Expr(plugin.SeriesArgs{Instance: "n1"})
	if !errors.Is(err, plugin.ErrNotFound) || errors.Is(err, queries.ErrBadSeries) || err.Error() != `not found: unknown node "n1"` {
		t.Errorf("err = %v", err)
	}
}
