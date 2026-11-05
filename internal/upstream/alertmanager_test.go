package upstream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

const amAlertsJSON = `[
 {"annotations":{"description":"always firing","summary":"Watchdog"},"endsAt":"2026-10-30T21:41:17.226Z","fingerprint":"e2306103db8679b0","receivers":[{"name":"healthchecks"}],"startsAt":"2026-10-02T21:15:20.000Z","status":{"inhibitedBy":[],"mutedBy":[],"silencedBy":[],"state":"active"},"updatedAt":"2026-10-30T21:39:57.228Z","generatorURL":"http:/explore","labels":{"alertgroup":"general.rules","alertname":"Watchdog","severity":"none"}},
 {"annotations":{},"fingerprint":"1","startsAt":"2026-10-02T21:15:20.000Z","status":{"inhibitedBy":[],"silencedBy":[],"state":"active"},"labels":{"alertname":"InfoInhibitor","severity":"none"}},
 {"annotations":{"description":"DIMM B8 ECC","summary":"IPMI SEL"},"fingerprint":"abc123","startsAt":"2026-10-30T03:14:30.000Z","status":{"inhibitedBy":null,"silencedBy":["5b1c2d3e-aaaa-bbbb-cccc-0123456789ab"],"state":"suppressed"},"labels":{"alertname":"ProxmoxIpmiSelCriticalEntry","severity":"warning","instance":"node-1"}}
]`

func TestAlertmanagerAlerts(t *testing.T) {
	var rawQuery string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		if r.URL.Path != "/api/v2/alerts" {
			t.Errorf("path %s", r.URL.Path)
		}
		io.WriteString(w, amAlertsJSON)
	})
	got, err := NewAlertmanager(s.URL, nil, []string{"Watchdog", "InfoInhibitor"}).Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rawQuery != "active=true&inhibited=true&silenced=true" {
		t.Fatalf("query = %q", rawQuery)
	}
	if len(got) != 1 {
		t.Fatalf("Watchdog/InfoInhibitor not dropped: %+v", got)
	}
	a := got[0]
	if a.Fingerprint != "abc123" || a.Name != "ProxmoxIpmiSelCriticalEntry" || a.Severity != "warning" ||
		a.State != "suppressed" || a.Summary != "IPMI SEL" || a.Description != "DIMM B8 ECC" ||
		a.Labels["instance"] != "node-1" || !a.StartsAt.Equal(time.Date(2026, 10, 30, 3, 14, 30, 0, time.UTC)) ||
		len(a.SilencedBy) != 1 || a.InhibitedBy == nil || len(a.InhibitedBy) != 0 {
		t.Fatalf("alert: %+v", a)
	}

	// An empty answer is an empty non-nil slice.
	s2 := serve(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `[]`) })
	empty, err := NewAlertmanager(s2.URL, nil, nil).Alerts(ctx)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %v, %v", empty, err)
	}
}

// The alert names to drop are the caller's, not literals: with none configured the meta alerts
// come through, and any name can be dropped.
func TestAlertmanagerIgnoreListIsTheOnlyFilter(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, amAlertsJSON) })
	names := func(ignore []string) []string {
		got, err := NewAlertmanager(s.URL, nil, ignore).Alerts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range got {
			out = append(out, a.Name)
		}
		return out
	}
	if got := names(nil); !reflect.DeepEqual(got, []string{"Watchdog", "InfoInhibitor", "ProxmoxIpmiSelCriticalEntry"}) {
		t.Errorf("no ignore list: %v", got)
	}
	if got := names([]string{"ProxmoxIpmiSelCriticalEntry"}); !reflect.DeepEqual(got, []string{"Watchdog", "InfoInhibitor"}) {
		t.Errorf("custom ignore list: %v", got)
	}
}

func TestAlertmanagerTakesTheTimeoutOption(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, `[]`)
	})
	if _, err := NewAlertmanager(s.URL, nil, nil, Timeout(30*time.Millisecond)).Alerts(ctx); err == nil {
		t.Error("the 30ms timeout option did not apply")
	}
}

