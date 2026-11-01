package topics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// alertmanagerOn is the alertmanager row, pointing at url.
func alertmanagerOn(url string) plugin.InstanceConfig {
	c := pluginconf.DefaultAlertmanager()
	c.URL = url
	return plugin.InstanceConfig{Name: "alertmanager", Config: &c}
}

func alertmanagerOff(url string) plugin.InstanceConfig {
	off := false
	c := pluginconf.DefaultAlertmanager()
	c.URL = url
	return plugin.InstanceConfig{Name: "alertmanager", Enabled: &off, Config: &c}
}

// fakeAlertmanager serves one firing critical alert and one that is silenced.
func fakeAlertmanager(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/alerts":
			_, _ = w.Write([]byte(`[
{"fingerprint":"f1","labels":{"alertname":"NodeDown","severity":"critical"},"annotations":{"summary":"cp-2 is down"},"startsAt":"2026-10-30T11:10:00Z","status":{"state":"active","silencedBy":[],"inhibitedBy":[]}},
{"fingerprint":"f3","labels":{"alertname":"Muted","severity":"critical"},"annotations":{},"startsAt":"2026-10-30T11:00:00Z","status":{"state":"suppressed","silencedBy":["s1"],"inhibitedBy":[]}}]`))
		case "/api/v2/silences":
			_, _ = w.Write([]byte(`[{"id":"5b1c2d3e-aaaa","status":{"state":"active"},"matchers":[{"name":"alertname","value":"Muted","isRegex":false,"isEqual":true}],"createdBy":"dash:alice","comment":"maintenance","startsAt":"2026-10-30T10:00:00Z","endsAt":"2026-10-30T22:00:00Z"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// overview is the decoded core.overview envelope with its sources, or ok=false.
func (r *linkRig) overview(t *testing.T) (model.OverviewData, []model.SourceStatus, bool) {
	t.Helper()
	env, ok := latest(r.h, model.TopicOverview)
	if !ok {
		return model.OverviewData{}, nil, false
	}
	var d model.OverviewData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d, env.Sources, true
}

func alertItem(d model.OverviewData, id string) (model.AttentionItem, bool) {
	for _, it := range d.Attention {
		if it.ID == id {
			return it, true
		}
	}
	return model.AttentionItem{}, false
}

func hasSource(srcs []model.SourceStatus, name string) (model.SourceStatus, bool) {
	for _, s := range srcs {
		if s.Name == name {
			return s, true
		}
	}
	return model.SourceStatus{}, false
}

// The alert of the overview comes from the running alertmanager plugin: it carries the object
// ref (must-hold b), the plugin and the /alerts route, and its link is whatever the running link
// providers resolve for Kind "Alerts": the Grafana alert list while grafana runs, none after.
func TestAlertmanagerAttentionReachesTheOverviewWithRefAndLinks(t *testing.T) {
	am := fakeAlertmanager(t)
	r := newLinkRig(t, config.Defaults(), grafanaOn("https://gf.example.com"), headlampOn("https://hl.example.com"), alertmanagerOn(am.URL))

	var got model.AttentionItem
	eventually(t, "the alert on the overview", func() bool {
		d, _, ok := r.overview(t)
		if !ok {
			return false
		}
		got, ok = alertItem(d, "alert:f1")
		return ok
	})
	if got.Severity != model.SeverityCrit || got.Plugin != "alertmanager" || got.Source != "alertmanager" ||
		got.Route != "/alerts" || got.Title != "NodeDown" || got.Detail != "cp-2 is down" {
		t.Errorf("item = %+v", got)
	}
	if got.Ref != "alertmanager:Alert/f1" {
		t.Errorf("ref = %q, want alertmanager:Alert/f1", got.Ref)
	}
	if len(got.Links) != 1 || got.Links[0].Kind != model.LinkKindGrafana || got.Links[0].Href != "https://gf.example.com/alerting/list" {
		t.Errorf("links = %+v, want the Grafana alert list", got.Links)
	}
	d, _, _ := r.overview(t)
	if len(d.Attention) != 1 {
		t.Errorf("a silenced alert is not attention: %+v", d.Attention)
	}

	// The topic itself, read as a viewer's stream carries it: the name is the plugin's, and every
	// field the plugin owns is filled (must-hold b).
	var env model.Envelope
	eventually(t, "the alertmanager.alerts topic", func() bool {
		var ok bool
		env, ok = latest(r.h, model.TopicAlerts)
		return ok
	})
	if env.Topic != "alertmanager.alerts" {
		t.Fatalf("alerts envelope = %+v", env)
	}
	var ad model.AlertsData
	if err := json.Unmarshal(env.Data, &ad); err != nil {
		t.Fatal(err)
	}
	if len(ad.Alerts) != 2 || len(ad.Silences) != 1 {
		t.Fatalf("alerts data = %+v", ad)
	}
	for _, a := range ad.Alerts {
		if a.Ref != "alertmanager:Alert/"+a.Fingerprint {
			t.Errorf("alert %s ref = %q", a.Name, a.Ref)
		}
	}
	if s := ad.Silences[0]; s.Ref != "alertmanager:Silence/5b1c2d3e-aaaa" || !s.Dash {
		t.Errorf("silence = %+v, want its ref and dash set (created by dash:alice)", s)
	}
	if len(ad.SilenceDurations) != 4 || ad.SilenceDurations[0] != "1h" {
		t.Errorf("silenceDurations = %v, want the configured list", ad.SilenceDurations)
	}
	// The firing count is the plugin's KPI, and the KPI is the only place it lives.
	if len(d.KPIList) != 1 || d.KPIList[0].Key != "firing" || d.KPIList[0].Plugin != "alertmanager" ||
		d.KPIList[0].Value != 1 || d.KPIList[0].Health != model.HealthCrit {
		t.Errorf("kpiList = %+v", d.KPIList)
	}

	// Grafana off: the next rebuild drops the link, the item stays.
	r.set.Set(grafanaOff())
	r.sup.Reload()
	eventually(t, "the alert to lose its Grafana link", func() bool {
		d, _, ok := r.overview(t)
		it, has := alertItem(d, "alert:f1")
		return ok && has && it.Links != nil && len(it.Links) == 0
	})
}

// Source health survives the move (review S-3): with alertmanager down the overview still lists
// it, with the category from the plugin's Status().Sources and nothing of the upstream; a row
// that is disabled, or has no URL, adds no chip.
func TestAlertmanagerDownStillListsItsOverviewSource(t *testing.T) {
	am := fakeAlertmanager(t)
	url := am.URL
	am.Close()
	r := newLinkRig(t, config.Defaults(), alertmanagerOn(url))
	eventually(t, "the failing alertmanager on the overview sources", func() bool {
		_, srcs, ok := r.overview(t)
		s, has := hasSource(srcs, "alertmanager")
		return ok && has && !s.OK && s.Error == "unreachable"
	})
	_, srcs, _ := r.overview(t)
	raw, _ := json.Marshal(srcs)
	if got := string(raw); strings.Contains(got, url) || strings.Contains(got, "127.0.0.1") {
		t.Errorf("the overview sources carry the upstream: %s", got)
	}

	// Hidden by its row: no chip once the plugin stops.
	r.set.Set(alertmanagerOff(url))
	r.sup.Reload()
	eventually(t, "the chip to go with the plugin", func() bool {
		r.m.mark(model.TopicStatus) // with nothing contributing, the overview waits for a topic to change
		_, srcs, ok := r.overview(t)
		_, has := hasSource(srcs, "alertmanager")
		return ok && !has
	})

	// No URL: the row is disabled from the start and has no chip either.
	r2 := newLinkRig(t, config.Defaults())
	eventually(t, "an overview", func() bool { _, _, ok := r2.overview(t); return ok })
	if _, srcs, _ := r2.overview(t); len(srcs) > 0 {
		if _, has := hasSource(srcs, "alertmanager"); has {
			t.Errorf("an unconfigured alertmanager has a source chip: %+v", srcs)
		}
	}
}
