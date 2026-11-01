package alertmanager

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// A twin publishes the fixture world without contacting anything, and its actions run for real on
// the in-memory list.
func TestDemoTwinPublishesTheWorldAndNeverCallsOut(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(srv.Close)

	e := newEnv(t, envOpts{demo: true, am: &fakeAM{srv: srv}, cfg: func(c *pluginconf.Alertmanager) {
		c.URL = "" // demo ignores the configuration
		c.Actions.Enabled = false
	}})
	d, sources := e.topic(t, "the fixture", func(d model.AlertsData) bool { return len(d.Alerts) == 3 })
	if len(sources) != 1 || !sources[0].OK || sources[0].Name != "alertmanager" {
		t.Errorf("sources = %+v", sources)
	}
	if !slices.Equal(d.SilenceDurations, []string{"1h", "4h", "24h", "7d"}) {
		t.Errorf("durations = %v", d.SilenceDurations)
	}
	plugintest.AssertRefs(t, e.sup, ID, d, model.Alert{}, model.Silence{})
	if pi := e.info(t); pi.State != plugin.StateHealthy || len(pi.Actions) != 2 {
		t.Errorf("row = %s %q with %d actions: demo forces actions on", pi.State, pi.Message, len(pi.Actions))
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the twin made %d requests", n)
	}
}

func TestDemoTwinSilenceLifecycle(t *testing.T) {
	e := newEnv(t, envOpts{demo: true})
	e.topic(t, "the fixture", func(d model.AlertsData) bool { return len(d.Alerts) == 3 })

	firingNames := func(d model.AlertsData) []string {
		var out []string
		for _, a := range d.Alerts {
			if a.State == "active" {
				out = append(out, a.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	cs := e.sup.Contributors()
	if len(cs) != 1 || cs[0].KPIs == nil || cs[0].KPIs.KPIs()[0].Value != 2 {
		t.Fatalf("contributors = %+v, want 2 firing", cs)
	}

	out, err := e.act(t, "silence.create", createIn(func(i *silenceCreateInput) {
		i.Matchers = []model.SilenceMatcher{{Name: "alertname", Value: "KubePodCrashLooping"}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	o := out.(plugin.Outcome)
	id := o.Body.(model.SilenceResponse).SilenceID
	if o.Target == nil || o.Target.Name != id {
		t.Fatalf("outcome = %+v", o)
	}
	d, _ := e.topic(t, "the silence", func(d model.AlertsData) bool { return len(d.Silences) == 2 })
	if !slices.Equal(firingNames(d), []string{"PersistentVolumeFillingUp"}) {
		t.Errorf("firing after the silence: %v", firingNames(d))
	}
	var mine model.Silence
	for _, s := range d.Silences {
		if s.ID == id {
			mine = s
		}
	}
	if !mine.Dash || mine.CreatedBy != "dash:alice" || mine.Ref != "alertmanager:Silence/"+id || mine.State != "active" {
		t.Errorf("created silence = %+v", mine)
	}
	if k := e.sup.Contributors()[0].KPIs.KPIs()[0].Value; k != 1 {
		t.Errorf("KPI after the silence = %v, want 1", k)
	}

	// Its id passes the expire action's own check, and expiring unmutes the alert.
	if _, err := e.act(t, "silence.expire", &silenceExpireInput{ID: id}); err != nil {
		t.Fatal(err)
	}
	d, _ = e.topic(t, "the expiry", func(d model.AlertsData) bool { return len(d.Silences) == 1 })
	if !slices.Equal(firingNames(d), []string{"KubePodCrashLooping", "PersistentVolumeFillingUp"}) {
		t.Errorf("firing after the expiry: %v", firingNames(d))
	}
}

func TestDemoTwinKeepsTheRules(t *testing.T) {
	e := newEnv(t, envOpts{demo: true})
	e.topic(t, "the fixture", func(d model.AlertsData) bool { return len(d.Alerts) == 3 })

	_, err := e.act(t, "silence.create", createIn(func(i *silenceCreateInput) { i.Matchers[0].Value = "Watchdog" }))
	if ae := actionError(t, err); ae.Status != http.StatusForbidden {
		t.Errorf("protected: %+v", ae)
	}
	_, err = e.act(t, "silence.create", createIn(func(i *silenceCreateInput) { i.Duration = "30d" }))
	if ae := actionError(t, err); ae.Status != http.StatusBadRequest {
		t.Errorf("duration: %+v", ae)
	}
	// The fixture's own silence was not made by dash.
	_, err = e.act(t, "silence.expire", &silenceExpireInput{ID: "6f1e3d2c-0d3e-4a11-8c00-0000000000d3"})
	if ae := actionError(t, err); ae.Status != http.StatusForbidden {
		t.Errorf("the seeded silence: %+v, want 403", ae)
	}
	_, err = e.act(t, "silence.expire", &silenceExpireInput{ID: "00000000-9999-4000"})
	if ae := actionError(t, err); ae.Status != http.StatusNotFound {
		t.Errorf("unknown: %+v", ae)
	}
}