func TestValidSilenceID(t *testing.T) {
	for id, want := range map[string]bool{
		"5b1c2d3e-aaaa-bbbb-cccc-0123456789ab": true, "ABCDEF12": true,
		"": false, "short": false, "../../x": false, "abc/def-1234": false, "zzzzzzzz": false,
	} {
		if got := ValidSilenceID(id); got != want {
			t.Errorf("ValidSilenceID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestAlertmanagerSilences(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[
		 {"id":"11111111-2222","status":{"state":"active"},"matchers":[{"name":"alertname","value":"Foo","isRegex":false,"isEqual":true},{"name":"ns","value":"a|b","isRegex":true,"isEqual":false}],"createdBy":"dash","comment":"c","startsAt":"2026-10-30T20:00:00Z","endsAt":"2026-10-30T22:00:00Z"},
		 {"id":"33333333-4444","status":{"state":"pending"},"matchers":[],"createdBy":"x","comment":"","startsAt":"2026-10-31T20:00:00Z","endsAt":"2026-10-31T22:00:00Z"},
		 {"id":"55555555-6666","status":{"state":"expired"},"matchers":[],"startsAt":"2026-10-01T20:00:00Z","endsAt":"2026-10-01T22:00:00Z"}]`)
	})
	got, err := NewAlertmanager(s.URL, nil, nil).Silences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "11111111-2222" || got[1].State != "pending" {
		t.Fatalf("got %+v", got)
	}
	wantM := []model.Matcher{{Name: "alertname", Value: "Foo", IsEqual: true}, {Name: "ns", Value: "a|b", IsRegex: true}}
	if !reflect.DeepEqual(got[0].Matchers, wantM) || got[1].Matchers == nil {
		t.Fatalf("matchers: %+v / %+v", got[0].Matchers, got[1].Matchers)
	}
}

func TestAlertmanagerCreateSilence(t *testing.T) {
	var body map[string]any
	var method, path, ctype string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"silenceID":"abcdef12-3456"}`)
	})
	start := time.Date(2026, 10, 30, 22, 0, 0, 0, time.FixedZone("CDT", -5*3600))
	id, err := NewAlertmanager(s.URL, nil, nil).CreateSilence(ctx, SilenceSpec{
		Matchers:  []model.SilenceMatcher{{Name: "alertname", Value: "Foo"}, {Name: "namespace", Value: "gatus"}},
		StartsAt:  start,
		EndsAt:    start.Add(4 * time.Hour),
		CreatedBy: "alice",
		Comment:   "maintenance",
	})
	if err != nil || id != "abcdef12-3456" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if method != http.MethodPost || path != "/api/v2/silences" || ctype != "application/json" {
		t.Fatalf("request: %s %s %s", method, path, ctype)
	}
	want := map[string]any{
		"matchers": []any{
			map[string]any{"name": "alertname", "value": "Foo", "isRegex": false, "isEqual": true},
			map[string]any{"name": "namespace", "value": "gatus", "isRegex": false, "isEqual": true},
		},
		"startsAt":  "2026-10-31T03:00:00Z",
		"endsAt":    "2026-10-31T07:00:00Z",
		"createdBy": "alice",
		"comment":   "maintenance",
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v\nwant %v", body, want)
	}
	if _, err := NewAlertmanager(s.URL, nil, nil).CreateSilence(ctx, SilenceSpec{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no matchers: %v", err)
	}
}

func TestAlertmanagerExpireSilence(t *testing.T) {
	var method, path string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	am := NewAlertmanager(s.URL, nil, nil)
	if err := am.ExpireSilence(ctx, "5b1c2d3e-aaaa-bbbb-cccc-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/api/v2/silence/5b1c2d3e-aaaa-bbbb-cccc-0123456789ab" {
		t.Fatalf("request: %s %s", method, path)
	}
	for _, id := range []string{"", "short", "../../x", "abc/def-1234", "zzzzzzzz"} {
		method = ""
		if err := am.ExpireSilence(ctx, id); !errors.Is(err, ErrInvalid) || method != "" {
			t.Errorf("id %q: err=%v, request sent=%q", id, err, method)
		}
	}

	missing := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "silence not found", http.StatusNotFound)
	})
	err := NewAlertmanager(missing.URL, nil, nil).ExpireSilence(ctx, "5b1c2d3e-aaaa")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 || se.Body != "silence not found" {
		t.Fatalf("err = %v", err)
	}
}
