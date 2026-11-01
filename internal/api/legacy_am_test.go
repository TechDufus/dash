package api

// Tests that exist only for the legacy silence routes (POST /api/silences, DELETE
// /api/silences/{id}). The alertmanager plugin replaces them in P3.3, which deletes this file
// together with the routes (and the AM dep: fakeAM is what newServer and serverWithFakes put
// there).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/upstream"
)

type fakeAM struct {
	spec      upstream.SilenceSpec
	created   int
	expiredID string
	err       error
}

func (f *fakeAM) CreateSilence(_ context.Context, s upstream.SilenceSpec) (string, error) {
	f.spec = s
	f.created++
	return "sil-1234", f.err
}

func (f *fakeAM) ExpireSilence(_ context.Context, id string) error {
	f.expiredID = id
	if f.err != nil {
		return f.err
	}
	if id == "bad" {
		return upstream.ErrInvalid
	}
	return nil
}

// slowAM delays CreateSilence to model an action that outlasts the body deadline.
type slowAM struct {
	*fakeAM
	delay time.Duration
}

func (a slowAM) CreateSilence(ctx context.Context, s upstream.SilenceSpec) (string, error) {
	select {
	case <-time.After(a.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return a.fakeAM.CreateSilence(ctx, s)
}

func silenceBody(mod func(*model.SilenceRequest)) model.SilenceRequest {
	r := model.SilenceRequest{
		Matchers: []model.SilenceMatcher{{Name: "alertname", Value: "HighTemp"}, {Name: "instance", Value: "pve-730"}},
		Duration: "4h",
		Comment:  "maintenance window",
	}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestCreateSilenceValidation(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*model.SilenceRequest)
		code int
		dur  time.Duration
	}{
		{"1h", func(r *model.SilenceRequest) { r.Duration = "1h" }, 200, time.Hour},
		{"4h", nil, 200, 4 * time.Hour},
		{"24h", func(r *model.SilenceRequest) { r.Duration = "24h" }, 200, 24 * time.Hour},
		{"7d", func(r *model.SilenceRequest) { r.Duration = "7d" }, 200, 168 * time.Hour},
		{"2h rejected", func(r *model.SilenceRequest) { r.Duration = "2h" }, 400, 0},
		{"empty duration", func(r *model.SilenceRequest) { r.Duration = "" }, 400, 0},
		{"30d rejected", func(r *model.SilenceRequest) { r.Duration = "30d" }, 400, 0},
		{"no alertname matcher", func(r *model.SilenceRequest) { r.Matchers = []model.SilenceMatcher{{Name: "instance", Value: "x"}} }, 400, 0},
		{"no matchers", func(r *model.SilenceRequest) { r.Matchers = nil }, 400, 0},
		{"empty comment", func(r *model.SilenceRequest) { r.Comment = "" }, 400, 0},
		{"whitespace comment", func(r *model.SilenceRequest) { r.Comment = " \t\n " }, 400, 0},
		{"500 char comment ok", func(r *model.SilenceRequest) { r.Comment = strings.Repeat("x", 500) }, 200, 4 * time.Hour},
		{"501 char comment", func(r *model.SilenceRequest) { r.Comment = strings.Repeat("x", 501) }, 400, 0},
		{"empty value", func(r *model.SilenceRequest) { r.Matchers[1].Value = "" }, 400, 0},
		{"empty name", func(r *model.SilenceRequest) { r.Matchers[1].Name = "" }, 400, 0},
		{"21 matchers", func(r *model.SilenceRequest) {
			r.Matchers = []model.SilenceMatcher{{Name: "alertname", Value: "A"}}
			for range 20 {
				r.Matchers = append(r.Matchers, model.SilenceMatcher{Name: "l", Value: "v"})
			}
		}, 400, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			am := &fakeAM{}
			s := newServer(func(d *Deps) { d.AM = am })
			w := req{method: "POST", path: "/api/silences", body: silenceBody(tc.mod)}.do(s.Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			if tc.code != 200 {
				if am.created != 0 {
					t.Error("Alertmanager must not be called")
				}
				if decodeErr(t, w) == "" {
					t.Error("empty error")
				}
				return
			}
			if got := am.spec.EndsAt.Sub(am.spec.StartsAt); got != tc.dur {
				t.Errorf("duration = %v, want %v", got, tc.dur)
			}
			if !am.spec.StartsAt.Equal(fixedNow) {
				t.Errorf("startsAt = %v", am.spec.StartsAt)
			}
			if am.spec.CreatedBy != "dash:alice" {
				t.Errorf("createdBy = %q", am.spec.CreatedBy)
			}
			var resp model.SilenceResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.SilenceID != "sil-1234" {
				t.Errorf("response = %s", w.Body)
			}
		})
	}
}

func TestCreateSilenceDetails(t *testing.T) {
	t.Run("comment is trimmed and matchers forwarded", func(t *testing.T) {
		am := &fakeAM{}
		s := newServer(func(d *Deps) { d.AM = am })
		req{method: "POST", path: "/api/silences", body: silenceBody(func(r *model.SilenceRequest) { r.Comment = "  hi  " })}.do(s.Handler())
		if am.spec.Comment != "hi" || len(am.spec.Matchers) != 2 || am.spec.Matchers[0].Name != "alertname" {
			t.Errorf("spec = %+v", am.spec)
		}
	})
	t.Run("alertmanager failure is 502 and audited", func(t *testing.T) {
		buf := captureAudit(t)
		am := &fakeAM{err: context.DeadlineExceeded}
		s := newServer(func(d *Deps) { d.AM = am })
		w := req{method: "POST", path: "/api/silences", body: silenceBody(nil)}.do(s.Handler())
		if w.Code != 502 {
			t.Fatalf("code = %d", w.Code)
		}
		e := auditEntries(t, buf)
		if len(e) != 1 || e[0]["action"] != "silence.create" || e[0]["kind"] != "Silence" || e[0]["result"] != "error" || e[0]["name"] != "HighTemp" {
			t.Errorf("audit = %v", e)
		}
	})
	t.Run("success audit carries the silence id", func(t *testing.T) {
		buf := captureAudit(t)
		s := newServer(nil)
		req{method: "POST", path: "/api/silences", body: silenceBody(nil)}.do(s.Handler())
		e := auditEntries(t, buf)
		if len(e) != 1 || e[0]["result"] != "ok" || e[0]["name"] != "sil-1234" || e[0]["user"] != "alice" {
			t.Errorf("audit = %v", e)
		}
	})
}

func TestExpireSilence(t *testing.T) {
	tests := []struct {
		name string
		id   string
		err  error
		code int
	}{
		{"ok", "0123abcd-4567", nil, 200},
		{"invalid id", "bad", nil, 400},
		{"alertmanager down", "0123abcd-4567", context.DeadlineExceeded, 502},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureAudit(t)
			am := &fakeAM{err: tc.err}
			s := newServer(func(d *Deps) { d.AM = am })
			w := req{method: "DELETE", path: "/api/silences/" + tc.id}.do(s.Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			if am.expiredID != tc.id {
				t.Errorf("expired %q", am.expiredID)
			}
			if tc.code == 200 && w.Body.String() != "{\"status\":\"expired\"}\n" {
				t.Errorf("body = %q", w.Body)
			}
			e := auditEntries(t, buf)
			if len(e) != 1 || e[0]["action"] != "silence.expire" || e[0]["name"] != tc.id {
				t.Errorf("audit = %v", e)
			}
		})
	}
}

