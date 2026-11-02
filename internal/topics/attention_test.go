package topics

import (
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

var testNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func input() AttentionInput {
	return AttentionInput{Now: testNow}
}

// severityOf returns the severity of the item with id, or "" when there is none.
func severityOf(items []model.AttentionItem, id string) model.Severity {
	for _, it := range items {
		if it.ID == id {
			return it.Severity
		}
	}
	return ""
}

func TestFluxRules(t *testing.T) {
	in := input()
	in.Flux = model.FluxData{
		Kustomizations: []model.FluxObj{
			{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "bad", Ready: false, Message: "boom\nsecond line"},
			{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "paused", Ready: false, Suspended: true, SuspendReason: "x"},
			{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "good", Ready: true},
		},
		Sources: []model.FluxObj{
			{Kind: model.FluxKindGitRepository, Namespace: "flux-system", Name: "flux-system", Ready: true, Verified: new(true)},
		},
	}
	items := Attention(in, nil)
	if got := severityOf(items, "flux:Kustomization/flux-system/bad"); got != model.SeverityWarn {
		t.Errorf("not-ready kustomization = %q, want warn", got)
	}
	if len(items) != 1 {
		t.Fatalf("suspended, ready and verified objects must not be flagged, got %+v", items)
	}
	if items[0].Detail != "boom" {
		t.Errorf("detail must be the first message line, got %q", items[0].Detail)
	}

	tests := []struct {
		name     string
		verified *bool
		want     model.Severity
	}{
		{"verified", new(true), ""},
		{"not verified", new(false), model.SeverityCrit},
		{"condition absent", nil, model.SeverityCrit},
	}
	for _, tc := range tests {
		in := input()
		in.Flux.Sources = []model.FluxObj{{Kind: model.FluxKindGitRepository, Namespace: "flux-system", Name: "flux-system", Ready: true, Verified: tc.verified}}
		if got := severityOf(Attention(in, nil), "flux:source-unverified"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// Only the flux-system source is fenced by the crit rule.
	in = input()
	in.Flux.Sources = []model.FluxObj{{Kind: model.FluxKindGitRepository, Namespace: "x", Name: "other", Ready: true, Verified: new(false)}}
	if items := Attention(in, nil); len(items) != 0 {
		t.Errorf("other sources are not fenced: %+v", items)
	}
}

func TestOverallStatusAndOrdering(t *testing.T) {
	if got := OverallStatus(nil); got != model.HealthOK {
		t.Errorf("empty = %s", got)
	}
	in := input()
	old, recent := testNow.Add(-2*time.Hour), testNow.Add(-time.Minute)
	in.Flux.Kustomizations = []model.FluxObj{
		{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "old", LastTransition: &old},
		{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "new", LastTransition: &recent},
	}
	items := Attention(in, nil)
	if OverallStatus(items) != model.HealthWarn {
		t.Errorf("only warnings must give warn, got %s", OverallStatus(items))
	}
	if items[0].ID != "flux:Kustomization/flux-system/new" {
		t.Errorf("newest first within a severity, got %s", items[0].ID)
	}
	in.Flux.Sources = []model.FluxObj{{Kind: model.FluxKindGitRepository, Namespace: "flux-system", Name: "flux-system", Ready: true, Verified: new(false), LastTransition: &old}}
	items = Attention(in, nil)
	if OverallStatus(items) != model.HealthCrit {
		t.Errorf("a critical item must make the status crit, got %s", OverallStatus(items))
	}
	if items[0].ID != "flux:source-unverified" {
		t.Errorf("crit sorts first even when older, got %s", items[0].ID)
	}
	if items[0].Links == nil {
		t.Error("links must never be nil")
	}
}

func TestTrackerKeepsFirstSeen(t *testing.T) {
	tr := NewTracker()
	in := input()
	// No time of its own (no Ready transition): the tracker gives it one.
	in.Flux.Kustomizations = []model.FluxObj{{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "k"}}
	first := Attention(in, tr)[0].Since
	if !first.Equal(testNow) {
		t.Fatalf("first sighting = %v, want now", first)
	}
	in.Now = testNow.Add(10 * time.Minute)
	if got := Attention(in, tr)[0].Since; !got.Equal(first) {
		t.Errorf("since moved to %v, must stay %v", got, first)
	}
	in.Flux.Kustomizations[0].Ready = true
	Attention(in, tr)
	in.Flux.Kustomizations[0].Ready = false
	in.Now = testNow.Add(20 * time.Minute)
	if got := Attention(in, tr)[0].Since; !got.Equal(in.Now) {
		t.Errorf("a recovered then failing item starts over, got %v", got)
	}
}
