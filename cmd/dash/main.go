// Command dash serves the homelab dashboard: REST + SSE API and the embedded SPA.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// JSON so VictoriaLogs parses the audit fields.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	res, err := config.FromEnv()
	if err != nil {
		return err
	}
	cfg := res.Config

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	h := hub.New()
	var srv *api.Server
	var handler http.Handler
	if cfg.Demo {
		cfg = demo.Config(cfg)
		src := demo.New(cfg)
		src.Run(ctx, h)
		srv = api.New(api.Deps{
			Cfg: cfg, Version: version, Hub: h, Prom: src, AM: src, VLogs: src, Data: src, Ready: src.Ready,
		})
		handler = demo.Wrap(srv.Handler())
		slog.Info("demo mode: serving fake data, no cluster or upstreams are contacted")
	} else {
		if err := resolveProxmoxNode(ctx, &cfg, upstream.NewProxmox(cfg.ProxmoxURL, "", cfg.ProxmoxToken, nil).Nodes, proxmoxDiscoveryBudget); err != nil {
			return err
		}
		rc, err := kube.LoadConfig(cfg)
		if err != nil {
			return err
		}
		cluster, err := kube.New(rc)
		if err != nil {
			return err
		}

		// Outside the cluster the monitoring services are reached through the API server proxy.
		var rt http.RoundTripper
		if cfg.Dev {
			if rt, err = upstream.DevTransport(rc); err != nil {
				return err
			}
		}
		prom := upstream.NewProm(cfg.VMURL, rt)
		am := upstream.NewAlertmanager(cfg.AlertmanagerURL, rt)
		vlogs := upstream.NewVLogs(cfg.VLogsURL, rt)
		gatus := upstream.NewGatus(cfg.GatusURL, rt)
		pve := upstream.NewProxmox(cfg.ProxmoxURL, cfg.ProxmoxNode, cfg.ProxmoxToken, nil) // not a .svc host

		mgr := topics.New(topics.Deps{
			Cfg: cfg, Cluster: cluster, Prom: prom, AM: am, Gatus: gatus, PVE: pve, Hub: h, Links: links.New(cfg),
		})
		mgr.Run(ctx)

		srv = api.New(api.Deps{
			Cfg: cfg, Version: version, Hub: h, Kube: cluster.Client, Dyn: cluster.Dynamic,
			Prom: prom, AM: am, VLogs: vlogs, Data: mgr, Ready: mgr.Ready,
		})
		handler = srv.Handler()
	}

	// Long-lived SSE requests derive their context from baseCtx, cancelled on shutdown.
	baseCtx, cancelStreams := context.WithCancel(context.Background())
	defer cancelStreams()
	apiSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    64 << 10,
		IdleTimeout:       120 * time.Second, // no WriteTimeout: SSE streams are open-ended; each SSE write has its own deadline (internal/api/sse.go)
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	apiSrv.RegisterOnShutdown(cancelStreams)
	healthSrv := &http.Server{
		Addr:              cfg.HealthListen,
		Handler:           srv.HealthHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		MaxHeaderBytes:    8 << 10,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	errc := make(chan error, 2)
	serve := func(s *http.Server, role string) {
		slog.Info("listening", "addr", s.Addr, "role", role)
		errc <- s.ListenAndServe()
	}
	go serve(apiSrv, "api")
	go serve(healthSrv, "health")

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
