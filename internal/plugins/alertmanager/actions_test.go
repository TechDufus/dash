package alertmanager

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/jsonschema"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// A silence request holds {name, value} matchers only, and the upstream client creates every
// matcher as an exact one. Both together are why the protected-alert check can compare an
// alertname value for equality: a regex or negated matcher cannot be requested. If someone adds
// an operator to the request, this fails and the rule in planSilence must be revisited.
func TestSilenceMatcherHasOnlyNameAndValue(t *testing.T) {
	var fields []string
	for f := range reflect.TypeFor[model.SilenceMatcher]().Fields() {
		fields = append(fields, f.Tag.Get("json"))
	}
	if !slices.Equal(fields, []string{"name", "value"}) {
		t.Fatalf("model.SilenceMatcher has JSON fields %v: protectedAlerts compares alertname values for equality, which holds only while a request cannot carry a regex or a negation", fields)
	}
}

func TestCreateSilenceValidate(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	tests := []struct {
		name string
		mod  func(*silenceCreateInput)
		ok   bool
	}{
		{"valid", nil, true},
		{"500 char comment", func(i *silenceCreateInput) { i.Comment = long(500) }, true},
		{"20 matchers", func(i *silenceCreateInput) {
			i.Matchers = []model.SilenceMatcher{{Name: "alertname", Value: "A"}}
			for range 19 {
				i.Matchers = append(i.Matchers, model.SilenceMatcher{Name: "l", Value: "v"})
			}
		}, true},
		{"no alertname matcher", func(i *silenceCreateInput) { i.Matchers = []model.SilenceMatcher{{Name: "instance", Value: "x"}} }, false},
		{"no matchers", func(i *silenceCreateInput) { i.Matchers = nil }, false},
		{"21 matchers", func(i *silenceCreateInput) {
			i.Matchers = []model.SilenceMatcher{{Name: "alertname", Value: "A"}}
			for range 20 {
				i.Matchers = append(i.Matchers, model.SilenceMatcher{Name: "l", Value: "v"})
			}
		}, false},
		{"empty comment", func(i *silenceCreateInput) { i.Comment = "" }, false},
		{"whitespace comment", func(i *silenceCreateInput) { i.Comment = " \t\n " }, false},
		{"501 char comment", func(i *silenceCreateInput) { i.Comment = long(501) }, false},
		{"multibyte comment counts runes", func(i *silenceCreateInput) { i.Comment = strings.Repeat("é", 500) }, true},
		{"empty value", func(i *silenceCreateInput) { i.Matchers[1].Value = "" }, false},
		{"empty name", func(i *silenceCreateInput) { i.Matchers[1].Name = "" }, false},
		{"256 byte name, 1024 byte value", func(i *silenceCreateInput) { i.Matchers[1].Name, i.Matchers[1].Value = long(256), long(1024) }, true},
		{"257 byte name", func(i *silenceCreateInput) { i.Matchers[1].Name = long(257) }, false},
		{"1025 byte value", func(i *silenceCreateInput) { i.Matchers[1].Value = long(1025) }, false},
		{"long alertname value", func(i *silenceCreateInput) { i.Matchers[0].Value = long(1025) }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := createIn(tc.mod).Validate()
			if (err == nil) != tc.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestExpireValidate(t *testing.T) {
	for id, ok := range map[string]bool{
		"5b1c2d3e-aaaa-bbbb-cccc-0123456789ab": true,
		"":                                     false, "short": false, "../../x": false, "abc/def-1234": false, "zzzzzzzz": false,
	} {
		if err := (&silenceExpireInput{ID: id}).Validate(); (err == nil) != ok {
			t.Errorf("id %q: %v", id, err)
		}
	}
}

// The matrix of silences_test.go (legacy), at the plugin: every duration the instance offers
// maps to its length, anything else is a 400 before Alertmanager is touched.
func TestCreateSilenceDurationAndRequest(t *testing.T) {
	for _, tc := range []struct {
		dur  string
		want time.Duration
	}{{"1h", time.Hour}, {"4h", 4 * time.Hour}, {"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}} {
		t.Run(tc.dur, func(t *testing.T) {
			e := newEnv(t, envOpts{})
			out, err := e.act(t, "silence.create", createIn(func(i *silenceCreateInput) { i.Duration = tc.dur; i.Comment = "  hi  " }))
			if err != nil {
				t.Fatal(err)
			}
			created, _ := e.am.snapshot()
			if len(created) != 1 {
				t.Fatalf("created = %v", created)
			}
			c := created[0]
			start, _ := time.Parse(time.RFC3339, c["startsAt"].(string))
			end, _ := time.Parse(time.RFC3339, c["endsAt"].(string))
			if end.Sub(start) != tc.want {
				t.Errorf("length = %v, want %v", end.Sub(start), tc.want)
			}
			if c["createdBy"] != "dash:alice" || c["comment"] != "hi" {
				t.Errorf("createdBy/comment = %v / %q", c["createdBy"], c["comment"])
			}
			ms := c["matchers"].([]any)
			if len(ms) != 2 {
				t.Fatalf("matchers = %v", ms)
			}
			for _, m := range ms {
				m := m.(map[string]any)
				if m["isEqual"] != true || m["isRegex"] != false {
					t.Errorf("matcher %v is not an exact one", m)
				}
			}
			o, ok := out.(plugin.Outcome)
			if !ok {
				t.Fatalf("result = %#v, want a plugin.Outcome", out)
			}
			if r, ok := o.Body.(model.SilenceResponse); !ok || r.SilenceID != "abcdef12-3456" {
				t.Errorf("body = %#v", o.Body)
			}
			if o.Target == nil || *o.Target != (plugin.ActionTarget{Kind: "Silence", Name: "abcdef12-3456"}) {
				t.Errorf("target = %+v, want Silence/<id>", o.Target)
			}
		})
	}
}

func TestCreateSilenceRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    func(*pluginconf.Alertmanager)
		mod    func(*silenceCreateInput)
		status int
		code   string
	}{
		{"unknown duration", nil, func(i *silenceCreateInput) { i.Duration = "2h" }, 400, model.ErrCodeInvalidInput},
		{"empty duration", nil, func(i *silenceCreateInput) { i.Duration = "" }, 400, model.ErrCodeInvalidInput},
		{"30d", nil, func(i *silenceCreateInput) { i.Duration = "30d" }, 400, model.ErrCodeInvalidInput},
		{"a duration the instance does not offer", func(c *pluginconf.Alertmanager) { c.SilenceDurations = []string{"1h", "24h"} },
			func(i *silenceCreateInput) { i.Duration = "4h" }, 400, model.ErrCodeInvalidInput},
		{"a protected alert", nil, func(i *silenceCreateInput) { i.Matchers[0].Value = "Watchdog" }, 403, model.ErrCodeForbidden},
		{"a protected alert named by a later matcher", nil, func(i *silenceCreateInput) {
			i.Matchers = []model.SilenceMatcher{{Name: "alertname", Value: "Fine"}, {Name: "alertname", Value: "Watchdog"}}
		}, 403, model.ErrCodeForbidden},
		{"a protected name set in configuration", func(c *pluginconf.Alertmanager) { c.ProtectedAlerts = []string{"DiskFull"} },
			func(i *silenceCreateInput) { i.Matchers[0].Value = "DiskFull" }, 403, model.ErrCodeForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, envOpts{cfg: tc.cfg})
			_, err := e.act(t, "silence.create", createIn(tc.mod))
			ae := actionError(t, err)
			if ae.Status != tc.status || ae.Code != tc.code || ae.Message == "" {
				t.Errorf("got %+v, want %d %s", ae, tc.status, tc.code)
			}
			if created, _ := e.am.snapshot(); len(created) != 0 {
				t.Errorf("Alertmanager was called: %v", created)
			}
		})
	}
}

