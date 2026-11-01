package gatus

import (
	"errors"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/upstream"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// endpoint builds a check whose results are oks, one minute apart and ending at end; Healthy is
// the last one, as Gatus' client sets it.
func endpoint(group, name, host string, end time.Time, oks ...bool) model.Endpoint {
	e := model.Endpoint{Group: group, Name: name, Key: group + "_" + name, URL: host, Results: []model.CheckResult{}}
	for i, ok := range oks {
		e.Results = append(e.Results, model.CheckResult{Success: ok, Timestamp: end.Add(-time.Duration(len(oks)-1-i) * time.Minute)})
	}
	if n := len(oks); n > 0 {
		e.Healthy = oks[n-1]
	}
	return e
}

var errDown = errors.New("dial tcp 192.0.2.1:8080: connect: connection refused")

func TestRevisionIsZeroUntilTheFirstPollFinishes(t *testing.T) {
	s := newService(nil)
	if s.Revision() != 0 {
		t.Errorf("Revision before any poll = %d", s.Revision())
	}
	if st := s.status(); st.State != "starting" {
		t.Errorf("state before any poll = %s", st.State)
	}
	if src := s.SourceStatuses(); len(src) != 1 || src[0].Name != "gatus" || src[0].OK || src[0].Error != notPolled {
		t.Errorf("sources before any poll = %+v", src)
	}
}

// A failed first poll counts as finished (M1 semantic 1): otherwise a Gatus that is down at
// start would hold the consumers' gate closed for good.
func TestAFailedFirstPollBumpsRevision(t *testing.T) {
	s := newService(nil)
	data, srcs := s.record(nil, errDown, t0)
	if s.Revision() == 0 {
		t.Fatal("Revision is still 0 after a failed first poll")
	}
	if len(srcs) != 1 || srcs[0].OK || srcs[0].Error != upstream.CategoryUnreachable || !srcs[0].CheckedAt.Equal(t0) {
		t.Errorf("sources = %+v", srcs)
	}
	if data.Endpoints == nil || len(data.Endpoints) != 0 {
		t.Errorf("endpoints = %#v, want an empty non-nil list", data.Endpoints)
	}
	if st := s.status(); st.State != "error" || st.Message != upstream.CategoryUnreachable {
		t.Errorf("status = %+v", st)
	}
	if got := s.SourceStatuses(); len(got) != 1 || got[0].OK || got[0].Error != upstream.CategoryUnreachable {
		t.Errorf("SourceStatuses = %+v", got)
	}
}

func TestRevisionChangesExactlyWhenAConsumerCouldSeeADifference(t *testing.T) {
	s := newService([]string{"infrastructure"})
	grafana := func(ok bool, end time.Time) model.Endpoint {
		return endpoint("apps", "Grafana", "grafana.example.com", end, true, ok)
	}
	record := func(err error, eps ...model.Endpoint) uint64 {
		s.record(eps, err, t0)
		return s.Revision()
	}

	r1 := record(nil, grafana(true, t0))
	if r1 == 0 {
		t.Fatal("first poll: Revision 0")
	}
	if r := record(nil, grafana(true, t0.Add(time.Minute))); r != r1 {
		t.Errorf("new results with the same health bumped Revision: %d -> %d", r1, r)
	}
	r2 := record(nil, grafana(false, t0))
	if r2 <= r1 {
		t.Errorf("a health flip did not raise Revision: %d -> %d", r1, r2)
	}
	r3 := record(nil, grafana(false, t0), endpoint("apps", "Plex", "plex.example.com", t0, true))
	if r3 <= r2 {
		t.Errorf("a new endpoint did not raise Revision: %d -> %d", r2, r3)
	}
	renamed := endpoint("apps", "Plex", "plex.example.net", t0, true)
	r4 := record(nil, grafana(false, t0), renamed)
	if r4 <= r3 {
		t.Errorf("a changed host did not raise Revision: %d -> %d", r3, r4)
	}
	// The source going down changes what the consumers append to their envelopes, though the
	// endpoints stay as they were.
	r5 := record(errDown)
	if r5 <= r4 {
		t.Errorf("the source going down did not raise Revision: %d -> %d", r4, r5)
	}
	if r := record(errDown); r != r5 {
		t.Errorf("the same failure again bumped Revision: %d -> %d", r5, r)
	}
	if got, known := s.ByName("plex"); !known || !got {
		t.Errorf("a failed poll must keep the last endpoints: ByName(plex) = %v, %v", got, known)
	}
	if r := record(&upstream.StatusError{Code: 500}); r <= r5 {
		t.Errorf("a different failure category did not raise Revision: %d -> %d", r5, r)
	}
	r6 := record(nil, grafana(false, t0), renamed)
	if r6 <= r5 {
		t.Errorf("the source recovering did not raise Revision: %d -> %d", r5, r6)
	}
	if r := record(nil, grafana(false, t0), renamed); r != r6 {
		t.Errorf("an unchanged poll bumped Revision: %d -> %d", r6, r)
	}
}

// A service built by a reload never reports a revision at or below its predecessor's (M1
// semantic 2).
func TestRevisionIsMonotonicAcrossServices(t *testing.T) {
	var last uint64
	for i := range 20 {
		s := newService(nil)
		if s.Revision() != 0 {
			t.Fatalf("service %d starts at %d", i, s.Revision())
		}
		s.record([]model.Endpoint{endpoint("apps", "A", "a.example.com", t0, true)}, nil, t0)
		first := s.Revision()
		if first <= last {
			t.Fatalf("service %d: first revision %d is not above the predecessor's %d", i, first, last)
		}
		s.record([]model.Endpoint{endpoint("apps", "A", "a.example.com", t0, false)}, nil, t0)
		last = s.Revision()
		if last <= first {
			t.Fatalf("service %d: %d after a change, %d before", i, last, first)
		}
	}
}

func TestByHostAndByName(t *testing.T) {
	s := newService([]string{"infrastructure"})
	s.record([]model.Endpoint{
		endpoint("apps", "Grafana", "Grafana.Example.com", t0, true),
		endpoint("apps", "Plex", "plex.example.com", t0, true, false),
		endpoint("infrastructure", "Switch", "192.0.2.10", t0, false),
		endpoint("apps", "NoResults", "", t0),
		// Two checks of one host and of one name: the last one wins.
		endpoint("apps", "Shared", "shared.example.com", t0, true),
		endpoint("edge", "shared", "shared.example.com", t0, false),
	}, nil, t0)

	for _, tc := range []struct {
		host  string
		state model.Health
		known bool
	}{
		{"grafana.example.com", model.HealthOK, true},
		{"GRAFANA.example.com", model.HealthOK, true},
		{"plex.example.com", model.HealthWarn, true},
		{"192.0.2.10", model.HealthCrit, true},
		{"shared.example.com", model.HealthWarn, true},
		{"", "", false},
		{"unknown.example.com", "", false},
	} {
		state, known := s.ByHost(tc.host)
		if state != tc.state || known != tc.known {
			t.Errorf("ByHost(%q) = %q, %v; want %q, %v", tc.host, state, known, tc.state, tc.known)
		}
	}
	for _, tc := range []struct {
		name      string
		up, known bool
	}{
		{"grafana", true, true},
		{"GRAFANA", true, true},
		{"Plex", false, true},
		{"noresults", false, true}, // as it always has: a check without results is down
		{"shared", false, true},    // the last of the two wins
		{"nothing", false, false},
	} {
		up, known := s.ByName(tc.name)
		if up != tc.up || known != tc.known {
			t.Errorf("ByName(%q) = %v, %v; want %v, %v", tc.name, up, known, tc.up, tc.known)
		}
	}
}

func TestRecordGivesEveryEndpointARef(t *testing.T) {
	s := newService(nil)
	keyless := endpoint("apps", "Odd", "odd.example.com", t0, true)
	keyless.Key = ""
	data, _ := s.record([]model.Endpoint{endpoint("apps", "Grafana", "grafana.example.com", t0, true), keyless}, nil, t0)
	got := map[string]string{}
	for _, e := range data.Endpoints {
		got[e.Name] = e.Ref
	}
	want := map[string]string{"Grafana": "gatus:Endpoint/apps_Grafana", "Odd": "gatus:Endpoint/apps_Odd"}
	for name, ref := range want {
		if got[name] != ref {
			t.Errorf("%s ref = %q, want %q", name, got[name], ref)
		}
	}
}

func TestAttentionSinceFirstFailureOfTheStreak(t *testing.T) {
	s := newService([]string{"infrastructure"})
	s.record([]model.Endpoint{
		// ok, fail, fail: the streak began two minutes before the last result.
		endpoint("infrastructure", "UniFi", "unifi.example.com", t0, true, false, false),
		endpoint("apps", "Grafana", "grafana.example.com", t0, true),
		endpoint("apps", "Plex", "plex.example.com", t0, false, true, false),
		endpoint("apps", "NoResults", "", t0),
	}, nil, t0)
	items := attention(s, t0)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	unifi, plex := items[0], items[1]
	if unifi.ID != "gatus:infrastructure_UniFi" || unifi.Severity != model.SeverityCrit ||
		!unifi.Since.Equal(t0.Add(-time.Minute)) || unifi.Title != "UniFi check failing" || unifi.Detail != "infrastructure" ||
		unifi.Route != "/status" || unifi.Source != "gatus" || unifi.Plugin != "gatus" || unifi.Ref != "gatus:Endpoint/infrastructure_UniFi" {
		t.Errorf("unifi item: %+v", unifi)
	}
	if plex.ID != "gatus:apps_Plex" || plex.Severity != model.SeverityWarn || !plex.Since.Equal(t0) {
		t.Errorf("plex item: %+v", plex)
	}
	for _, it := range items {
		if it.Links == nil {
			t.Errorf("%s: nil Links", it.ID)
		}
	}
}

func TestAttentionIsEmptyNotNilWhenEverythingPasses(t *testing.T) {
	s := newService(nil)
	s.record([]model.Endpoint{endpoint("apps", "Grafana", "grafana.example.com", t0, true)}, nil, t0)
	if items := attention(s, t0); items == nil || len(items) != 0 {
		t.Errorf("items = %#v", items)
	}
}

func TestEmptyCriticalGroupsMakeEveryFailureAWarning(t *testing.T) {
	failing := endpoint("infrastructure", "UniFi", "unifi.example.com", t0, false)
	for name, tc := range map[string]struct {
		critical []string
		want     model.Severity
	}{
		"none":               {nil, model.SeverityWarn},
		"exact":              {[]string{"infrastructure"}, model.SeverityCrit},
		"different case":     {[]string{"Infrastructure"}, model.SeverityCrit},
		"other group":        {[]string{"edge"}, model.SeverityWarn},
		"a list that has it": {[]string{"edge", "infrastructure"}, model.SeverityCrit},
	} {
		s := newService(tc.critical)
		s.record([]model.Endpoint{failing}, nil, t0)
		items := attention(s, t0)
		if len(items) != 1 || items[0].Severity != tc.want {
			t.Errorf("%s: %+v, want one %s item", name, items, tc.want)
		}
		if state, _ := s.ByHost("unifi.example.com"); (state == model.HealthCrit) != (tc.want == model.SeverityCrit) {
			t.Errorf("%s: ByHost state %s disagrees with the attention severity", name, state)
		}
	}
}
