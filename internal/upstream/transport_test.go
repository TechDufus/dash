package upstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRewriteSvcURL(t *testing.T) {
	tests := []struct {
		name, in, api, want string
		ok                  bool
	}{
		{"query preserved", "http://vmsingle-vm.monitoring.svc:8428/api/v1/query?query=up",
			"https://10.0.20.20:6443",
			"https://10.0.20.20:6443/api/v1/namespaces/monitoring/services/vmsingle-vm:8428/proxy/api/v1/query?query=up", true},
		{"encoded query preserved", "http://vl-server.monitoring.svc:9428/select/logsql/query?query=_time%3A5m+%22x%22&limit=500",
			"https://k:6443",
			"https://k:6443/api/v1/namespaces/monitoring/services/vl-server:9428/proxy/select/logsql/query?query=_time%3A5m+%22x%22&limit=500", true},
		{"apiserver path prefix", "http://gatus.gatus.svc:8080/api/v1/endpoints/statuses",
			"https://rancher.example/k8s/clusters/c-1/",
			"https://rancher.example/k8s/clusters/c-1/api/v1/namespaces/gatus/services/gatus:8080/proxy/api/v1/endpoints/statuses", true},
		{"no port means 80", "http://web.apps.svc/x", "https://k:6443",
			"https://k:6443/api/v1/namespaces/apps/services/web:80/proxy/x", true},
		{"empty path", "http://web.apps.svc:8080", "https://k:6443",
			"https://k:6443/api/v1/namespaces/apps/services/web:8080/proxy", true},
		{"silence id path", "http://vmalertmanager-vm.monitoring.svc:9093/api/v2/silence/abc-123", "https://k:6443",
			"https://k:6443/api/v1/namespaces/monitoring/services/vmalertmanager-vm:9093/proxy/api/v2/silence/abc-123", true},
		{"public host", "https://proxmox.home.techdufus.com:8006/api2/json/x", "https://k:6443", "", false},
		{"cluster.local is not .svc", "http://a.b.svc.cluster.local:80/", "https://k:6443", "", false},
		{"too many labels", "http://a.b.c.svc:80/", "https://k:6443", "", false},
		{"bare svc", "http://svc/", "https://k:6443", "", false},
		{"missing namespace", "http://a.svc/", "https://k:6443", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RewriteSvcURL(mustURL(t, tc.in), mustURL(t, tc.api))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got.String() != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSvcRewriterRoundTrip(t *testing.T) {
	var apiGot, plainGot *http.Request
	var apiBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiGot = r
		b, _ := io.ReadAll(r.Body)
		apiBody = string(b)
		w.Header().Set("X-From", "api")
		w.WriteHeader(http.StatusCreated)
	}))
	defer api.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainGot = r
		w.Header().Set("X-From", "plain")
	}))
	defer plain.Close()

	rt := newSvcRewriter(http.DefaultTransport, http.DefaultTransport, mustURL(t, api.URL+"/prefix"))
	hc := &http.Client{Transport: rt}

	req, _ := http.NewRequest(http.MethodPost, "http://vmsingle-vm.monitoring.svc:8428/api/v1/query?query=up", strings.NewReader("a=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Custom", "1")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("X-From") != "api" {
		t.Fatalf("service request did not reach the apiserver: %d %v", resp.StatusCode, resp.Header)
	}
	if apiGot.Method != http.MethodPost ||
		apiGot.URL.Path != "/prefix/api/v1/namespaces/monitoring/services/vmsingle-vm:8428/proxy/api/v1/query" ||
		apiGot.URL.RawQuery != "query=up" ||
		apiGot.Header.Get("X-Custom") != "1" || apiGot.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
		apiBody != "a=b" {
		t.Fatalf("apiserver saw %s %s body=%q hdr=%v", apiGot.Method, apiGot.URL, apiBody, apiGot.Header)
	}
	if apiGot.Host != mustURL(t, api.URL).Host {
		t.Fatalf("Host = %q, want the apiserver's", apiGot.Host)
	}
	if req.URL.Host != "vmsingle-vm.monitoring.svc:8428" {
		t.Fatal("original request was mutated")
	}

	resp, err = hc.Get(plain.URL + "/x?y=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-From") != "plain" || plainGot.URL.String() != "/x?y=1" {
		t.Fatalf("plain host was rewritten: %v", plainGot.URL)
	}
}

// baseRT records the requests DevWrap's base transport receives.
type baseRT struct{ got []*http.Request }

func (b *baseRT) RoundTrip(r *http.Request) (*http.Response, error) {
	b.got = append(b.got, r)
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
}

func TestDevWrapKeepsTheCallersTransportForOtherHosts(t *testing.T) {
	var apiPath string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { apiPath = r.URL.Path }))
	defer api.Close()
	wrap, err := DevWrap(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	base := &baseRT{}
	hc := &http.Client{Transport: wrap(base)}

	resp, err := hc.Get("http://grafana.monitoring.svc:3000/d/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(base.got) != 0 || apiPath != "/api/v1/namespaces/monitoring/services/grafana:3000/proxy/d/x" {
		t.Fatalf("an in-cluster service must go through the API server proxy: base saw %d, api saw %q", len(base.got), apiPath)
	}

	resp, err = hc.Get("https://dash.example.com/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(base.got) != 1 || base.got[0].URL.Host != "dash.example.com" {
		t.Fatalf("any other host must use the caller's transport, got %d requests", len(base.got))
	}
}