// The legacy silence actions count under the plugin name and action they will have in P3.3, with
// the outcome of what happened.
func TestLegacySilencesAreCountedWithMetricsOn(t *testing.T) {
	m := obs.New("test", RoutePatterns(), nil)
	am := &fakeAM{}
	s := newServer(func(d *Deps) { d.AM, d.Audit, d.Metrics = am, openAudit(t, "", nil), m })
	h := s.Handler()
	for _, c := range []struct {
		r   req
		err bool
	}{
		{req{method: "POST", path: "/api/silences", body: silenceBody(nil)}, false},
		{req{method: "DELETE", path: "/api/silences/abc"}, true},
		{req{method: "DELETE", path: "/api/silences/abc"}, false},
	} {
		am.err = nil
		if c.err {
			am.err = errors.New("boom")
		}
		c.r.do(h)
	}
	ss := scrapeMetrics(t, s)
	for _, want := range []struct {
		action, outcome string
		n               float64
	}{
		{"silence.create", obs.ActionOK, 1},
		{"silence.expire", obs.ActionError, 1},
		{"silence.expire", obs.ActionOK, 1},
	} {
		got, ok := seriesValue(t, ss, "dash_actions_total", map[string]string{"plugin": "alertmanager", "action": want.action, "outcome": want.outcome})
		if !ok || got != want.n {
			t.Errorf("dash_actions_total %s/%s = %v (present %v), want %v", want.action, want.outcome, got, ok, want.n)
		}
	}
}
