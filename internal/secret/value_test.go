package secret

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestValuePresentAndPending(t *testing.T) {
	tests := []struct {
		name                  string
		v                     Value
		set, pending, present bool
	}{
		{"zero", Value{}, false, false, false},
		{"literal", New("x", ""), true, false, true},
		{"env ref read", New("x", "env:A"), true, false, true},
		{"ref not read", New("", "k8s:a/b#c"), false, true, true},
		{"empty literal", New("", ""), false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.v.IsSet() != tt.set || tt.v.Pending() != tt.pending || tt.v.Present() != tt.present {
				t.Errorf("IsSet=%v Pending=%v Present=%v, want %v %v %v", tt.v.IsSet(), tt.v.Pending(), tt.v.Present(), tt.set, tt.pending, tt.present)
			}
		})
	}
	// A pending handle becomes set for every copy through Update.
	p := New("", "k8s:a/b#c")
	cp := p
	p.Update("filled")
	if cp.Pending() || !cp.IsSet() {
		t.Error("a copy must see the fill")
	}
}

func TestReasonIsMetadataOnly(t *testing.T) {
	v := New("", "k8s:a/b#c")
	if v.Reason() != "" || (Value{}).Reason() != "" {
		t.Error("no reason yet")
	}
	if !v.setReason(ReasonForbidden) || v.setReason(ReasonForbidden) || (Value{}).setReason("x") {
		t.Error("setReason reports a change once and never on the zero Value")
	}
	if v.Reason() != ReasonForbidden {
		t.Errorf("Reason = %q", v.Reason())
	}
	b, _ := json.Marshal(v)
	if string(b) != `{"set":false,"ref":"k8s:a/b#c"}` {
		t.Errorf("JSON = %s: a pending handle shows the same metadata as any reference", b)
	}
	v.Update(canary)
	for _, out := range []string{fmt.Sprintf("%v %+v %#v %s", v, v, v, v)} {
		if strings.Contains(out, canary) {
			t.Errorf("a filled handle prints %q", out)
		}
	}
}
