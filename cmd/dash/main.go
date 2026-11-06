// Command dash serves the homelab dashboard: REST + SSE API and the embedded SPA.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/overview"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/push"
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
	if err := run(reg, nil); err != nil {
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
	if len(args) >= 1 && args[0] == "hash-password" {
		return hashPasswordCommand(args[1:], os.Stdin, out)
	}
	if len(args) >= 1 && args[0] == "token" {
		return tokenCommand(args[1:], out)
	}
	if len(args) >= 1 && args[0] == "healthcheck" {
		return healthcheckCommand(args[1:])
	}
	if len(args) >= 1 && args[0] == "docs" { // not in the usage: it writes this repository's docs
		return docsCommand(args[1:])
	}
	return fmt.Errorf("unknown command %q; usage: %s | %s | %s | %s | %s | %s", strings.Join(args, " "), configUsage, auditUsage, apiUsage, hashPasswordUsage, tokenUsage, healthcheckUsage)
}

// exitCode is the exit status of a subcommand that returned err: 1 when it ran and found a
// problem (its report is on stdout; healthcheck's one line on stderr), 2 for a usage error.
func exitCode(err error) int {
	if errors.Is(err, errAuditFailed) || errors.Is(err, errConfigInvalid) || errors.Is(err, errUnhealthy) {
		return 1
	}
	return 2
}

// run starts dash and serves until a signal; onBound, when not nil, receives the addresses both
// listeners bound (a test that listens on port 0 learns them there).
func run(reg *secret.Registry, onBound func(api, health net.Addr)) error {
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
	adminHash, err := config.AdminPasswordHash(os.Environ(), reg)
	if err != nil {
		return err
	}
	pa, err := startAuth(res, processSessionEnv(adminHash, reg), slog.Default())
	if err != nil {
		return err
	}
	defer pa.close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Rotated secret files reach the running credentials, and a changed plugins.* section reaches
	// the plugin supervisor (newFramework). The reloader's loop ends with ctx and is awaited
	// before the audit log and the store close.
	// metrics and supervisor are set below, before any plugin starts, and read by the transport
	// hook when a plugin builds its HTTP client: the hook is what puts the upstream metrics (and,
	// on a laptop, the API server proxy for in-cluster services) under every plugin's client. A
	// connection check from the settings page gets the proxy but not the metrics: it is not the
	// running plugin's traffic, and a failing test must not raise its error counters.
	var (
		metrics *obs.Metrics
		sup     *plugin.Supervisor
	)
	route := func(id plugin.ID, rt http.RoundTripper) http.RoundTripper {
		if id != "kubernetes" { // the plugin's own client talks to the API server directly
			rt = viaAPIServer{next: rt, service: func(id plugin.ID) (any, bool) { return sup.Service(id) }}
		}
		return rt
	}
	transport := func(id plugin.ID, rt http.RoundTripper) http.RoundTripper {
		return metrics.Upstream(string(id), route(id, rt))
	}
	fw := newFramework(res, store, config.ReloadOptions{}, reg, refs, transport, route, cfg.Demo)
	h, sup, reloader := fw.hub, fw.sup, fw.reloader
	fw.reloadAuth(pa.proxy)
	fw.reloadTokens(pa.tokens)
	if pa.sess != nil {
		fw.reloadOIDC(pa.sess.roles)
	}
	reloaderDone := make(chan struct{})
	go func() { defer close(reloaderDone); reloader.Run(ctx) }()
	defer func() { stop(); <-reloaderDone }()
	if pa.store != nil {
		// Close writes the last ones after the loop ends (pa.close).
		go pa.store.RunLastUsedFlush(ctx, credentials.LastUsedFlushInterval)
	}
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
	deps := api.Deps{
		Cfg: cfg, Version: version, Hub: h, Plugins: sup,
		Health: sup, Metrics: metrics, Secrets: reg, Audit: alog,
		MCPEnabled: func() bool { return reloader.Tree().MCP.Enabled },
	}
	if store != nil { // demo mode has no store and no settings API
		deps.Settings = newSettingsBackend(res, store, reloader, sup, alog)
	}
	pa.deps(&deps)
	if pa.push {
		// At shutdown (defers run last-in first-out) the sweep and the notifier stop first, then
		// the sender's deliveries end, then the store closes.
		svc := push.New(push.Options{
			Store: pa.store, Contact: cfg.PushContact, Allow: cfg.PushAllow, PublicURL: cfg.PublicURL,
			Metrics: metrics, Secrets: reg,
		})
		defer svc.Close()
		var loops sync.WaitGroup
		defer loops.Wait()
		pushCtx, stopPush := context.WithCancel(ctx)
		defer stopPush()
		notifier := push.NewNotifier(push.NotifierOptions{Service: svc, Store: pa.store, MayNotify: pushMayNotify(res.Tree.Auth)})
		loops.Go(func() { svc.RunSweep(pushCtx) })
		loops.Go(func() { notifier.Run(pushCtx, h) })
		deps.Push = svc
	}
	srv := api.New(deps)
	srv.LogMCPState()
	reloader.Subscribe(func(c config.Change) {
		if c.Section == "mcp" { // mcp.enabled is live
			srv.LogMCPState()
		}
	})
	handler := srv.Handler()
	if cfg.Demo {
		slog.Info("demo mode: serving fake data, no cluster or upstreams are contacted")
	}

	// Long-lived SSE requests derive their context from baseCtx, cancelled on shutdown.
	baseCtx, cancelStreams := context.WithCancel(context.Background())
	defer cancelStreams()
	apiSrv, healthSrv := newServers(cfg, handler, srv.HealthHandler(), baseCtx, cancelStreams)
	return serve(ctx, apiSrv, healthSrv, srv.MarkServing, cancelStreams, onBound)
}

// processSessionEnv is the sessionEnv run starts the auth modes with: the admin hash it read,
// stderr for the setup line, and reg, which cuts the IdP's error text (oidcauth.Config.Secrets).
func processSessionEnv(adminHash string, reg *secret.Registry) sessionEnv {
	return sessionEnv{adminHash: adminHash, stderr: os.Stderr, secrets: reg}
}
