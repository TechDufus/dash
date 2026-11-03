package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/TechDufus/dash/internal/config"
)

// newServers builds the API listener and the health listener. Long-lived SSE requests derive
// their context from baseCtx; cancelStreams (also called on Shutdown) ends them.
func newServers(cfg config.Config, api, health http.Handler, baseCtx context.Context, cancelStreams func()) (apiSrv, healthSrv *http.Server) {
	apiSrv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           api,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    cfg.Limits.MaxHeaderBytes,
		IdleTimeout:       120 * time.Second, // no WriteTimeout: SSE streams are open-ended; each SSE write has its own deadline (internal/api/sse.go)
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	apiSrv.RegisterOnShutdown(cancelStreams)
	healthSrv = &http.Server{
		Addr:              cfg.HealthListen,
		Handler:           health,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		MaxHeaderBytes:    8 << 10,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return apiSrv, healthSrv
}

// listen opens serve's listeners; a test replaces it to see the listeners serve opened.
var listen = net.Listen

// serve binds both listeners, calls onBound (when not nil) with their addresses, calls
// markServing (readiness: the sockets are open), then serves until ctx ends or a listener fails.
// Binding first means an address in use is an error before /readyz can say ok. On return both
// servers are shut down, within 10 seconds.
func serve(ctx context.Context, apiSrv, healthSrv *http.Server, markServing func(), cancelStreams func(), onBound func(api, health net.Addr)) error {
	apiLn, err := listen("tcp", apiSrv.Addr)
	if err != nil {
		cancelStreams()
		return err
	}
	healthLn, err := listen("tcp", healthSrv.Addr)
	if err != nil {
		_ = apiLn.Close()
		cancelStreams()
		return err
	}
	if onBound != nil {
		onBound(apiLn.Addr(), healthLn.Addr())
	}
	markServing()

	errc := make(chan error, 2)
	run := func(s *http.Server, ln net.Listener, role string) {
		slog.Info("listening", "addr", ln.Addr().String(), "role", role)
		errc <- s.Serve(ln)
	}
	go run(apiSrv, apiLn, "api")
	go run(healthSrv, healthLn, "health")

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			cancelStreams()
			return err
		}
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = apiSrv.Shutdown(sctx)
	_ = healthSrv.Shutdown(sctx)
	return nil
}

// warnSettledRequirements logs, once at startup, each server.readyRequires entry whose plugin
// is disabled: readiness does not wait for a plugin that will never start.
func warnSettledRequirements(t config.Tree, requires []string) {
	disabled := map[string]bool{}
	for _, s := range t.PluginSections() {
		disabled[s.Name] = s.Enabled != nil && !*s.Enabled
	}
	for _, id := range requires {
		if disabled[id] {
			slog.Warn("server.readyRequires names a disabled plugin; readiness does not wait for it", "plugin", id)
		}
	}
}
