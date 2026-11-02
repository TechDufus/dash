// Command dash serves the homelab dashboard: REST + SSE API and the embedded SPA.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		if err := command(os.Args[1:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "dash:", err)
			os.Exit(exitCode(err))
		}
		return
	}
	// JSON so VictoriaLogs parses the audit fields. Every record passes the redacting handler,
	// which scrubs the secrets the configuration resolves (registered as it does) and
	// credential-shaped tokens.
	reg := secret.NewRegistry()
	slog.SetDefault(slog.New(secret.NewHandler(slog.NewJSONHandler(os.Stderr, nil), reg)))
	if err := run(reg); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// command runs a subcommand, which prints to out and exits instead of starting the server.
func command(args []string, out io.Writer) error {
	if len(args) >= 1 && args[0] == "config" {
		return configCommand(args[1:], out)
	}
	if len(args) >= 2 && args[0] == "audit" && args[1] == "verify" {
		return auditVerify(args[2:], out)
	}
	if len(args) >= 1 && args[0] == "api" {
		return apiCommand(args[1:], out)
	}
	return fmt.Errorf("unknown command %q; usage: %s | %s | %s", strings.Join(args, " "), configUsage, auditUsage, apiUsage)
}

// exitCode is the exit status of a subcommand that returned err: 1 when it ran and found a
// problem (its report is on stdout), 2 for a usage error.
func exitCode(err error) int {
	if errors.Is(err, errAuditFailed) || errors.Is(err, errConfigInvalid) {
		return 1
	}
	return 2
}

func run(reg *secret.Registry) error {
	res, store, alog, err := openState(reg)
	if err != nil {
		return err
	}
	// The store and the audit log close after the servers stop, the log first (defers run
	// last-in first-out).
	if store != nil {
		defer store.Close()
	}

	defer alog.Close()
	cfg := res.Config
	warnSettledRequirements(res.Tree, cfg.ReadyRequires)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Rotated secret files reach the running credentials, and a changed plugins.* section reaches
	// the plugin supervisor (newFramework). The reloader's loop ends with ctx and is awaited
	// before the audit log and the store close.
	// metrics and devWrap are set below, before any plugin starts, and read by the transport hook
	// when a plugin builds its HTTP client: the hook is what puts the upstream metrics (and, on a
	// laptop, the API server proxy for in-cluster services) under every plugin's client.
	var (
		metrics *obs.Metrics
		devWrap func(http.RoundTripper) http.RoundTripper
	)
	transport := func(id plugin.ID, rt http.RoundTripper) http.RoundTripper {
		if devWrap != nil {
			rt = devWrap(rt)
		}
		return metrics.Upstream(string(id), rt)
	}
	fw := newFramework(res, store, config.ReloadOptions{}, reg, transport, cfg.Demo)
	h, sup, reloader := fw.hub, fw.sup, fw.reloader
	reloaderDone := make(chan struct{})
	go func() { defer close(reloaderDone); reloader.Run(ctx) }()
	defer func() { stop(); <-reloaderDone }()
	// Plugins stop after the servers, before the reloader and the audit log.
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sup.Shutdown(sctx)
	}()

	// Metrics are opt-in (server.metrics); a nil *obs.Metrics is "off" everywhere it is used.
	if cfg.Metrics {
		metrics = obs.New(version, api.RoutePatterns(), sup.Infos)
	}
	var srv *api.Server
	var handler http.Handler
	if cfg.Demo {
		cfg = demo.Config(cfg)
		src := demo.New(cfg)
		src.HasReal = hasRealPlugin
		if err := fw.start(ctx, registry(src.Plugins(sup)), func() { src.Run(ctx) }); err != nil {
			return err
		}
		srv = api.New(api.Deps{
			Cfg: cfg, Version: version, Hub: h, Plugins: sup, Data: src, Health: sup, Metrics: metrics, Secrets: reg, Audit: alog,
		})
		handler = demo.Wrap(srv.Handler())
		slog.Info("demo mode: serving fake data, no cluster or upstreams are contacted")
	} else {
		rc, err := kube.LoadConfig(cfg)
		if err != nil {
			return err
		}
		cluster, err := kube.New(rc)
		if err != nil {
			return err
		}

		// Outside the cluster the monitoring services are reached through the API server proxy,
		// through the transport hook of the framework.
		if cfg.Dev {
			if devWrap, err = upstream.DevWrap(rc); err != nil {
				return err
			}
		}
		mgr := topics.New(topics.Deps{
			Cfg: cfg, Cluster: cluster, Hosts: sup, Contributors: sup.Contributors,
			Services: sup.Service, LinkProviders: sup.Links, Secrets: reg,
		})
		// The supervisor starts before the Manager so that no first publish is dropped.
		if err := fw.start(ctx, registry(mgr.Plugins()), func() { mgr.Run(ctx) }); err != nil {
			return err
		}

		srv = api.New(api.Deps{
			Cfg: cfg, Version: version, Hub: h, Plugins: sup, Kube: cluster.Client, Dyn: cluster.Dynamic,
			Data: mgr, Health: sup, Metrics: metrics, Secrets: reg, Audit: alog,
		})
		handler = srv.Handler()
	}

	// Long-lived SSE requests derive their context from baseCtx, cancelled on shutdown.
	baseCtx, cancelStreams := context.WithCancel(context.Background())
	defer cancelStreams()
	apiSrv, healthSrv := newServers(cfg, handler, srv.HealthHandler(), baseCtx, cancelStreams)
	return serve(ctx, apiSrv, healthSrv, srv.MarkServing, cancelStreams)
}