// With no protected alert configured, Watchdog can be silenced; a label value that merely
// contains a protected name is not that name.
func TestProtectedAlertsAreExactNames(t *testing.T) {
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Alertmanager) { c.ProtectedAlerts = nil }})
	if _, err := e.act(t, "silence.create", createIn(func(i *silenceCreateInput) { i.Matchers[0].Value = "Watchdog" })); err != nil {
		t.Errorf("no protected names configured: %v", err)
	}
	e2 := newEnv(t, envOpts{})
	if _, err := e2.act(t, "silence.create", createIn(func(i *silenceCreateInput) { i.Matchers[0].Value = "WatchdogExtra" })); err != nil {
		t.Errorf("WatchdogExtra is not Watchdog: %v", err)
	}
	// Only the alertname matcher names an alert: another label that equals a protected name is fine.
	if _, err := e2.act(t, "silence.create", createIn(func(i *silenceCreateInput) { i.Matchers[1].Value = "Watchdog" })); err != nil {
		t.Errorf("instance=Watchdog: %v", err)
	}
}

func TestCreateSilenceUpstreamFailureIsAnUpstreamError(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.am.setStatus(http.StatusServiceUnavailable)
	_, err := e.act(t, "silence.create", createIn(nil))
	if err == nil {
		t.Fatal("no error")
	}
	if _, refusal := err.(*plugin.ActionError); refusal {
		t.Errorf("an upstream failure is not a deliberate refusal: %v", err)
	}
}

