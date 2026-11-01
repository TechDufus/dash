package upstream

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"
)

// DevTransport lets a laptop reach in-cluster services: requests to
// <svc>.<ns>.svc[:port] go through the API server's service proxy using the
// kube credentials; every other host uses a plain transport.
func DevTransport(cfg *rest.Config) (http.RoundTripper, error) {
	wrap, err := DevWrap(cfg)
	if err != nil {
		return nil, err
	}
	return wrap(http.DefaultTransport.(*http.Transport).Clone()), nil
}

// DevWrap is DevTransport over transports of the caller's: wrap(base) sends the requests to
// <svc>.<ns>.svc through the API server's service proxy and every other request through base, so
// a plugin's own client (its CA, credentials and dial guard) keeps serving the hosts that are not
// in-cluster services.
func DevWrap(cfg *rest.Config) (wrap func(base http.RoundTripper) http.RoundTripper, err error) {
	kube, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}
	host := cfg.Host
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	api, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse apiserver host %q: %w", cfg.Host, err)
	}
	return func(base http.RoundTripper) http.RoundTripper { return newSvcRewriter(kube, base, api) }, nil
}

// RewriteSvcURL maps http://<svc>.<ns>.svc[:port]/path?q to the API server's
// service proxy URL; ok is false for any other host.
func RewriteSvcURL(u *url.URL, apiServer *url.URL) (*url.URL, bool) {
	host, found := strings.CutSuffix(u.Hostname(), ".svc")
	if !found {
		return nil, false
	}
	svc, ns, found := strings.Cut(host, ".")
	if !found || svc == "" || ns == "" || strings.Contains(ns, ".") {
		return nil, false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	raw := strings.TrimRight(apiServer.EscapedPath(), "/") +
		"/api/v1/namespaces/" + url.PathEscape(ns) + "/services/" + url.PathEscape(svc) + ":" + port + "/proxy" + u.EscapedPath()
	path, err := url.PathUnescape(raw)
	if err != nil {
		return nil, false
	}
	return &url.URL{Scheme: apiServer.Scheme, Host: apiServer.Host, Path: path, RawPath: raw, RawQuery: u.RawQuery}, true
}

type svcRewriter struct {
	kube, plain http.RoundTripper
	apiServer   *url.URL
}

func newSvcRewriter(kube, plain http.RoundTripper, apiServer *url.URL) *svcRewriter {
	return &svcRewriter{kube: kube, plain: plain, apiServer: apiServer}
}

func (s *svcRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	u, ok := RewriteSvcURL(req.URL, s.apiServer)
	if !ok {
		return s.plain.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	r.URL = u
	r.Host = ""
	return s.kube.RoundTrip(r)
}
