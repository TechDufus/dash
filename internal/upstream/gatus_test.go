package upstream

import (
	"io"
	"net/http"
	"testing"
	"time"
)

func TestGatusStatuses(t *testing.T) {
	var rawQuery string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		io.WriteString(w, `[
		 {"name":"Grafana","group":"apps","key":"apps_grafana","results":[
		   {"status":200,"hostname":"grafana.example.com","duration":10172175,"conditionResults":[{"condition":"[STATUS] == 200","success":true}],"success":true,"timestamp":"2026-10-30T21:10:15.668367726Z"},
		   {"status":500,"hostname":"grafana.example.com","duration":2500000,"success":false,"timestamp":"2026-10-30T21:40:15.668367726Z"}]},
		 {"name":"New","group":"apps","key":"apps_new","results":[]}]`)
	})
	got, err := NewGatus(s.URL, nil, 30).Statuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rawQuery != "page=1&pageSize=30" {
		t.Fatalf("query = %q", rawQuery)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	g := got[0]
	if g.Group != "apps" || g.Name != "Grafana" || g.Key != "apps_grafana" || g.URL != "grafana.example.com" ||
		g.Healthy || len(g.Results) != 2 || !g.Results[0].Success || g.Results[0].DurationMs != 10.172175 ||
		!g.Results[0].Timestamp.Before(g.Results[1].Timestamp) {
		t.Fatalf("endpoint: %+v", g)
	}
	if got[1].Results == nil || got[1].Healthy || got[1].URL != "" {
		t.Fatalf("empty endpoint: %+v", got[1])
	}
}

func TestGatusPageSizeReachesTheQuery(t *testing.T) {
	for _, tc := range []struct {
		pageSize int
		want     string
	}{{5, "page=1&pageSize=5"}, {100, "page=1&pageSize=100"}, {0, "page=1&pageSize=1"}, {-3, "page=1&pageSize=1"}} {
		var rawQuery string
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			rawQuery = r.URL.RawQuery
			io.WriteString(w, `[]`)
		})
		if _, err := NewGatus(s.URL, nil, tc.pageSize).Statuses(ctx); err != nil {
			t.Fatal(err)
		}
		if rawQuery != tc.want {
			t.Errorf("pageSize %d: query = %q, want %q", tc.pageSize, rawQuery, tc.want)
		}
	}
}

func TestGatusOptionsReachTheClient(t *testing.T) {
	if g := NewGatus("http://x.example", nil, 30); g.c.hc.Timeout != 5*time.Second {
		t.Errorf("default timeout = %v, want 5s", g.c.hc.Timeout)
	}
	if g := NewGatus("http://x.example", nil, 30, Timeout(7*time.Second)); g.c.hc.Timeout != 7*time.Second {
		t.Errorf("Timeout option: timeout = %v, want 7s", g.c.hc.Timeout)
	}
}

func TestGatusCategories(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "login at gatus", http.StatusUnauthorized) })
	_, err := NewGatus(s.URL, nil, 30).Statuses(ctx)
	if got := Category(err); got != CategoryAuth {
		t.Errorf("category = %q, want %q", got, CategoryAuth)
	}
}
