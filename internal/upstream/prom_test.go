package upstream

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

func TestPromInstant(t *testing.T) {
	var gotForm string
	var gotMethod, gotPath string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotForm, gotMethod, gotPath = string(b), r.Method, r.URL.Path
		io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"__name__":"up","instance":"192.0.2.20:2381","job":"etcd","node":"node-1"},"value":[1793396394,"1"]},
			{"metric":{},"value":[1793396394.5,"0.25"]},
			{"metric":{"job":"nan"},"value":[1793396394,"NaN"]},
			{"metric":{"job":"inf"},"value":[1793396394,"+Inf"]}]}}`)
	})
	got, err := NewProm(s.URL, nil).Instant(ctx, `up{job="etcd"}`)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/query" || gotForm != "query=up%7Bjob%3D%22etcd%22%7D" {
		t.Fatalf("request: %s %s %q", gotMethod, gotPath, gotForm)
	}
	want := []Sample{
		{Labels: map[string]string{"__name__": "up", "instance": "192.0.2.20:2381", "job": "etcd", "node": "node-1"}, Value: 1},
		{Labels: map[string]string{}, Value: 0.25},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestPromErrors(t *testing.T) {
	body, code := "", 200
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		io.WriteString(w, body)
	})
	p := NewProm(s.URL, nil)
	for name, tc := range map[string]struct {
		code int
		body string
	}{
		"promql error":   {200, `{"status":"error","errorType":"bad_data","error":"parse error"}`},
		"http error":     {422, `{"status":"error"}`},
		"wrong type":     {200, `{"status":"success","data":{"resultType":"matrix","result":[]}}`},
		"garbage":        {200, `<html>`},
		"empty document": {200, ``},
	} {
		t.Run(name, func(t *testing.T) {
			code, body = tc.code, tc.body
			if _, err := p.Instant(ctx, "x"); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := NewProm("", nil).Instant(ctx, "x"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestPromRange(t *testing.T) {
	var form string
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form = string(b)
		io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"instance":"a"},"values":[[1793396000,"1.5"],[1793396060,"NaN"],[1793396120,"2"]]},
			{"metric":{"instance":"b"},"values":[[1793396000,"+Inf"]]}]}}`)
	})
	start := time.Unix(1793396000, 0)
	got, err := NewProm(s.URL, nil).Range(ctx, "up", start, start.Add(time.Hour), 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if form != "end=1793399600&query=up&start=1793396000&step=60" {
		t.Fatalf("form = %q", form)
	}
	want := []model.SeriesLine{
		{Labels: map[string]string{"instance": "a"}, Points: [][2]float64{{1793396000, 1.5}, {1793396120, 2}}},
		{Labels: map[string]string{"instance": "b"}, Points: [][2]float64{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestNewPromOptions(t *testing.T) {
	t.Run("the default timeout is 5s", func(t *testing.T) {
		if got := NewProm("http://x.example", nil).c.hc.Timeout; got != 5*time.Second {
			t.Errorf("timeout = %v", got)
		}
	})
	t.Run("Timeout cuts a slow query", func(t *testing.T) {
		release := make(chan struct{})
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		t.Cleanup(func() { close(release) }) // runs before the server's Close, which waits for the handler
		_, err := NewProm(s.URL, nil, Timeout(50*time.Millisecond)).Instant(ctx, "up")
		if err == nil || Category(err) != "timeout" {
			t.Errorf("err = %v, category %q, want timeout", err, Category(err))
		}
	})
	t.Run("CheckRedirect is the client's policy", func(t *testing.T) {
		errStop := errors.New("stop")
		s := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) })
		_, err := NewProm(s.URL, nil, CheckRedirect(func(*http.Request, []*http.Request) error { return errStop })).Instant(ctx, "up")
		if !errors.Is(err, errStop) {
			t.Errorf("err = %v, want the policy's error", err)
		}
	})
}
