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
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/overview"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
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
	// One per process: the k8s: references of every resolution (the reloader's too) share its
	// holders, and the kubernetes plugin reads the Secrets they name.
	refs := secret.NewK8sRefs(reg)
	res, store, alog, err := openState(reg, refs)
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
	// metrics and supervisor are set below, before any plugin starts, and read by the transport
	// hook when a plugin builds its HTTP client: the hook is what puts the upstream metrics (and,
	// on a laptop, the API server proxy for in-cluster services) under every plugin's client.
	var (
		metrics *obs.Metrics
		sup     *plugin.Supervisor
	)
	transport := func(id plugin.ID, rt http.RoundTripper) http.RoundTripper {
		if id != "kubernetes" { // the plugin's own client talks to the API server directly
			rt = viaAPIServer{next: rt, service: func(id plugin.ID) (any, bool) { return sup.Service(id) }}
		}
		return metrics.Upstream(string(id), rt)
	}
	fw := newFramework(res, store, config.ReloadOptions{}, reg, refs, transport, cfg.Demo)
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
	// Demo and live share one path: the supervisor runs the plugins (their demo twins in demo
	// mode), then the overview is composed from what they contribute. The supervisor starts
	// before the publisher so that no first publish is dropped.
	ov := overview.New(overview.Deps{Hosts: sup, Contributors: sup.Contributors})
	if err := fw.start(ctx, registry(refs), func() { ov.Run(ctx) }); err != nil {
		return err
	}
	srv := api.New(api.Deps{
		Cfg: cfg, Version: version, Hub: h, Plugins: sup,
		Health: sup, Metrics: metrics, Secrets: reg, Audit: alog,
	})
	handler := srv.Handler()
	if cfg.Demo {
		handler = demo.SignIn(handler)
		slog.Info("demo mode: serving fake data, no cluster or upstreams are contacted")
	}

	// Long-lived SSE requests derive their context from baseCtx, cancelled on shutdown.
	baseCtx, cancelStreams := context.WithCancel(context.Background())
	defer cancelStreams()
	apiSrv, healthSrv := newServers(cfg, handler, srv.HealthHandler(), baseCtx, cancelStreams)
	return serve(ctx, apiSrv, healthSrv, srv.MarkServing, cancelStreams)
}
