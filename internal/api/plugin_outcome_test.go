package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/plugin"
)

const silenceCreate = "/api/v1/plugins/aa/actions/silence.create"

func TestActionOutcomeBodyAndStatus(t *testing.T) {
	created := &plugin.ActionTarget{Kind: "Silence", Name: silenceID}
	for _, tc := range []struct {
		name   string
		out    any
		status int
		body   string
	}{
		{"an Outcome with a body", plugin.Outcome{Body: map[string]string{"silenceId": silenceID}, Target: created}, 200, `{"silenceId":"sil-1234"}`},
		{"a pointer to an Outcome", &plugin.Outcome{Body: map[string]string{"silenceId": silenceID}, Target: created}, 200, `{"silenceId":"sil-1234"}`},
		{"an Outcome without a body is accepted", plugin.Outcome{Target: created}, 202, `{"status":"accepted"}`},
		{"a nil pointer to an Outcome is accepted", (*plugin.Outcome)(nil), 202, `{"status":"accepted"}`},
		{"a plain result is its own body", map[string]string{"plain": "yes"}, 200, `{"plain":"yes"}`},
		{"no result is accepted", nil, 202, `{"status":"accepted"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.act = func(string, any) (any, error) { return tc.out, nil }
			w := h.do(req{method: "POST", path: silenceCreate, body: silenceIn{Alertname: "HighTemp"}})
			if w.Code != tc.status || strings.TrimSpace(w.Body.String()) != tc.body {
				t.Errorf("answer = %d %s, want %d %s", w.Code, w.Body, tc.status, tc.body)
			}
		})
	}
}

func TestActionOutcomeRetargetsTheAuditRecordOnSuccessOnly(t *testing.T) {
	t.Run("success: intent keeps what was known, outcome has what exists", func(t *testing.T) {
		h := newHarness(t) // the default silence.create answers an Outcome with the created id
		if w := h.do(req{method: "POST", path: silenceCreate, body: silenceIn{Alertname: "HighTemp"}}); w.Code != http.StatusOK {
			t.Fatalf("answer = %d %s", w.Code, w.Body)
		}
		recs := h.audit(t)
		if len(recs) != 2 || recs[0].Kind != audit.KindIntent || recs[1].Kind != audit.KindOutcome || recs[0].ID != recs[1].ID {
			t.Fatalf("records = %+v, want an intent and its outcome", recs)
		}
		if want := (audit.Target{Kind: "Silence", Name: "HighTemp"}); recs[0].Target != want {
			t.Errorf("intent target = %+v, want %+v", recs[0].Target, want)
		}
		if want := (audit.Target{Kind: "Silence", Name: silenceID}); recs[1].Target != want || recs[1].Result != audit.ResultOK {
			t.Errorf("outcome = %+v, want target %+v and result ok", recs[1], want)
		}
		if recs[0].Plugin != "aa" || recs[1].Plugin != "aa" {
			t.Errorf("plugins = %q/%q", recs[0].Plugin, recs[1].Plugin)
		}
	})

	t.Run("failure: the target of ActionSpec.Target stays", func(t *testing.T) {
		h := newHarness(t)
		h.act = func(string, any) (any, error) {
			// Even an Outcome returned next to an error is ignored: nothing was created.
			return plugin.Outcome{Target: &plugin.ActionTarget{Kind: "Silence", Name: silenceID}}, errors.New("alertmanager is down")
		}
		if w := h.do(req{method: "POST", path: silenceCreate, body: silenceIn{Alertname: "HighTemp"}}); w.Code != http.StatusBadGateway {
			t.Fatalf("answer = %d %s, want 502", w.Code, w.Body)
		}
		recs := h.audit(t)
		if len(recs) != 2 {
			t.Fatalf("records = %+v", recs)
		}
		want := audit.Target{Kind: "Silence", Name: "HighTemp"}
		if recs[0].Target != want || recs[1].Target != want || recs[1].Result != audit.ResultError {
			t.Errorf("records = %+v, want both on %+v and the outcome an error", recs, want)
		}
	})
}
