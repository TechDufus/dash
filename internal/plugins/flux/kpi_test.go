package flux

import (
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// kpiMap is the KPIs by key with their value and health.
func kpiMap(t *testing.T, ks []model.KPI) map[string]model.KPI {
	t.Helper()
	out := map[string]model.KPI{}
	for _, k := range ks {
		if _, dup := out[k.Key]; dup {
			t.Errorf("KPI %s twice", k.Key)
		}
		if k.Plugin != "flux" || k.Label == "" {
			t.Errorf("KPI = %+v", k)
		}
		out[k.Key] = k
	}
	return out
}

func TestFluxKPIsCountKustomizationsAndReleasesOnly(t *testing.T) {
	d := payload(
		fo(git, "gitops", "root"), fo(oci, "gitops", "charts", notRdy("x")), // sources are not counted
		fo(kst, "gitops", "a"), fo(kst, "gitops", "b", notRdy("x")), fo(kst, "gitops", "c", susp("")),
		fo(hr, "m", "x"), fo(hr, "m", "y", susp("")),
	)
	got := kpiMap(t, kpisOf(built{data: d}, cfgOf(true)))
	for key, want := range map[string]float64{"ready": 4, "total": 5, "suspended": 2} {
		if got[key].Value != want {
			t.Errorf("%s = %v, want %v", key, got[key].Value, want)
		}
	}
	if got["ready"].Health != model.HealthWarn {
		t.Errorf("ready health = %s, want warn while one is not ready", got["ready"].Health)
	}
	if got["total"].Health != model.HealthOK || got["suspended"].Health != model.HealthOK {
		t.Errorf("health = %+v", got)
	}
	if _, ok := got["verified"]; ok {
		t.Error("verified is present with nothing pinned or signed")
	}
	if len(got) != 3 {
		t.Errorf("KPIs = %v", got)
	}
}

func TestFluxKPIsAreOKWhenEverythingIsReady(t *testing.T) {
	got := kpiMap(t, kpisOf(built{data: payload(fo(kst, "g", "a"), fo(hr, "m", "x"))}, cfgOf(true)))
	if got["ready"].Value != 2 || got["total"].Value != 2 || got["ready"].Health != model.HealthOK {
		t.Errorf("KPIs = %+v", got)
	}
	// An empty cluster still reports its zero counts: the tile reads them.
	zero := kpiMap(t, kpisOf(built{data: payload()}, cfgOf(true)))
	if len(zero) != 3 || zero["total"].Value != 0 || zero["ready"].Health != model.HealthOK {
		t.Errorf("empty KPIs = %+v", zero)
	}
}

// verified is present only when something is pinned or signed, is 1 when all of those are
// verified (a missing pin is not), 0 otherwise, and critical at 0.
func TestFluxVerifiedKPI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     pluginconf.Flux
		objs    []model.FluxObj
		unknown map[model.FluxKind]bool
		want    float64
		present bool
	}{
		{"nothing pinned or signed", cfgOf(true), []model.FluxObj{fo(git, "g", "r")}, nil, 0, false},
		{"a signed source that is verified", cfgOf(true), []model.FluxObj{fo(git, "g", "r", sgn(), vrf(true))}, nil, 1, true},
		{"a signed source that is not verified", cfgOf(true), []model.FluxObj{fo(git, "g", "r", sgn())}, nil, 0, true},
		{"one of two signed sources is not verified", cfgOf(true),
			[]model.FluxObj{fo(git, "g", "r", sgn(), vrf(true)), fo(oci, "g", "c", sgn(), vrf(false))}, nil, 0, true},
		{"a signed source still counts with the check off", cfgOf(false), []model.FluxObj{fo(git, "g", "r", sgn())}, nil, 0, true},
		{"a pin that is verified", cfgOf(false, pin(git, "g", "r")), []model.FluxObj{fo(git, "g", "r", sgn(), vrf(true))}, nil, 1, true},
		{"a pin without spec.verify", cfgOf(true, pin(git, "g", "r")), []model.FluxObj{fo(git, "g", "r", vrf(true))}, nil, 0, true},
		{"a pin that is missing", cfgOf(true, pin(git, "g", "r")), nil, nil, 0, true},
		{"a pin that is missing while its list is refused", cfgOf(true, pin(git, "g", "r")), nil, map[model.FluxKind]bool{git: true}, 0, false},
		{"a verified pin and a missing one", cfgOf(true, pin(git, "g", "r"), pin(oci, "g", "gone")),
			[]model.FluxObj{fo(git, "g", "r", sgn(), vrf(true))}, nil, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, ok := kpi(kpisOf(built{data: payload(tc.objs...), unknown: tc.unknown}, tc.cfg), "verified")
			if ok != tc.present {
				t.Fatalf("present = %v, want %v (%+v)", ok, tc.present, k)
			}
			if !ok {
				return
			}
			wantHealth := model.HealthOK
			if tc.want == 0 {
				wantHealth = model.HealthCrit
			}
			if k.Value != tc.want || k.Health != wantHealth {
				t.Errorf("verified = %v (%s), want %v (%s)", k.Value, k.Health, tc.want, wantHealth)
			}
		})
	}
}
