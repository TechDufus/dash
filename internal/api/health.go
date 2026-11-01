package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/TechDufus/dash/internal/model"
)

// HealthSource is the plugin half of readiness. *plugin.Supervisor implements it.
type HealthSource interface {
	// Ready reports that every plugin's Start has returned and every plugin in
	// server.readyRequires has left "starting" (or server.readyTimeout has passed).
	Ready() bool
	// Infos is the state of every plugin, for the verbose readiness report.
	Infos() []model.PluginInfo
}

// MarkServing records that the listeners are bound. Until then /readyz answers 503, so a
// bind failure never shows up as a ready pod.
func (s *Server) MarkServing() { s.serving.Store(true) }

// ready is readiness: the configuration loaded (the server exists), the listeners serve, and
// the plugins have settled. A plugin in error does not make dash unready: one broken upstream
// must not take the dashboard out of the Service.
func (s *Server) ready() bool {
	return s.serving.Load() && (s.d.Health == nil || s.d.Health.Ready())
}

// HealthHandler serves only /healthz and /readyz, for the unauthenticated health listener.
//
// /readyz answers "ok" (200) or "not ready" (503). With ?verbose=1 and a loopback TCP peer
// (kubectl port-forward, an exec into the pod) the body also lists one "<id> <state>[ <message>]"
// line per plugin; any other peer gets the plain body, because plugin messages can name
// internal hosts.
func (s *Server) HealthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		var b strings.Builder
		if s.ready() {
			b.WriteString("ok")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			b.WriteString("not ready")
		}
		if r.URL.Query().Get("verbose") == "1" && s.d.Health != nil && loopbackPeer(r) {
			for _, p := range s.d.Health.Infos() {
				b.WriteString("\n" + p.ID + " " + string(p.State))
				if m := oneLine(p.Message); m != "" && m != string(p.State) { // "disabled disabled" says nothing
					b.WriteString(" " + m)
				}
			}
		}
		_, _ = w.Write([]byte(b.String()))
	})
	return mux
}

// loopbackPeer reports whether the TCP peer is a loopback address. Headers play no part.
func loopbackPeer(r *http.Request) bool {
	ip := net.ParseIP(peerOf(r))
	return ip != nil && ip.IsLoopback()
}

// oneLine keeps a plugin's message on its own line of the report.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