func TestExpireSilence(t *testing.T) {
	am := newFakeAM(t)
	am.set(nil, []string{amSilenceDash, amSilenceForeign})
	e := newEnv(t, envOpts{am: am})

	// A dash silence is expired and the answer is empty (202).
	out, err := e.act(t, "silence.expire", &silenceExpireInput{ID: "5b1c2d3e-aaaa"})
	if err != nil || out != nil {
		t.Fatalf("expire = %#v, %v", out, err)
	}
	if _, expired := am.snapshot(); !slices.Equal(expired, []string{"5b1c2d3e-aaaa"}) {
		t.Errorf("expired = %v", expired)
	}

	for _, tc := range []struct {
		name, id string
		status   int
		code     string
	}{
		{"a silence dash did not create", "7c7c7c7c-bbbb", 403, model.ErrCodeForbidden},
		{"an unknown silence", "00000000-1111-2222", 404, model.ErrCodeNotFound},
	} {
		_, err := e.act(t, "silence.expire", &silenceExpireInput{ID: tc.id})
		ae := actionError(t, err)
		if ae.Status != tc.status || ae.Code != tc.code {
			t.Errorf("%s: %+v, want %d %s", tc.name, ae, tc.status, tc.code)
		}
	}
	if _, expired := am.snapshot(); len(expired) != 1 {
		t.Errorf("a refused expire reached Alertmanager: %v", expired)
	}
}

// Expire reads the list first, so Alertmanager down means it fails and nothing is expired blind.
func TestExpireNeedsAlertmanagerToRead(t *testing.T) {
	am := newFakeAM(t)
	am.set(nil, []string{amSilenceDash})
	e := newEnv(t, envOpts{am: am})
	am.setStatus(http.StatusBadGateway)
	_, err := e.act(t, "silence.expire", &silenceExpireInput{ID: "5b1c2d3e-aaaa"})
	if err == nil {
		t.Fatal("expired with the list unreadable")
	}
	if _, refusal := err.(*plugin.ActionError); refusal {
		t.Errorf("an upstream failure is not a deliberate refusal: %v", err)
	}
}

func TestActionSpecTargetsAndSummaries(t *testing.T) {
	specs := map[string]plugin.ActionSpec{}
	for _, s := range actionSpecs() {
		specs[s.Name] = s
	}
	cr := createIn(nil)
	k, ns, n := specs["silence.create"].Target(cr)
	if k != "Silence" || ns != "" || n != "HighTemp" {
		t.Errorf("create target = %s/%s/%s: the intent names the alert", k, ns, n)
	}
	k, ns, n = specs["silence.expire"].Target(&silenceExpireInput{ID: "abc12345"})
	if k != "Silence" || ns != "" || n != "abc12345" {
		t.Errorf("expire target = %s/%s/%s", k, ns, n)
	}
	if s := specs["silence.create"].Summary(cr); !strings.Contains(s, "HighTemp") || !strings.Contains(s, "4h") {
		t.Errorf("create summary = %q", s)
	}
	if s := specs["silence.expire"].Summary(&silenceExpireInput{ID: "abc12345"}); !strings.Contains(s, "abc12345") {
		t.Errorf("expire summary = %q", s)
	}
}

// The inputs describe themselves to the OpenAPI generator and the MCP tools: the embedded request
// is flattened into one object that rejects other fields.
func TestInputSchemas(t *testing.T) {
	doc, err := jsonschema.Document(&silenceCreateInput{})
	if err != nil {
		t.Fatal(err)
	}
	props, _ := doc["properties"].(jsonschema.Schema)
	for _, name := range []string{"matchers", "duration", "comment"} {
		if _, ok := props[name]; !ok {
			t.Errorf("silence.create schema has no %q: %v", name, doc)
		}
	}
	if doc["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v", doc["additionalProperties"])
	}
	doc, err = jsonschema.Document(&silenceExpireInput{})
	if err != nil {
		t.Fatal(err)
	}
	if req, _ := doc["required"].([]string); !slices.Equal(req, []string{"id"}) {
		t.Errorf("silence.expire required = %v", doc["required"])
	}
}
