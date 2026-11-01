package upstream

import (
	"io"
	"net/http"
	"testing"
)

func TestGatusStatuses(t *testing.T) {
	var rawQuery string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		io.WriteString(w, `[
		 {"name":"Grafana","group":"apps","key":"apps_grafana","results":[
		   {"status":200,"hostname":"grafana.home.techdufus.com","duration":10172175,"conditionResults":[{"condition":"[STATUS] == 200","success":true}],"success":true,"timestamp":"2026-10-30T21:10:15.668367726Z"},
		   {"status":500,"hostname":"grafana.home.techdufus.com","duration":2500000,"success":false,"timestamp":"2026-10-30T21:40:15.668367726Z"}]},
		 {"name":"New","group":"apps","key":"apps_new","results":[]}]`)
	})
	got, err := NewGatus(s.URL, nil).Statuses(ctx)
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
	if g.Group != "apps" || g.Name != "Grafana" || g.Key != "apps_grafana" || g.URL != "grafana.home.techdufus.com" ||
		g.Healthy || len(g.Results) != 2 || !g.Results[0].Success || g.Results[0].DurationMs != 10.172175 ||
		!g.Results[0].Timestamp.Before(g.Results[1].Timestamp) {
		t.Fatalf("endpoint: %+v", g)
	}
	if got[1].Results == nil || got[1].Healthy || got[1].URL != "" {
		t.Fatalf("empty endpoint: %+v", got[1])
	}
}
