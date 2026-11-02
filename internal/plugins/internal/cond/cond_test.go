package cond

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(conds ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"conditions": conds}}}
}

func condition(typ, status, msg, at string) map[string]any {
	return map[string]any{"type": typ, "status": status, "message": msg, "lastTransitionTime": at}
}

func TestOfAndTrue(t *testing.T) {
	u := obj(
		"not a map",
		condition("Healthy", "False", "boom", ""),
		condition("Ready", "True", "ok", ""),
	)
	if s, m := Of(u, "Ready"); s != "True" || m != "ok" {
		t.Errorf("Of(Ready) = %q, %q", s, m)
	}
	if s, m := Of(u, "Healthy"); s != "False" || m != "boom" {
		t.Errorf("Of(Healthy) = %q, %q", s, m)
	}
	if s, m := Of(u, "Missing"); s != "" || m != "" {
		t.Errorf("Of(Missing) = %q, %q", s, m)
	}
	if !True(u, "Ready") || True(u, "Healthy") || True(u, "Missing") {
		t.Errorf("True: Ready=%v Healthy=%v Missing=%v", True(u, "Ready"), True(u, "Healthy"), True(u, "Missing"))
	}
	if s, m := Of(&unstructured.Unstructured{Object: map[string]any{}}, "Ready"); s != "" || m != "" {
		t.Errorf("an object without status: %q, %q", s, m)
	}
}

func TestTime(t *testing.T) {
	u := obj(
		condition("Ready", "True", "", "2026-10-30T14:00:00+02:00"),
		condition("Bad", "True", "", "yesterday"),
		condition("Unset", "True", "", ""),
	)
	got := Time(u, "Ready")
	if got == nil || !got.Equal(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)) || got.Location() != time.UTC {
		t.Errorf("Time(Ready) = %v, want 12:00 UTC", got)
	}
	for _, typ := range []string{"Bad", "Unset", "Missing"} {
		if got := Time(u, typ); got != nil {
			t.Errorf("Time(%s) = %v, want nil", typ, got)
		}
	}
}
