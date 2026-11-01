package gatus

import (
	"reflect"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

func TestEndpointHealth(t *testing.T) {
	tests := []struct {
		name     string
		group    string
		healthy  bool
		critical []string
		want     model.Health
	}{
		{"healthy is ok", "infrastructure", true, []string{"infrastructure"}, model.HealthOK},
		{"healthy in another group", "apps", true, []string{"infrastructure"}, model.HealthOK},
		{"listed group is crit", "infrastructure", false, []string{"infrastructure"}, model.HealthCrit},
		{"list entry in another case", "infrastructure", false, []string{"Infrastructure"}, model.HealthCrit},
		{"group in another case", "Infrastructure", false, []string{"infrastructure"}, model.HealthCrit},
		{"unlisted group warns", "apps", false, []string{"infrastructure"}, model.HealthWarn},
		{"any of several", "edge", false, []string{"infrastructure", "EDGE"}, model.HealthCrit},
		{"no critical groups warns", "infrastructure", false, nil, model.HealthWarn},
		{"empty list warns", "infrastructure", false, []string{}, model.HealthWarn},
		{"ungrouped warns", "", false, []string{"infrastructure"}, model.HealthWarn},
	}
	for _, tc := range tests {
		if got := EndpointHealth(tc.group, tc.healthy, tc.critical); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"grafana.example.com":             "grafana.example.com",
		"Grafana.Example.COM":             "grafana.example.com",
		"https://Grafana.example.com/x":   "grafana.example.com",
		"http://grafana.example.com:8080": "grafana.example.com",
		"192.0.2.10":                      "192.0.2.10",
		"192.0.2.10:8080":                 "192.0.2.10:8080", // not a URL: the whole string stands in
		"":                                "",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortedOrdersByGroupThenNameAndNeverReturnsNilResults(t *testing.T) {
	in := []model.Endpoint{
		{Group: "b", Name: "z"}, {Group: "a", Name: "y", Results: []model.CheckResult{{Success: true}}},
		{Group: "b", Name: "c"}, {Group: "a", Name: "x"},
	}
	got := sorted(in)
	var names []string
	for _, e := range got {
		names = append(names, e.Group+"/"+e.Name)
		if e.Results == nil {
			t.Errorf("%s/%s: nil Results", e.Group, e.Name)
		}
	}
	if want := []string{"a/x", "a/y", "b/c", "b/z"}; !reflect.DeepEqual(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
	if in[0].Group != "b" || in[0].Name != "z" || in[0].Results != nil {
		t.Errorf("the input was changed: %+v", in[0])
	}
	if got := sorted(nil); got == nil || len(got) != 0 {
		t.Errorf("sorted(nil) = %#v, want an empty non-nil slice", got)
	}
}
