# Plugin architecture, layered config and open-source readiness

| | |
|---|---|
| Status | Proposal (plan only, no code changes). The owner decided all 19 open questions on 2026-10-31 (section J); they are recorded as an ADR in `docs/adr/0001-plugins-config-auth.md` (P0.2) |
| Owner | TechDufus |
| Base | Builds on the **not-yet-merged** branch `claude/project-thread-8gve8b` (tip `e2b7f93`, "feat(config): make site-specific settings configurable"), which sits 3 commits ahead of `main` (`3fe842c`): `397f4c6` demo mode, `ba73150` README, `e2b7f93` configurable settings. |
| Line references | Every `file:line` below refers to `e2b7f93`. Several cited files (`dev/site.env.example`, `docs/launcher.example.yaml`, the `Conventions` struct, `GET /api/config`) do not exist on `main` until that branch lands. |
| Companion | [`epic-draft.md`](epic-draft.md): the issue epic that implements this plan. It gets filed only after this plan is approved. [`clients-and-ai.md`](clients-and-ai.md) (sections K–S, continuing A–J) is the second companion: the security review of this plan, API clients, MCP agents, mobile and AI. It amends this plan where marked. |

## Goal

The owner put it this way: "make the project configurable for users via environment variables, a config file, secrets in a cluster, etc., perhaps with a settings dashboard listing plugins (a Proxmox plugin, a Grafana plugin, etc.) as the way systems are integrated, instead of hard-coded dashboards. It should feel modular, so somebody could host dash and configure all the supported connections." Also: "it should be runnable as a stand-alone docker file with a volume, docker compose, any other cloud thing, and in my case just kubernetes." All of this is required before the repository goes public.

Non-goals for this plan:

- Loading third-party code at runtime, whether remote JavaScript or dynamically loaded Go.
- Multi-tenant SaaS.
- High availability with more than one replica writing settings.
- Telemetry. dash will not phone home, and this plan adds nothing that does.

## Summary of decisions

1. **Plugins are in-process Go packages, listed explicitly at compile time.** Each one sits behind a small interface (`internal/plugin`): a manifest, a config struct that generates a JSON Schema, a status, topics, actions and series. The SPA has a matching module per plugin ID, compiled into the bundle. `GET /api/plugins` tells the SPA which plugins are enabled, and the SPA never loads remote JS. Out-of-process plugins are a later adapter behind the same interface.
2. **Config layers, from lowest to highest priority:** built-in defaults, then UI settings (`/data/ui-settings.yaml`), then the config file (`DASH_CONFIG`), then environment variables. Any field set by the file or by env is **locked**: the UI shows it read-only, along with where it came from, and the server rejects writes to it. The UI only fills gaps, so GitOps-managed values always win. Secret references (`env:`, `file:`, `k8s:`) are a *value form*, not a separate layer: the file and env layers can use all three, while the UI layer holds only encrypted literals and `k8s:` refs (B.2). The Helm chart defaults to editable UI settings; `settings.ui: readonly` is the option for GitOps installs (J.4).
3. **Secrets never reach the browser.** The API returns only `{set, source, updatedAt}`. Secrets entered in the UI are encrypted at rest with XChaCha20-Poly1305. A dedicated `secret.Value` type redacts itself in logs, JSON and errors.
4. **Kubernetes becomes optional.** Today the process exits without a cluster (`cmd/dash/main.go:59-62`), and readiness is informer sync (`internal/topics/manager.go:105`). The new `kubernetes` plugin auto-detects in-cluster config or a kubeconfig and gates its informers on API discovery. With no cluster, it reports "unavailable" without crashing, and `/readyz` is unaffected. `flux`, `cnpg` and the apps discovery depend on it.
5. **Auth becomes part of dash.** The default for open source is a built-in local admin, bootstrapped with a one-time setup token. Trusted reverse-proxy headers (today's oauth2-proxy setup, now restricted to trusted proxy CIDRs) and built-in OIDC are first-class alternatives. There are two roles, viewer and admin; settings and actions need admin. An anonymous read-only viewer for wall displays is opt-in and off by default. The CSRF and Origin checks stay.
6. **Packaging:** one image (`ghcr.io/techdufus/dash`, multi-arch) that runs with `docker run -v dash-data:/data`, Docker Compose, PaaS platforms and Kubernetes. A Helm chart in the repo, published as an OCI chart to GHCR, is the Kubernetes path (J.7).
7. **Before going public:** remove the self-hosted runner, move CI to GitHub-hosted runners, publish to GHCR with provenance and SBOM attestations, scrub real-lab identifiers from tests and docs, decouple dev tooling from `../homelab`, license the project MIT (J.1), and keep the git history but scrub the lab identifiers from it with `git filter-repo` right before the flip, as a separate step that needs the owner's explicit go-ahead (J.8).
8. **Clients beyond the browser.** Non-browser clients (a native Android app, AI agents over MCP) get bearer tokens, a built-in OAuth 2.1 server, a versioned `/api/v1`, an MCP server that holds agent writes for human approval, and a PWA with push notifications. The design, the security review of this plan and phase 7 are in [`clients-and-ai.md`](clients-and-ai.md) (sections K–S).

Convention: core API paths in sections A–J are written unversioned (`/api/plugins`, `/api/settings`, …). From P2.3 and P2.10 they live under `/api/v1` ([`clients-and-ai.md`](clients-and-ai.md) L.1).

---

## 0. How dash works today (one paragraph)

`cmd/dash/main.go` reads `DASH_*` env vars (`internal/config/config.go:80-124`) and builds a Kubernetes client (`main.go:59-67`). It then builds five fixed upstream clients (`main.go:75-79`) and passes everything to one `topics.Manager` (`main.go:81-84`). The Manager runs informers (`internal/kube/kube.go:119-165`) and six pollers (`manager.go:108-136`), and once a second it rebuilds whichever of the nine fixed topics are dirty (`manager.go:276-370`). It publishes them on the hub (`internal/hub/hub.go:48`), and the hub streams them over one SSE endpoint (`internal/api/sse.go:63-94`). Overview publishes only after all eight other topics have been built at least once (`manager.go:357-365`). The SPA hard-codes its routes (`web/src/App.tsx:35-64`), its nav (`web/src/shell/nav.ts:21-31`) and its topic list (`web/src/api/topics.ts:16-46`, checked at compile time against the Go `Topic` union at `topics.ts:29-34`). Writes are three fixed handlers (`internal/api/api.go:88-91`).

The topics are confirmed in `internal/model/model.go:42-52`: `overview`, `host`, `vms`, `cluster`, `flux`, `storage`, `alerts`, `status` and `apps`.

---

## 1. Audit

### 1.1 Values and coupling

The "Still hard-coded?" column describes `e2b7f93`. The "Action" column points to the section of this plan that removes the value.

| Value | file:line | Kind | Still hard-coded? | Action |
|---|---|---|---|---|
| Public URL required outside dev | `internal/config/config.go:147-149`; dev default `:91` | required setting | No (covered by e2b7f93) | Keep; becomes `server.publicURL` (B) |
| Proxmox, Headlamp and Grafana URLs default to empty; empty drops the links | `config.go:104,109,111`; `internal/links/links.go:76-82` | default | No (covered) | These become plugin fields; an unset field means the plugin is unconfigured (A, F) |
| Proxmox URL and node required with a token | `config.go:150-152` | validation | No (covered) | Moves into the plugin schema as `required` (A.4) |
| Launcher links from a file | `config.go:112,131-138,160-176`; `docs/launcher.example.yaml` | file | No (covered) | Becomes the `launcher` plugin (file or inline list) (F) |
| Host model label | `config.go:108`; `internal/api/api.go:155-161` | setting | No (covered) | Becomes `hardware.hosts[].model` (F) |
| Conventions as settings | `config.go:61-77,113-121` | settings | No (covered), **but the defaults are homelab values** | Clean up the defaults (next rows) |
| `GET /api/config`, returning only `{hostModel, launcher}` | `api.go:81,155-161`; `model.go:644-648` | API | No (covered) | Replaced by `GET /api/plugins` plus plugin-scoped config (A.7) |
| `dev/site.env.example` | `dev/site.env.example:1-8` | dev | No (covered) | Superseded by a dev `config.yaml` example (G) |
| Default `.svc` URLs, specific to the VictoriaMetrics operator: `vmsingle-vm.monitoring`, `vmalertmanager-vm.monitoring`, `vl-server.monitoring`, `gatus.gatus` | `config.go:100-103`; `README.md:103-106` | default URL | **Yes** | Remove the defaults; a plugin is unconfigured until its URL is set (B.5, F) |
| `homelab.io/protected`, `homelab.io/tenant`, `homelab.io/suspended` | `config.go:114-116`; `README.md:126-128`; comment at `model.go:661` | label convention | **Yes** (as defaults) | Default to empty, which turns the feature off; the homelab sets its values explicitly (F) |
| `app-` Flux prefix, `homelab_zfs_scrub`, `rustfs`/`nas-backup` | `config.go:117-120` | convention / metric / object names | **Yes** (as defaults) | Default to empty, which hides the panel (F) |
| Headlamp cluster `main` | `config.go:110` | default | Harmless | Becomes `headlamp.cluster` with the same default |
| `127.0.0.1:8080` listen default (assumes a sidecar proxy) | `config.go:97`; `README.md:101` | default | **Yes** | The container default becomes `0.0.0.0:8080`; requires in-app auth (D.5, E) |
| `GitRepository flux-system/flux-system` treated as the one signed source | `internal/topics/attention.go:134-137`; `internal/topics/overview.go:45`; `web/src/pages/gitops.tsx:144-184,315`; `web/src/shell/command-palette.tsx:81-88` | k8s object name | **Yes** | `flux` plugin: any source with `spec.verify` must be verified, plus an optional explicit list (F.9) |
| Machines `mac-mini` (Prometheus `up`) and `rpi-control` (Gatus endpoint `"rpi-control SSH"`) | `internal/topics/vms.go:19-20,42,100-103` | node names | **Yes** (not in the earlier audit) | `hardware.machines[]` config (F.7) |
| Talos control-plane VMIDs 2020-2022 are critical | `internal/topics/health.go:75-87` | VM IDs | **Yes** (not in the earlier audit) | `proxmox.criticalVMs` (IDs or tag) (F.6) |
| Gatus group `infrastructure` is critical | `health.go:100-109`; `web/src/pages/status.tsx:31-35` | group name | **Yes** (not in the earlier audit) | `gatus.criticalGroups` (F.4) |
| Apps discovery only covers HTTPRoutes parented on `gateway/home`, skips namespace `dash`, and reads `gethomepage.dev/*` annotations | `internal/topics/feeds.go:87-96,117,123-135` | object names / annotations | **Yes** (not in the earlier audit) | `kubernetes.apps` config: gateways, exclusions, annotation prefix (F.8) |
| Ignored alerts `Watchdog`, `InfoInhibitor`; severity mapping | `health.go:133-147` | alert names | Generic (common kube-prometheus names) | `alertmanager.ignoreAlerts` with the same default (F.3) |
| Icon CDN `cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons` | `feeds.go:77`; `web/src/config/links.ts:4` | external URL | **Yes** | `ui.iconBaseURL`: icons are bundled or self-hosted by default, the CDN is opt-in, `""` disables (G.5, J.17) |
| PromQL assumes node-exporter `job="node-exporter"` and the `nodename` label, the Intel `platform_coretemp_*` hwmon chip, IPMI exporter, smartmon textfile, pve-exporter (`pve_*`), RustFS, CNPG barman, etcd `job="etcd"`, kube-state-metrics, and cAdvisor at `metrics_path="/metrics/cadvisor"` | `internal/queries/queries.go:17-118,147-208` | metric names / labels | **Yes** | Query packs owned by plugins, with selector overrides (F.1, F.7) |
| `model.StorageData` has fixed `RustFS`, `CNPG`, `NASBackups`, `Etcd` fields; the Storage page renders "RustFS drive", "CNPG" and "NAS backups" panels | `model.go:521-529`; `internal/topics/storage.go:16-25`; `web/src/pages/storage.tsx:305-308,363-376` | API shape | **Yes** | Storage becomes a composite page with plugin panels (A.8, F.10) |
| Hardware page assumes 16 bays, fan scale 18000 rpm, `inlet`/`exhaust` sensor names | `web/src/pages/hardware.tsx:46-48,148-149` | UI constants | **Yes** | `hardware.hosts[].bays`, `fanMaxRPM`, `sensorAliases` (F.7) |
| Closed TS unions `LinkKind` (`headlamp\|proxmox\|grafana\|app\|external`) and `Group` (`Platform\|Apps\|Tenants`) | `model.go:28-37`; `web/src/components/links.tsx:6-12`; `web/src/components/launcher.tsx:9` | API shape | **Yes** | Link kinds become plugin IDs; groups stay as core names (A.8) |
| "Homelab mission control" and "homelab dashboard" text | `web/src/shell/topbar.tsx:125`; `cmd/dash/main.go:1`; `internal/demo/demo.go:2` | copy | **Yes** | Kept: name and tagline stay (decision J.14) |
| oauth2-proxy and GitHub assumptions in the client reauth path | `web/src/api/stream.ts:40-47`; `web/src/api/client.ts:24-32` | auth coupling | **Yes** | Generic 401 handling, with a redirect to login for the local or OIDC modes (E) |
| Admission message `homelab guardrail:` | comment at `internal/api/actions.go:41`; `internal/api/actions_test.go:154-155,248,253` | test strings | **Yes** | Neutral strings, e.g. `admission denied:` (G.2) |
| CI runner labels `[self-hosted, linux, x64, homelab, dash]` | `.github/workflows/ci.yml:13,22` | CI runner | **Yes** | Use GitHub-hosted runners (G.8, H) |
| Registry `registry.home.techdufus.com/dash/dash` | `ci.yml:31`; `README.md:169-170` | registry | **Yes** | Publish to GHCR (H) |
| `../homelab/scripts/hl-op`, `../homelab/.kube/config`, `dev-api` running through `hl-op` | `Makefile:1-2,16-18`; `README.md:150-151` | dev tooling | **Yes** | Make it optional; give a generic dev path (G.3) |
| `dev/secrets.env` (two lines: a comment that points at `../homelab`, and one `DASH_PROXMOX_TOKEN` 1Password reference; no value) | `dev/secrets.env:1-2` | dev tooling | **Yes** | Delete it from the repo, or replace it with a `.example` (G.3) |
| `dash.home.techdufus.com`, link to the homelab repo, Talos, R730, the oauth2-proxy user `TechDufus`, the homelab guardrail path, the release flow (github-runner-1, Zot, homelab manifest bump) | `README.md:23-28,51-53,150-151,166-174` | docs | **Yes** | README rewrite (G.4) |
| Test fixtures with real-lab identifiers: `grafana.home.techdufus.com` | `internal/upstream/clients_test.go:247-248,262` | domain | **Yes** | Use `example.com` (G.2) |
| `proxmox.home.techdufus.com`, `10.0.20.20:6443` | `internal/upstream/transport_test.go:27-28,41` | domain / IP | **Yes** | Use `example.com` / `192.0.2.x` (RFC 5737) (G.2) |
| `pve-730`, `talos-cp-1`, `10.0.20.20`, `E5-2683 v4`, `homelab;talos` tags, `fast-zfs`, real MACs | `clients_test.go:34,47,113,138,289-294,302,310,330-337,362-366` | node / IP / hardware | **Yes** | Neutral fixtures (G.2) |
| Token-shaped fixture `csi@pve!homelab=secret-value` (fake, but has the shape of a real token ID) | `clients_test.go:294,318` | token-shaped | **Yes** | Use `dash@pve!test=00000000-0000-0000-0000-000000000000`-style fixtures (G.2) |
| `createdBy: "techdufus"` | `clients_test.go:184,200` | user | **Yes** | `alice` (G.2) |
| `pve-730`, `10.0.20.20:9100` | `internal/queries/queries_test.go:10-29`; `internal/api/api_test.go:107,283`; `internal/api/silences_test.go:15` | node / IP | **Yes** | Neutral fixtures (G.2) |
| Tenant `yahwai`; `omp-cloud`, `talos-cp-*` VMs | `internal/topics/attention_test.go:110,121-124,131,254-257,355,383-384` | tenant / VM names | **Yes** | Neutral fixtures (G.2) |
| `talos-cp-1`, `rustfs/nas-backup` | `internal/links/links_test.go:29-31,65-66` | names | **Yes** | Neutral fixtures (G.2) |
| `.dockerignore` lacks `dev/`, so `COPY . .` (`Dockerfile:14`) brings `dev/secrets.env` and any local `dev/site.env` into the build stage. They do not reach the final image (only `/out/dash` is copied), but they do sit in the build cache | `.dockerignore:1-4` | build hygiene | **Yes** | Add `dev/` and `.github/` (D.5) |
| No `LICENSE`, `SECURITY.md`, `CONTRIBUTING.md` or `CODE_OF_CONDUCT.md` | repo root | OSS hygiene | **Yes** | G.6, G.7 |

The demo fixtures are already clean. `internal/demo/demo_test.go:44` fails if demo topics contain `techdufus`, `home.`, `10.0.` or `192.168.`. G.2 extends that idea into a repo-wide denylist test.

### 1.2 Coupling findings that shape the design

- **No cluster means no process.** `kube.LoadConfig` falls back to `rest.InClusterConfig()` (`internal/kube/kube.go:62-74`), and `main.go:59-62` returns the error. Today the only way to run without Kubernetes is demo mode.
- **A missing CRD blocks readiness [INFERENCE, not reproduced].** Dynamic informers are always registered for Flux, CNPG, cert-manager, External Secrets and Gateway API HTTPRoutes (`kube.go:154-164`). `HasSynced` needs every informer synced (`kube.go:203-211`). On a cluster without one of these CRDs, the reflector's list fails, so `/readyz` stays 503 and `cluster`, `flux`, `vms`, `storage`, `apps` and `overview` never publish (`manager.go:321-365`). The `kubernetes` plugin must gate informers on API discovery (F.8).
- **The `.svc` service-proxy transport is dev-only.** `upstream.DevTransport` (`internal/upstream/transport.go:15-29`) is wired only when `cfg.Dev` is set (`main.go:70-74`). In production, upstream requests use plain HTTP to cluster DNS. Out-of-cluster deployments (Docker, PaaS) need URLs they can actually reach.
- **As shipped (G3 PR-1, v0.7.0):** `upstream.DevTransport` is deleted. Its replacement is `upstream.DevWrap(rc)`, which wraps a plugin's own transport instead of replacing it: only `<svc>.<ns>.svc[:port]` requests go through the API server's service proxy, and every other request keeps the plugin's CA, credentials and dial guard. It is applied through `plugin.Options.Transport` (A.3), only with `DASH_DEV`, and is built in `cmd/dash/main.go` until the kubernetes plugin takes it over (P3.8).
- **As shipped (G3 PR-3a, v0.9.0):** the kubernetes plugin takes over the three findings above. dash starts with no cluster (the kubernetes and flux rows are `unavailable`, Proxmox and the rest serve, `/readyz` is 200). The missing-CRD wedge was reproduced in a scratch copy (it is no longer an inference) and fixed by the discovery gate and the bounded typed sync (A.3). `DevWrap` is built by the plugin, not by `cmd/dash/main.go`, and is switched by `plugins.kubernetes.upstreamViaAPIServer` (resolved per request), not by `DASH_DEV` (A.3).
- **Auth is only as safe as the listener.** `auth` trusts `X-Forwarded-Preferred-Username` or `X-Forwarded-User` from any peer (`internal/api/middleware.go:32-51`). It is safe only because `DASH_LISTEN` defaults to loopback (`config.go:97`) and oauth2-proxy is the only thing on that loopback. Setting `DASH_LISTEN=0.0.0.0:8080` without a proxy means anyone can impersonate any user with one header. CSRF requires `X-Dash-Csrf: 1` and `Origin == PublicOrigin()` on every non-GET request (`middleware.go:18-29`); keep that.
- **Upstream error text goes to the browser.** `setSource` stores `err.Error()` (truncated to 300 chars) in `SourceStatus.Error` (`manager.go:189-199`), and that is published to every SSE client. Any future upstream that echoes credentials in error bodies would leak them. Redaction must sit at this boundary (C.6).
- **Upstream clients cannot authenticate and have no TLS options.** `newClient` takes a base URL and a RoundTripper only (`internal/upstream/http.go:46-50`). Proxmox uses `http.DefaultTransport` (`main.go:79`), so a self-signed Proxmox certificate fails unless it is trusted by the host. Most homelab Proxmox installs use self-signed certificates. Each plugin needs `tls.caFile` / `tls.insecureSkipVerify` and auth headers (F).
- **Topics mix sources.** `host` mixes Proxmox and Prometheus (`internal/topics/host.go:15-51`). `vms` mixes Proxmox, Prometheus, Gatus and Kubernetes (`vms.go:22-105`). `storage` mixes Kubernetes, Prometheus, CNPG and RustFS (`storage.go:27-165`). `apps` mixes Kubernetes and Gatus (`feeds.go:99-150`). Plugins need a way to consume other plugins' services (A.5).
- **Series are named and allow-listed** (`queries.go:147-208`; `internal/api/read.go:15-55`). There is no free-form PromQL from the browser. Keep that property.

### 1.3 Git history

The repository has seven commits on all refs, and one author identity: a personal email address on every commit, which becomes public with the history. Counts are `git log --all -S<string> --oneline | wc -l`, i.e. commits that change the number of occurrences. An earlier count with `git log -G` used a different method, so small differences (5 vs 4 for `home.techdufus.com`) are expected.

| Identifier | Commits (pickaxe) |
|---|---|
| `techdufus` (any case-sensitive match) | 5 |
| `home.techdufus.com` | 4 |
| `rustfs` | 4 |
| `homelab.io/` | 3 |
| `pve-730`, `talos-cp`, `mac-mini`, `rpi-control` | 2 each |
| `10.0.20.`, `yahwai`, `omp-cloud`, `registry.home`, `github-runner-1`, `hl-op`, `csi@pve`, `E5-2683` | 1 each |

### 1.4 Secret scan

gitleaks 8.21.2 was run over the full history of all refs (7 commits) on 2026-10-31: 0 findings. trufflehog has not been run. `dev/secrets.env` holds a reference, not a value. Recommendation: add gitleaks and trufflehog (`--only-verified` plus a full-history run) to CI before the flip (H.2), and run both once more over all refs immediately before changing visibility (G.10).

---

## A. Plugin interface

### A.1 Registration: in-process, explicit list (decided, J.2)

| Option | How | Pros | Cons |
|---|---|---|---|
| **In-process, explicit list** (decided) | `cmd/dash/plugins.go` lists factories; each plugin is a Go package | One static binary that stays distroless-friendly; type-safe; no IPC; one test suite; no blank imports | A new plugin means a new build. Third parties fork or build their own image (an xcaddy-style `go build` with an extra import list after the API is public) |
| In-process, `init()` self-registration | `import _ ".../plugins/proxmox"` | Familiar database/sql style | Hidden ordering; dead plugins are hard to spot; no gain over an explicit list |
| Out-of-process HTTP/gRPC sidecar | The plugin serves a manifest plus topics; dash proxies | Any language; isolation; independent releases | A versioned wire protocol, auth between processes, and more deployment pieces. Needs a stable data contract first |
| hashicorp/go-plugin (subprocess, gRPC) | dash spawns plugin binaries | Isolation | Many binaries in the image; awkward with distroless and read-only filesystems |
| WASM (e.g. wazero) | Plugins as `.wasm` | Sandboxed | Network, TLS and client-go inside WASM are immature; large effort |

Decision J.2 (decided): in-process now; out-of-process is rejected for v1. Keep the interface transport-agnostic: topic payloads are JSON-serialisable structs, and no Go handles cross the boundary except through `Host.Service`. A "remote plugin" adapter can then be added later without changing the core. Keep the package at `internal/plugin` until the API is stable at 1.0, then promote it to `pkg/plugin` with semver guarantees. That move is what makes third-party builds possible.

```go
// cmd/dash/plugins.go: the only list of built-in plugins. Order is display order.
var builtin = []plugin.Factory{
	prometheus.New, alertmanager.New, gatus.New, victorialogs.New,
	proxmox.New, hardware.New,
	kubernetes.New, flux.New, cnpg.New, rustfs.New,
	grafana.New, headlamp.New, launcher.New,
}
```

### A.2 Manifest, ID and version

```go
// Package plugin is the contract between dash's core and its integrations.
package plugin

// ID is [a-z][a-z0-9]{1,31}: no '_' or '-', so DASH_PLUGINS_<ID>_<FIELD> parses unambiguously.
// "core" is reserved for the core.* topics and is never a plugin.
type ID string

type Factory func() Plugin

type Manifest struct {
	ID          ID
	Name        string // "Proxmox VE"
	Description string
	Version     string // semver of this plugin's topic/API contract (not the dash version)
	Requires    []ID   // hard: flux requires kubernetes; without it the plugin is "blocked"
	Uses        []ID   // soft: proxmox uses kubernetes to mark VMs that are cluster nodes
	Config      func() any // pointer to a zero config struct; the JSON Schema is reflected from it
	Topics      []TopicSpec
	Actions     []ActionSpec
	Series      []SeriesSpec // named range queries served by /api/v1/series (needs prometheus)
	UI          UISpec       // nav entries, routes and panel slots the SPA module provides
}

type TopicSpec struct {
	Name   string // "vms" is published as SSE event "proxmox.vms"
	Doc    string
	Type   any    // zero value of the payload: Host.Publish drops another type, and OpenAPI types the envelope per topic (clients-and-ai.md L.3)
	Legacy string // pre-namespace SSE name ("vms"); set only by the G2 legacy shims, deleted by the leaf that migrates the plugin (A.7)
}

type ActionSpec struct {
	Name        string    // "restart" -> POST /api/v1/plugins/kubernetes/actions/restart
	Title       string
	MinRole     auth.Role // auth.RoleAdmin unless stated
	Confirm     Confirm   // ConfirmNone | ConfirmDialog | ConfirmTypeName
	Destructive bool
	Idempotent  bool                                           // repeating with the same input is safe; becomes the MCP idempotentHint
	Input       func() any // zero request struct; schema reflected; body capped like decodeBody today
	Summary     func(input any) string                         // one line for approvals and audit, e.g. "Restart deployment media/app-0"
	Target      func(input any) (kind, namespace, name string) // what the action touches; drives deny namespaces, typed confirmation and approvals
}

type SeriesSpec struct {
	Name string                               // "vm.cpu"
	Expr func(a SeriesArgs) (string, error)   // validated args only, as queries.Series does today
}
```

`Destructive`, `Idempotent` and `Input` also define the MCP tool for the action (clients-and-ai.md N.2).

As implemented (G2, `internal/plugin/plugin.go`): `SeriesArgs` is an alias of `queries.Args`; `UISpec` is `{Nav []model.NavItem}` (generic views arrive with P2.8); `Supervisor.Add` fills an action's empty `MinRole` with `admin` and its empty `Confirm` with `none`, and rejects an invalid or duplicate ID or topic name, a duplicate series name, a `Requires`/`Uses` cycle, and a second instance of one type.

Config is keyed by instance name. `plugins.<instance>.type` defaults to the instance name, so `plugins.proxmox: {...}` just works, and a second instance can later be `plugins.pvelab2: {type: proxmox, ...}`. Instance names follow the ID rule above, and topics, routes and env names use the instance name. v1 enforces one instance per type (decided, J.16), but nothing in the schema or the env naming has to change to lift that limit. In G2 the section map is still a closed struct (`config.PluginsConfig`, `internal/config/tree.go`): each of the 11 sections embeds `config.PluginBase` (`internal/config/plugins.go`), which adds the leaves `plugins.<id>.type` and `plugins.<id>.enabled` (a `*bool`; unset means auto), and `validate()` rejects a `type` that differs from the section name. `Tree.PluginSections()` hands the sections to the supervisor through the `Settings` interface that `cmd/dash/plugins.go` implements. `internal/plugin` and the plugin packages import neither `internal/config` nor `internal/api`.

### A.3 Lifecycle and optional capabilities

```go
type Plugin interface {
	Manifest() Manifest
	// Start receives a config already validated against the schema and Validate. It builds
	// clients and starts pollers or informers, and must not block. An error means the instance
	// cannot run at all (unreadable CA file); upstream outages are reported via Status, not here.
	Start(ctx context.Context, h Host, cfg any) error
	// Stop ends everything Start launched, within ctx's deadline.
	Stop(ctx context.Context) error
	Status() Status
}

// Optional interfaces, detected with type assertions. These are the ones the core calls.
type Validator interface{ Validate(cfg any) error }   // cross-field rules; a failure makes the instance "unconfigured"
type Configured interface{ Configured(cfg any) bool } // auto-enable: enabled unset + false -> "disabled"; enabled true + false -> "unconfigured"
type Provider interface{ Service() any }              // what Host.Service(id) returns to a plugin that declares it in Requires/Uses (A.5)
type RestartOnly interface{ RestartOnly() }           // cannot be stopped and started in-process: the G2 legacy shims, until G3
type Actor interface {
	Act(ctx context.Context, action string, p auth.Principal, input any) (any, error) // auth.Principal: clients-and-ai.md M.1
}
// ActionError is a deliberate refusal from Act; status and code reach the client as written. Any other error is a 502 with the upstream category.
type ActionError struct {
	Status  int
	Code    string // a model.ErrCode*
	Message string
	Details any
}
type Router interface{ Routes(r Routes) } // GET-only, mounted under /api/v1/plugins/{id}/
type Routes interface {
	Get(pattern string, h http.HandlerFunc, spec RouteSpec) // every route carries its OpenAPI description (clients-and-ai.md L.3)
}
type AttentionSource interface{ Attention(now time.Time) []model.AttentionItem }
type KPISource interface{ KPIs() []model.KPI }
type TopologySource interface{ Topology() model.Topology } // merged into core.overview (A.7)

// Host is what the core gives a running plugin.
type Host interface {
	Publish(topic string, data any, sources []model.SourceStatus) // namespaced by the core; an undeclared topic or a payload of the wrong type is logged and dropped
	Logger() *slog.Logger                                          // slog.Default().With("plugin", instance): behind the redacting handler (C.6)
	HTTPClient(c HTTPConfig) (*http.Client, error)                 // TLS/CA, per-request credentials, timeout; dial guards below
	Service(id ID) (any, bool)                                     // see A.5
	Now() time.Time
	Go(name string, fn func())                                     // a goroutine that recovers panics: the instance becomes "error"
}

// HTTPConfig is how a plugin wants its upstream client built.
type HTTPConfig struct {
	Timeout            time.Duration       // 0 = 25s
	CAFile             string              // extra root CAs (PEM), added to the system roots
	InsecureSkipVerify bool                // logs a startup WARN (F.6)
	Authorize          func(*http.Request) // sets credentials per request, so a rotated secret applies at the next request
	FromUI             bool                // the URL came from the UI layer: loopback dials are refused too
}
```

Three interfaces of the first draft are not in G2 and arrive, additively, with their first caller: `Checker` (`Check(ctx, cfg) error`, the settings "Test connection", P4.7), `Demo` (`Demo() Plugin`, the fixture-backed twin for `DASH_DEMO=true`, the first G3 leaf), and `Objects` (search, get and context by ref, clients-and-ai.md N.4, P7.3). `Host.Links() links.Registry` is also absent: `links.Registry` (F.2) does not exist yet and `links.Builder` imports `config`; the first G3 consumer adds it. `Host.Go` was added because a panic in a plugin's own goroutine cannot be recovered from outside, and "a panic in a poller gives `error`" needs a launcher the core controls. `Router` takes a typed registrar instead of a `*http.ServeMux` so the OpenAPI generator and the drift test can enumerate every route; a mux cannot be listed.

**As shipped (G3 PR-1, v0.7.0).** `Demo` and `Host.Links()` arrived with the first G3 leaves, `Host.Links()` in another form; `Checker` and `Objects` are still absent. With them came these additions:

- `Demo` is `interface{ Demo() Plugin }`, with `Options.Demo`. In demo mode the supervisor runs the twin of every plugin that has one, treats it as configured and mounts its actions, and leaves a plugin without a twin `disabled` ("no demo data"), however it is configured. A twin runs unless the section says `enabled: false` explicitly (an unset `enabled` counts as on), so demo mode still shows a page appearing and disappearing when you switch its plugin off. Twins live in `internal/plugins/<id>/demo.go` and build their payloads from the shared fixtures in `internal/demo/world`; the legacy shims stay as demo stand-ins through `internal/demo/plugin.go` until their leaf.
- `Host.Links()` returns a `plugin.LinkResolver` (`Resolve(LinkRequest) []model.Link`), not `links.Registry`. It collects the links of the running `plugin.LinkProvider`s (`Links(LinkRequest) []model.Link`: grafana and headlamp). A request names a Kubernetes kind or one of the dash-wide kinds `Alerts` and `Host`, and a link without a kind gets the provider's ID. Core code asks `Supervisor.Links()`, once per rebuild. Proxmox links stay with `links.Builder` until P3.6.
- `Supervisor.Service(id)` is the lookup for code that is not a plugin (the Manager, the API): the running instance, with no `Requires`/`Uses` check. `Host.Service` keeps the check.
- `Options.Transport func(id, rt) http.RoundTripper` wraps every client `Host.HTTPClient` builds, after the dial guard and the credentials, so the upstream metrics record the real request. `Options.ScrubLog` is what `Host.ScrubLog(s)` calls: it removes every secret the core knows (the registry's `ScrubLogLine`, which also covers `dash_setup_` values that `Scrub` does not).
- `Routes.Stream` and `RouteSpec.Namespace` (A.8), `plugin.Ref`/`ParseRef` and `plugintest.AssertRefs` (A.7), and `plugin.Outcome` (A.8).

`internal/plugin.Supervisor` supervises each instance the way `topics.Manager` supervises pollers today (`manager.go:141-176`): a panic in `Start`, `Status`, `Act` or a `Host.Go` goroutine is recovered and the instance becomes `error` without taking the process down. It evaluates an instance in a fixed order: `enabled: false` → `disabled`; a section `type` that is not the instance, or a config of the wrong type → `error`; `Configured` false → `disabled` (auto) or `unconfigured` (explicitly enabled); `Validator` failure → `unconfigured`; a `Requires` instance that is unknown, not started or `unavailable` → `blocked`; `Start` failing → `error`; otherwise the plugin's own `Status()`. It starts instances in dependency order over `Requires` and `Uses` (ties by registration order) and stops them in reverse. On a config change (`config.Reloader` emits one `Change` per `plugins.<id>` section; `ReloadOptions.Live` keeps a live-applied change out of the "restart required" list) the changed instance and its dependents go through Stop (at most 10s) then Start. `RestartOnly` instances are never touched. Whenever any instance's state, message, topics, actions or nav changes, the supervisor publishes `core.plugins`, and when an instance stops it removes its topics from the hub.

`Host.HTTPClient` guards every dial, so a settings value cannot turn dash into a request proxy: the guard inspects the resolved address at dial time and always refuses link-local addresses (`169.254.0.0/16` and `fe80::/10`) and the cloud metadata address `fd00:ec2::254`, and it refuses loopback and the unspecified address when `HTTPConfig.FromUI` is set (clients-and-ai.md K.22). Redirects are re-checked and may not change host, and the client ignores `HTTP_PROXY` because a proxy would hide the real destination from the guard. `InsecureSkipVerify` logs a startup WARN; the "tls: verification disabled" source note comes with the first G3 plugin that builds a real client.

**As shipped (G3 PR-2, v0.8.0).** Four framework seams arrived with the scaffold commit, before the two leaves that need them, and `Proxmox links stay with links.Builder until P3.6` above no longer holds: `internal/links` is deleted.

- `LinkRequest.ID` is the identity where a name does not identify the object (a VMID), and the dash-wide kinds now include `VM` (`ID` is the VMID, `Name` the VM name), `HostShell` and `ProxmoxUI`. The proxmox plugin is the `LinkProvider` for them; the hardware plugin asks `Host.Links()` for `ProxmoxUI`, `HostShell` and the Grafana `Host` kind when it builds the host's links (between the two leaves, the Manager's host builder asked `Supervisor.Links()` the same way).
- `Supervisor.Series` reads the **running instance's** `Manifest`, so a `SeriesSpec.Expr` that closes over its plugin's settings follows a reload and the demo twin (a series that was read from the registered manifest followed neither). The proxmox and hardware closures read their settings from an `atomic.Pointer` set in `Start` and return `queries.ErrBadSeries` before it, and a series whose plugin is not running is an unknown name: 400 `invalid_input`.
- `plugin.FirstSeen` (`Stamp(now, items)`; the zero value is ready) gives attention items that have no timestamp of their own (a stopped VM, a failing disk, a hot CPU) a `Since` that is the first time the item was seen with that ID, kept while it keeps appearing and restarted when it goes away and comes back; an item that arrives with a `Since` keeps it. Both leaves stamp their list on every call to `Attention`, so `Since` is stable across calls (the Manager's `Tracker` rule, now a plugin's).
- `plugin.TopicName(id, spec)` is the one rule for a topic's wire name (A.7), so the Manager's owner map and the supervisor agree.

The "tls: verification disabled" source note that the paragraph above promises is the plugin's, not the host's: `Host.HTTPClient` logs the WARN and the plugin adds `{Name: "tls: verification disabled", OK: true}` to the sources of its `Status()`. The proxmox plugin is the first to do so; the note shows in `core.overview.sources` and draws no chip, because chips are drawn for sources that are not ok only.

**As shipped (G3 PR-3a, v0.9.0).** Three framework seams arrived with the scaffold-lite commit and the kubernetes leaf, and the kubernetes plugin is the first to need the informer gate described last.

- **`plugin.ErrNotFound` and `plugin.NotFound(msg)`.** The scaffold-lite commit added `plugin.ErrNotFound`: a `SeriesSpec.Expr` wraps it for a resource that does not exist and the series handler answers 404 `not_found` with its text, while any other `Expr` error is 400 `invalid_input`. The integration added `plugin.NotFound(msg)`, whose `Error()` is the message alone (it is the 404 body, so it carries no `not_found:` prefix) and for which `errors.Is(err, plugin.ErrNotFound)` holds, wrapped or not; the kubernetes series return `plugin.NotFound("unknown node")`, and `TestSeriesUnknownNode`, the plugin series test and the API plugin tests expect the bare text. Argument validation still comes first, so **a malformed `node=` is 400 before any lookup and an unknown one is 404 `not_found` "unknown node"**, as at v0.8.0, when the API resolved the node's IP itself. That call is gone: the handler passes the node name in `queries.Args.Instance`, the plugin resolves it to `<ip>:9100` through its own node cache, `queries.Series` has no cases left (it always returns `ErrBadSeries`) and `NodeExporterPort` moved into the plugin.
- **The transport hook is resolved per request.** `plugin.Options.Transport` still wraps a plugin's own transport (the PR-1 note above). The switch is now `plugins.kubernetes.upstreamViaAPIServer` (default false), not `DASH_DEV`: `cmd/dash` resolves `Supervisor.Service("kubernetes")` on **every request** and calls `APIServerTransport()` on the returned service value, because the other plugins build their clients before the kubernetes plugin starts. The method returns nil while the plugin is unavailable, in `error` or has the flag off, and the request then takes the plain transport.
- **`internal/kube` is independent of `internal/config`.** `kube.LoadConfig(mode, kubeconfig, context, qps, burst)` and the connection code in `internal/plugins/kubernetes/connect.go` replace the old `LoadConfig(cfg)`; `kubernetes.Factory(refs)` is the production constructor, `kubernetes.FactoryWithConnect(refs, connect)` the one tests use with client-go fakes (so no test needs a cluster) and `kubernetes.New()` is `Factory(nil)()`, which manifests and routes use. `cmd/dash` lost its two fatal paths (no cluster, no client), so dash starts with no cluster (A.5).

**The informer-gate recipe (R4-2, observed and fixed in a scratch copy before the plan relied on it).** Before P3.8 every dynamic informer (Flux, CNPG, cert-manager, external-secrets, Gateway API) was registered unconditionally, so one CRD the cluster does not serve kept `HasSynced` false for ever (`WaitForSync(5 s)` false, the reflector retrying at about 0, 1 and 4 s), `/readyz` stayed 503 and no topic published. What shipped, in `internal/kube` and the plugin:

1. **A discovery gate.** `Cluster.Discover(wanted)` makes one `ServerGroupsAndResources` call at `Start`, every 5 minutes, and at once when a consumer asks for a new GVR through `Watch` (coalesced). A partial failure (`ErrGroupDiscoveryFailed`: an aggregated API that is down) leaves the healthy groups present and the failed group neither present nor absent; a total failure keeps the previous state, counts every wanted resource as listed (so nothing waits for ever) and is logged.
2. **Only typed informers gate readiness.** The 11 typed informers register before `Start` and gate `HasSynced`; a dynamic informer is started with `AddDynamic(gvr, kind)` **after `Start`**, only for a GVR that discovery found served, and never gates it.
3. **A bounded typed sync.** If the typed informers have not all synced 60 s after `Start` (`typedSyncTimeout`, measured on `Host.Now`) the row becomes **`error` with a category** (`auth` for a forbidden list, `unreachable` or `timeout`, from the informers' last watch error) and the service is withdrawn (A.5), where before it stayed `starting` for ever and held Proxmox's VMs back for ever. It leaves `error` by itself when the sync completes, and the API-server probe's `unavailable` takes precedence.
4. **Wanted and optional groups.** `ErrGroupDiscoveryFailed` lists every failing group; the row degrades only for a **wanted** one: the three optional CRD groups unless `optional.*` opts one out, Ingress when `apps.ingress`, and, for this release, the CNPG group that the transitional storage extras read (a broken group degrades, an absent one is not a note). Any other failing group, such as a down metrics-server `APIService`, is logged at INFO once per change, scrubbed, and affects nothing (`TestUnwantedGroupDiscoveryFailureDoesNotDegrade`).
5. **Per-GVR `Listed` publish gates.** A dynamic informer never gates `Synced()`, so each builder waits for its own inputs: `kubernetes.cluster` publishes once `Synced()` and `Listed` is true for certificates and external secrets; `kubernetes.storage` once `Synced()`, `Listed` for the CNPG GVR and the first storage poll have ended; `kubernetes.apps` once `Synced()`, `Listed` for HTTPRoutes (and Ingresses when enabled) and the Gatus gate (`EndpointHealth.Revision() > 0`, or the stub when Gatus is absent) have passed. `Listed(gvr)` is true after the first list, or when discovery says the resource is absent, or when discovery failed for its group (that GVR publishes empty; a wanted one adds the degraded note).
6. **A served resource that the client may not list.** An Ingress is served on every cluster but needs RBAC the default role lacks: its informer never lists. The apps topic waits for it for the sync timeout, then publishes without it, degrades the row and carries the refusal category. This needed a new `kube.Cluster.WatchError(gvr)` (the informer's last watch error as a note on the plugin, not a failure of the whole plugin) and goes beyond the contract.

A CRD that disappears keeps failing in its informer: a shared dynamic factory cannot stop one informer, so its reflector retries with backoff until the plugin restarts, and client-go logs each failed list (accepted). `Has` turns false at the next discovery and `List` answers nil.

### A.4 Config schema and validation

Each plugin's config is a Go struct. The schema (JSON Schema 2020-12) is reflected from struct tags, using either a small in-house reflector or `invopop/jsonschema` (decide in P2.2). The schema drives four things: validation, the settings form, `dash config schema`, and the generated env-var reference in the docs.

```go
type Config struct {
	Enabled     *bool        `json:"enabled,omitempty" doc:"Default: on when url and token are set"`
	URL         string       `json:"url" dash:"required,format=uri" doc:"e.g. https://pve.example.com:8006"`
	Node        string       `json:"node" dash:"required"`
	TokenID     string       `json:"tokenID" dash:"required" doc:"user@realm!tokenname"`
	TokenSecret secret.Value `json:"tokenSecret" dash:"required,secret"`
	TLS         TLSConfig    `json:"tls"`
	CriticalVMs CriticalVMs  `json:"criticalVMs" doc:"VMs whose stop is critical, not a warning"`
}
```

- `secret` emits standard JSON Schema `writeOnly: true` plus `x-dash-secret: true`. Settings forms render a write-only control for it (C.2).
- `restart` (`x-dash-restart`) marks fields that need a process restart: `server.listen`, `server.healthListen`, `auth.mode`, `dataDir`.
- Validation runs in this order: schema (types, required, enum, format), then `Validator`, then (UI only, on demand) `Checker`. Errors carry the field path and source, e.g. `plugins.proxmox.tokenSecret (env DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE): file not readable`.
- Unknown keys in the config file are errors (the strict decoding that `LoadLauncher` already uses at `config.go:166`).
- **As shipped (G3 PR-1, v0.7.0): `internal/pluginconf`.** The config structs live in one package, one file per plugin (`prometheus`, `alertmanager`, `gatus`, `victorialogs`, `grafana`, `headlamp`, `launcher`, plus `ui.go` for the top-level `ui` section), not in each plugin package and not in `internal/config`. Reason: `internal/kube` imports `internal/config`, so if `config.Tree` embedded a struct from `internal/plugins/kubernetes`, which imports `internal/kube`, the build would have an import cycle. `pluginconf` imports only `internal/secret` and `internal/model`; `internal/config` (the tree, validation, schema and env table) imports `pluginconf`; a plugin imports `pluginconf` and never `internal/config`. `PluginBase` (`type`, `enabled`), `Duration`, the shared types (`TLS`, `BasicAuth`, `Auth`, `Actions`, `ActionsToggle`) and the launcher loader (`LoadLauncher`) moved there, because `config` imports `pluginconf` and not the reverse. A section that has not migrated yet stays in `config` and embeds `pluginconf.PluginBase`. A section implements the optional methods the rest of dash looks for: `Validate() error` (cross-field rules; the `FieldError`s carry paths the loader prefixes and attributes to a layer), `ActionsConfig() Actions` and `ReadDenyConfig() []string` (A.8). The JSON and dash tags of an existing field never change, because they are the environment names. A plugin's own `Validate` (the `plugin.Validator`) calls the section's, so a configuration that reaches the supervisor another way is checked by the same rules.
- **As shipped (G3 PR-2, v0.8.0): `pluginconf.Proxmox` and `pluginconf.Hardware`.** The scaffold commit retyped the tree with structs that carry today's fields and tags only and took over the rules of `config/validate.go` (same paths and messages), so it adds no key; each key arrives in the leaf that reads it, with its `json` and `dash` tags, a default in `defaults()` where it has one, a `Validate()` rule where a value can be wrong, and a test that runs the real env layer into `treeSettings.Instances()` and the running plugin (`TestPR2SettingsReachTheProxmoxPlugin`, `TestPR2SettingsReachThePlugin`, with a guard that fails when a leaf has no row). `config.defaults()` calls `pluginconf.DefaultProxmox()` (so the reference shows `api` and `id="qemu/{vmid}"`) and `DefaultHardware()` (which `Validate` needs, F.7). The v0.8.0 deployment's environment is one shared file, `internal/config/testdata/deployment-v0.8.0.env`, read by `TestDeploymentEnvV08` (the resolved tree) and by the `cmd/dash` test that runs the real plugins on it.
- **As shipped (G3 PR-3a, v0.9.0): `pluginconf.Kubernetes`.** The scaffold-lite commit retyped the tree as PR-2's did: `KubernetesConfig` left `internal/config` and `KubernetesConventions` and `BackupJob` moved into `pluginconf.Kubernetes`, which carried today's fields and tags only and took over the `backupJobs` rules of `config/validate.go` (same paths and messages); the flat `Kubeconfig` and every flat `Conventions` field except the suspend-reason annotation are deleted with P3.8. The keys arrived in the leaves, each with its `json` and `dash` tags, a `doc` tag, a default in `config.defaults()` (`pluginconf.DefaultKubernetes()`: mode `auto`, qps 20, burst 40, `etcd.job` `etcd`, `apps.annotationPrefix` `gethomepage.dev`) and a `Validate()` rule where a value can be wrong: P3.8 `mode`, `context`, `qps`, `burst`, `etcd.job`, `optional.{certificates,externalSecrets,httpRoutes}`, `upstreamViaAPIServer`, `actions.{enabled,denyNamespaces}` and `backupJobs[].label` with up to eight entries; P3.9 `apps.{gateways,excludeNamespaces,annotationPrefix,ingress}`. P3.10 adds no key: it adds an accepted reference form to every secret field (C.3). No existing env name or config path changed. **A zero `qps` or `burst` is "use the default", not invalid** (a deviation: `EffectiveQPS()` and `EffectiveBurst()` read it, and `burst` below `qps` is the error). Every key has a row in `TestPR3aSettingsReachThePlugin` (`cmd/dash`: the real env layer into the started plugin, a reload of the file layer restarts the instance, and a guard fails when a key of `config.EnvVars()` has no row), and the v0.8.0 and v0.9.0 deployment environments are two shared files read by `TestDeploymentEnvV09` (`internal/config`, the resolved tree) and `TestDeploymentEnvReachesThePlugins` (`cmd/dash`, both files; `actions.enabled` is false for the first and true for the second). The `enabled` doc text, the README row and both env table footers now name kubernetes as a live plugin and the pairs that restart together (prometheus restarts proxmox, hardware and kubernetes; gatus restarts hardware and kubernetes; proxmox restarts hardware; kubernetes restarts proxmox; grafana restarts alertmanager), derived from the manifests by `TestEnabledDocNamesTheUsers`; `flux` is the one shim left ("flux has not moved to a plugin of its own yet").

### A.5 Cross-plugin services

Some plugins provide services instead of, or in addition to, topics. Consumers declare `Requires` or `Uses` and look the service up through `Host.Service(id)`, which returns a narrow interface:

| Provider | Service | Consumers |
|---|---|---|
| `prometheus` | `Querier{Instant, Range}` (today `upstream.Prom`, `internal/upstream/prom.go:61-100`) | hardware, kubernetes (pod and node usage, PVC stats, etcd), proxmox (VM series), cnpg, rustfs |
| `kubernetes` | `Cluster` (listers from `kube.go:78-104`), `NodeIndex`, `SecretResolver` | flux, cnpg, proxmox (VM to node), apps discovery, `k8s:` secret refs |
| `gatus` | `EndpointHealth(host) (bool, bool)` | kubernetes apps (health dot), hardware machines |
| `victorialogs` | `Logs{Query, Tail}` (today `api.VLogsAPI`, `internal/api/api.go:33-37`) | kubernetes pod sheet |
| `headlamp`, `grafana`, `proxmox` | link providers (`links.Registry`) | every builder that attaches `[]model.Link` |

**As shipped (G3 PR-1, v0.7.0):** the interfaces live in `internal/svc`, a neutral package, so a consumer never imports its producer; the provider returns one from `plugin.Provider.Service()`. Two exist: `svc.Querier{Instant, Range}` (prometheus; `svc.Sample{Labels, Value}`, which `upstream.Sample` aliases) and `svc.EndpointHealth` (gatus), which is `ByHost(host) (model.Health, bool)`, `ByName(name) (up, known bool)` (case-insensitive), `Revision() uint64` and `SourceStatuses() []model.SourceStatus`. `Revision` is 0 until the first poll has finished (a failed first poll counts), then increases whenever the endpoint list, a health value or the source status changed, and it comes from a process-global counter so a service rebuilt by a reload never reports a value lower than its predecessor's. This replaces a callback: a callback registered once would not survive `Supervisor.Reload`, which builds a new service. A consumer re-reads the service on its own tick, compares the service pointer and `Revision`, republishes when either changed, and appends `SourceStatuses()` to its envelope (the Manager does this for apps and VMs, at the top of `rebuild`, before its early return). Core code reaches a service through `Supervisor.Service(id)` (`topics.Deps.Services`), and the Manager's accessors `m.querier()` and `m.endpoints()` return a stub while the provider is not running: `upstream.ErrDisabled` for the querier, and for the endpoints a stub whose `SourceStatuses()` reads `gatus: not_configured`. `Logs{Query, Tail}` is **not** built in G3: it waits for its first Go consumer (P7.3), because the pod sheet uses the HTTP routes. `gatus.EndpointHealth(group, healthy, critical)` is exported so the later consumers apply the same severity rule. `SecretResolver` arrives with P3.10, and `NodeIndex` and `Cluster` with the kubernetes plugin.

**As shipped (G3 PR-2, v0.8.0).** Two more interfaces in `internal/svc`. `svc.NodeName{NodeName() (name string, ok bool)}` is what the proxmox plugin exports: the node it reads, for the hardware plugin. The service is non-nil from the plugin's `Start`, so `ok` is the only signal: it is true from `Start` when `plugins.proxmox.node` is configured, and only an unset node waits for discovery (F.6). `svc.NodeIndex{K8sNode(vmName) (node, ready, ok), NodeIP(node), Synced(), Revision()}` is what the kubernetes plugin will export; until P3.8 **the topic Manager is its provider**, through the kubernetes shim (`topics.Legacy.Service`), so the proxmox plugin needs no change when the real plugin replaces the shim. `K8sNode` answers `ok` false for every name until `Synced()` is true. `Revision` changes when the node set or a node's readiness changes and never repeats for the life of the process; `Synced` turning true is a change a consumer watches separately, because it comes with no node event (the proxmox plugin polls both, once a second). Hardware reads three services and requires none of them: `prometheus` (`Querier`), `proxmox` (`NodeName`) and `gatus` (`EndpointHealth`, for gatus machines).

**As shipped (G3 PR-3a, v0.9.0).** The kubernetes plugin is the provider of `svc.NodeIndex`, and the Manager no longer implements it (the kubernetes shim's `Service` is deleted; the only shim left is flux's). It exports **one object that is both `svc.NodeIndex` and `svc.Cluster`**, with a compile-time guard in `internal/plugins/kubernetes/service.go` and a fake asserted against `svc.Cluster` in `internal/svc/svc_test.go`. The `SecretResolver` of the table above is not built (C.3 says what carries `k8s:` references instead), and `topics.Deps.Services`, which the paragraph above names as the way core code reaches a service, is deleted because nothing reads it any more (core code uses `Supervisor.Service`, and the Manager gets the cluster through `Deps.Cluster`).

- **`svc.Cluster` is `Synced`, `Has`, `Listed`, `Watch`, `List`, `GVRRevision`, `Patch`.** `Synced()` is the readiness gate of the **typed** informers only; a dynamic informer never gates it. `Has(gvr)` is true when the last successful discovery listed the resource. `Listed(gvr)` is true after the first list of `gvr`, **or** when discovery says it is absent, **or** when discovery failed for its group, so a builder publishes once its own inputs are ready and never computes from a partial list. `Watch(gvrs...)` is idempotent and, for a GVR that is not yet watched, triggers an immediate discovery (the five Flux GVRs are not registered by the plugin: the consumer asks for them). `List(gvr)` answers the cached objects of every namespace, nil while the resource is not served or not watched. `Patch(ctx, gvr, ns, name, merge)` sends a JSON merge patch under the field manager `dash` and returns the API server's error unwrapped.
- **`GVRRevision`, not `Revision`.** D13 has one object implement both services, but `NodeIndex.Revision() uint64` and the scaffold's `Cluster.Revision(gvrs ...GVR) uint64` have different signatures, and no Go type can have two methods of one name, so the scaffold's `svc.Cluster` (`c3c2443`) could not have been implemented together with `NodeIndex`. The cluster's method was renamed `GVRRevision` in its own commit (`32dd3a8`), before anything implemented or consumed `svc.Cluster`; `NodeIndex.Revision` is unchanged, and the Manager's flux watcher calls `GVRRevision`. `GVRRevision(gvrs...)` is a per-GVR counter (all watched GVRs when none are given) bumped when an object is added, changed or deleted or when its CRD appears or goes, so the flux watcher moves only on flux changes. The flux shim reads the service through an accessor (`topics.Deps.Cluster`, a `func() (svc.Cluster, bool)` that answers nothing while the service is absent): it asks for the five Flux GVRs once per service identity, waits for the service, `Synced()` and `Listed` of all five, and marks itself dirty on a new identity, on `Synced` and on the `GVRRevision` or `Listed` state of the five.
- **`unavailable` withdraws the service.** While the row is `unavailable`, `error` or `disabled`, or before `Start`, `Service()` returns an untyped nil, which the supervisor reads as "no service". Proxmox withholds its VM publish while a node index exists and has not synced, so a cluster-less dash, or an API server that has been unreachable for 10 s, would otherwise keep the VMs page empty; with the service absent the VMs publish without the "is a Kubernetes node" attributes (`k8sNode` ok false), as they do when kubernetes is disabled, and they republish with them when the service returns. The flux shim is `unavailable` (hidden) without the service, `starting` until `Synced()` and `healthy` after; the legacy reconcile route answers 503. The debounce is 10 s (A.9, F.8); the withdrawal also applies to the `error` state of a typed sync that has not finished in 60 s (A.3).
- `internal/svc/svc.go` still says on `NodeIndex` that "until the kubernetes plugin exists the kubernetes shim of internal/topics supplies it": the file belongs to the scaffold and the comment was left as it was (it is listed in the epic's deviations).

The core starts plugins in dependency order. A plugin whose `Requires` provider is disabled or unavailable is `blocked`, with a message such as "requires kubernetes (unavailable: no cluster config)".

### A.6 Health and status

| State | Meaning | Shown as |
|---|---|---|
| `disabled` | `enabled: false`, or auto with nothing configured | Grey; pages hidden |
| `unavailable` | A runtime prerequisite is absent and that is not an error: no in-cluster config or kubeconfig, Flux or CNPG CRDs not installed | Grey with the reason; pages hidden |
| `unconfigured` | Enabled, but required fields are missing or invalid | Amber; link to settings |
| `blocked` | A hard dependency is not running | Amber; names the dependency |
| `starting` | `Start` returned and the first poll or sync has not finished | Spinner |
| `healthy` | All sources OK | Green |
| `degraded` | Some sources failing: queries failing, some informers unsynced, optional CRDs missing | Amber with source chips (the existing `SourceStatus` UI, `web/src/components/panel.tsx:27-30`) |
| `error` | `Start` failed, or the primary upstream is unreachable | Red |

- `Status.Sources` reuses `model.SourceStatus`. The per-topic source keys `"<scope>:<source>"` (`manager.go:188-242`) become per-plugin.
- `/healthz` is unchanged: the process is alive.
- `/readyz` means: config loaded, HTTP serving, and every enabled plugin has returned from `Start`. Plugin health never makes `/readyz` fail, which reverses today's `manager.go:104-105`. `/readyz?verbose=1` lists plugin states, but only to loopback peers (other peers get the plain status; clients-and-ai.md K.15), and the health listener stays unauthenticated and separate (`internal/api/health.go`). If someone wants today's semantics, they can set `server.readyRequires: [kubernetes]`, which waits for the informer sync, capped at `server.readyTimeout` (default 60s). Shipped in G2: `Supervisor.Ready` plus the serving flag the API sets once both listeners are bound (`cmd/dash/serve.go`); `readyRequires` entries must be known `plugins.<id>` sections, and a listed plugin that is disabled counts as settled. The health listener also serves `GET /metrics` when `server.metrics` is true (`internal/obs`: a private Prometheus registry with fixed label sets, `github.com/prometheus/client_golang`) and answers 404 otherwise.

### A.7 Data, topics and SSE

**Naming.** `<pluginID>.<topic>` for plugin topics, and `core.<topic>` for composed ones. The hub is already keyed by string (`hub.go:23-26`); only `model.Topic` and the fixed order in `hub.Subscribe` (`hub.go:113`) change. In G2 the order comes from `Hub.SetOrder` (the supervisor's `TopicOrder`: `core.plugins`, then `model.TopicOverview`, then each instance's topics in registration order).

| Today (`model.go:42-52`) | Owner after migration | New topic(s) | Notes |
|---|---|---|---|
| `overview` | core | `core.overview` | Composed from `KPISource` and `AttentionSource` contributions plus a topology fragment from proxmox and kubernetes. It no longer waits for all eight other topics (`manager.go:357-365`) |
| `host` | hardware (+ proxmox facts) | `hardware.hosts`, `proxmox.node` | Proxmox facts (version, kernel, uptime, CPU model) come from `proxmox`; metrics from `hardware` |
| `vms` | proxmox | `proxmox.vms` | VM-to-node cross-reference via `kubernetes` (uses). Machines move to `hardware.machines` |
| `cluster` | kubernetes | `kubernetes.cluster` | Usage overlay via `prometheus` (uses) |
| `flux` | flux | `flux.objects` | requires kubernetes |
| `storage` | split | `kubernetes.storage` (PVCs, etcd, backup CronJobs), `cnpg.clusters`, `rustfs.usage`, `proxmox.storage` | The Storage page becomes composite (A.8) |
| `alerts` | alertmanager | `alertmanager.alerts` | |
| `status` | gatus | `gatus.status` | |
| `apps` | kubernetes (+ launcher) | `kubernetes.apps` | Infra links come from the `launcher` plugin, as today from `/api/config` |
| (new) | core | `core.plugins` | Plugin states (A.6) pushed live, so the SPA hides or shows pages without a reload |

- `Envelope` (`model.go:69-75`) is unchanged: `{topic, updatedAt, sources, data}`. The topic string carries the plugin ID.
- **Cutover:** each migration PR renames its topic in Go and in `web/src/api/topics.ts` together; the compile-time guard at `topics.ts:29-34` enforces this. No alias topics. **As shipped in G2, only two topics changed:** `core.plugins` is new, and `overview` became `core.overview` (in the P2.5 commit, in Go and web together; it is core-composed and has no plugin leaf that could rename it). The other eight keep their names (`host`, `vms`, `cluster`, `flux`, `storage`, `alerts`, `status`, `apps`) and are renamed, with their payload splits, by the G3 leaf that migrates their plugin. Until then the legacy shims declare them with `TopicSpec.Legacy`, so `Host.Publish("vms", …)` emits SSE `vms`, `PluginInfo.Topics` lists `vms`, and removing the instance removes `vms`; the G3 leaf deletes `Legacy` and the topic becomes `<plugin>.<topic>`.
- **As shipped (G3 PR-1, v0.7.0).** The Go constants in `internal/model` are the registry of topic names. Rule: every published topic keeps a `model.Topic*` constant and a `TopicTypes()` entry, in the same commit as its web module key, its `shims.ts` row and its tests. Three guards tie the constants to the web: the generated `Topic` union must equal the keys of the SPA's declared topics (`_topicsExact` in `plugins/topic-data.ts`), `TestTopicsAgreeInGoAndWeb` requires the constants, the `TopicTypes()` keys and the module declarations to be the same set, and `PaletteCtx.topic` and `PanelContribution.reads` are typed by the union. Nothing generates a namespaced name, so a plugin topic without a constant fails `npm run check`. The manifest's `TopicSpec.Type` must be the type of the `TopicTypes()` entry, so the OpenAPI envelope is not duplicated. Three topic names changed in PR-1, with no alias: `alerts` is `alertmanager.alerts` (`TopicAlerts`), `status` is `gatus.status` (`TopicStatus`), and `launcher.links` is new (`TopicLauncherLinks`, payload `model.LauncherData`, F.2). The other names stay until their leaf. Topics of third-party plugins are outside this guard (a G5 concern). An operator or client that reads the old stream or snapshot names must switch.
- When a plugin is disabled, the core removes its latest events from the hub and publishes `core.plugins`. The SPA drops the corresponding pages.
- Per-plugin API: `GET /api/v1/plugins` returns `[{id, name, version, state, message, topics, actions, nav, configurable}]`. Plugin GET routes live under `/api/v1/plugins/{id}/...`: today's `/api/vms/{vmid}` becomes `/api/v1/plugins/proxmox/vms/{vmid}`, `/api/pods/{ns}/{name}` becomes `/api/v1/plugins/kubernetes/pods/{ns}/{name}`, and `/api/logs` becomes `/api/v1/plugins/victorialogs/logs`. `/api/v1/series` stays a core endpoint that dispatches to the `SeriesSpec` registered under the series name, so the browser can still only ask for named, validated series. The whole public API moves under `/api/v1` (clients-and-ai.md L.1, P2.10).
- **Snapshot GET.** `GET /api/v1/topics/{topic}` returns the Envelope with an `ETag` (the hub digest); `If-None-Match` gets 304 (clients-and-ai.md L.5).
- **Topic filter and user-scoped topics.** `GET /api/v1/stream?topics=a,b` sends only the listed topics. There is no resumption: a reconnect replays current state. User-scoped topics such as `core.approvals` reach only their owner (clients-and-ai.md L.5).
- **Object refs.** Every object in a plugin payload carries `ref`, `<plugin>:<Kind>/<id>` (for example `kubernetes:Pod/media/app-0`). Refs are opaque to clients; the prefix routes to the owning plugin's `Objects` (clients-and-ai.md L.6).
- **Overview composition (P2.5).** `core.overview` no longer waits for the other topics: `topics.compose` merges the attention items of every running `AttentionSource` (each item carries `plugin`), `KPISource` outputs (`OverviewData.kpiList`, new) and `TopologySource` fragments, with the legacy per-source builders adapted as one contributor. The `Kpis` struct of today's payload stays for the SPA until each KPI moves into a `KPISource` in G3.
- **As shipped (G3 PR-1, v0.7.0): contributors and KPIs.** `plugin.Contributor` gains `Sources func() []model.SourceStatus`, filled from the plugin's `Status().Sources` (none when `Status` panics); `compose` merges them, without repeating a name (the failing entry wins), into `core.overview.sources`, which the Overview panel headers render. Without it a dead Alertmanager would show an empty, green Attention panel once its poll left the Manager. A hidden plugin has no contributor and so no chip. A KPI is `model.KPI{Key, Plugin, Label, Unit, Value, Health}` in `OverviewData.kpiList`; a migrated plugin's field leaves `model.Kpis` and its SPA tile reads `kpiList` by key (alertmanager: `firing`, replacing `AlertsFiring`; the alerts rail badge reads it too). A KPI carries its own `health`, and a tile reads that instead of recomputing from other plugins' attention items. The planned keys of the later leaves: proxmox `vms.running`, `vms.total`; hardware `power`, `cpuTemp`; kubernetes `nodes.ready`, `nodes.total`, `pvc.worst`; flux `ready`, `total`, `suspended`, `verified`. Every object that has an identity carries `ref` (`plugin.Ref(plugin, kind, id...)` makes `<plugin>:<Kind>/<id>`, `ParseRef` splits it; the prefix is the publishing instance's ID). In PR-1: `alertmanager:Alert/<fingerprint>`, `alertmanager:Silence/<id>`, `gatus:Endpoint/<Endpoint.Key>`. `AttentionItem.ref` points at the object. `plugintest.AssertRefs` walks a payload and requires every `Ref` field to be non-empty, parseable, owned by the plugin and registered, plus a must-have list per plugin so a type that forgot its field fails. The `Objects` interface and `/api/v1/context` stay with P7.3.
- **G2 routes.** Only the core routes are under `/api/v1` in G2 (`me`, `stream`, `series`, `plugins`, `topics/{topic}`, `plugins/{id}/…`). The integration routes that still run inside `topics.Manager` (`/api/config`, `/api/vms/{vmid}`, `/api/pods/{ns}/{name}`, `/api/logs`, `/api/logs/tail`, `/api/actions/*`, `/api/silences`) keep their unversioned paths until their plugin migrates and the P3.13 cleanup deletes them; they already use the L.2 error envelope, but they are not part of the OpenAPI document (`Route.Legacy` in `internal/api/routes.go`, with an allowlist test).
- **As shipped (G3 PR-1, v0.7.0): routes.** Four integration routes are gone and answer 404 `not_found`: `/api/config` (the launcher links are the `launcher.links` topic; the host model is a later leaf's), `/api/logs` and `/api/logs/tail` (now `GET /api/v1/plugins/victorialogs/logs` and `.../logs/tail`), and `POST /api/silences` and `DELETE /api/silences/{id}` (now `POST /api/v1/plugins/alertmanager/actions/silence.create` and `.../silence.expire` with body `{"id"}`). `/api/vms/{vmid}`, `/api/pods/{ns}/{name}` and `/api/actions/*` keep their unversioned paths until P3.6, P3.8 and P3.11. `dash api openapi` now documents the routes of the shipped plugins, streams included, and a test compares every plugin route with the spec.
- **As shipped (G3 PR-2, v0.8.0): topics, routes and fields.** No alias for any of these, and Go and web change together. Renamed: `vms` is `proxmox.vms` (`TopicVMs`, the identifier kept) and `host` is `hardware.hosts` (`TopicHost`, kept). New: `proxmox.node` (`TopicProxmoxNode`, `pvemodel.NodeData`), `proxmox.storage` (`TopicProxmoxStorage`, `pvemodel.PVEStorageData`) and `hardware.machines` (`TopicHardwareMachines`, `hwmodel.MachinesData`), each with its `TopicTypes()` entry and web declaration. The stream order of a demo run is `core.plugins`, `core.overview`, `alertmanager.alerts`, `gatus.status`, `proxmox.vms`, `proxmox.node`, `proxmox.storage`, `hardware.hosts`, `hardware.machines`, `cluster`, `storage`, `apps`, `flux`, `launcher.links`. Fields that moved or went: `host.{uptimeSeconds,pveVersion,kernel,cpuModel}` are `proxmox.node.{uptimeSeconds,version,kernel,cpuModel}`; `vms.machines` is the topic `hardware.machines`; `storage.pveStorages` is `proxmox.storage.storages`; `kpis.vmsRunning` and `vmsTotal` are the KPIs `vms.running` and `vms.total`, and `kpis.powerWatts` and `cpuTempMaxC` the KPIs `power` and `cpuTemp`, all in `kpiList` (the `Kpis` struct stays until P3.13). Fields added: `ref` and `critical` on `VM`; `ref` on `NodeData`, `PVEStorage`, `Machine`, `Pool` and `Disk`; `bays`, `fanMaxRpm`, `sensorAliases`, `tempWarnC`, `tempCritC`, `cpuTempHealth` and `ref` on `HostData`; `cpuTempHealth` on `TopologyHost`. `plugin.TopicName(id, spec)` is the one rule for a topic's wire name (its `Legacy` name if it has one, else `<plugin>.<name>`), and the Manager's owner map is keyed by it. `/api/vms/{vmid}` is gone (404 `not_found`) and the route is `GET /api/v1/plugins/proxmox/vms/{vmid}`; `/api/pods` and `/api/actions/*` stay until P3.8 and P3.11. The generated OpenAPI now gives every path parameter that no route documents a generic description (the plugin route's `{vmid}`; vacuum requires one). Tabs opened before the deploy must be reloaded, and a client that reads the old names or fields must switch.
- **As shipped (G3 PR-3a, v0.9.0): topics, routes and fields.** No alias for any of these, and Go and web change together. Renamed: `cluster`, `storage` and `apps` are `kubernetes.cluster`, `kubernetes.storage` and `kubernetes.apps` (`TopicCluster`, `TopicStorage` and `TopicApps`, the identifiers kept). The stream order of a demo run is `core.plugins`, `core.overview`, `alertmanager.alerts`, `gatus.status`, `proxmox.vms`, `proxmox.node`, `proxmox.storage`, `hardware.hosts`, `hardware.machines`, `kubernetes.cluster`, `kubernetes.storage`, `kubernetes.apps`, `flux`, `launcher.links`; `flux` keeps its name and its shim until v0.10.0. Routes: `GET /api/pods/{ns}/{name}` is `GET /api/v1/plugins/kubernetes/pods/{ns}/{name}` and `POST /api/actions/restart` is `POST /api/v1/plugins/kubernetes/actions/restart` (A.8); the old paths answer 404 `not_found`, and `POST /api/actions/reconcile` is the one legacy integration route left. Fields that moved or went: `storage.nasBackups` (type `NASBackup`) is `kubernetes.storage.backupJobs` (type `BackupJob`); `apps[].iconUrl` is `apps[].icon`, the raw annotation token that the SPA resolves (A.9); `kpis.nodesReady`, `nodesTotal` and `worstPvc` are the KPIs `nodes.ready`, `nodes.total` and `pvc.worst` (plugin `kubernetes`) in `kpiList` (the `Kpis` struct keeps only the flux fields until P3.11 and P3.13). Refs added, one per type that has an identity: `kubernetes:Node/<name>`, `Namespace/<name>`, `Workload/<Kind>/<ns>/<name>` (the kind is in the id because a Deployment and a StatefulSet may share `ns/name`), `Pod/<ns>/<name>`, `Certificate/<ns>/<name>`, `ExternalSecret/<ns>/<name>`, `PVC/<ns>/<name>`, `BackupJob/<ns>/<name>` (the row name: the `label`, else the metric's `cronjob` label) and `App/<ns>/<source>/<resource>/<host>` (source `httproute` or `ingress`, one app per hostname); the kubernetes attention items carry the `ref` of the object they point at. `plugintest.AssertRefs` fails on a duplicate `Ref` within one payload and names both objects, except that it **skips `model.AttentionItem`**: several items may point at one object, so the rule does not apply to them. The transitional `Bucket` and `CNPGCluster` types in `kubernetes.storage` carry no `ref` yet (no field before its producer; v0.10.0). The legacy source chip `victoriametrics` is gone from a real run's `core.overview.sources`: the kubernetes plugin's queries report `prometheus`, as hardware's do. Tabs opened before the deploy must be reloaded, and a client that reads the old names, routes or fields must switch.

### A.8 Actions (writes)

Today there are three hand-written handlers (`api.go:88-91`), plus the audit helper at `internal/api/actions.go:30-38`.

- A plugin declares an `ActionSpec`, and the core mounts `POST /api/v1/plugins/{id}/actions/{name}`. The middleware chain is: principal (clients-and-ai.md M.1), then the `actions:<plugin>` scope (tokens only), then `RequireRole(spec.MinRole)`, then CSRF (every non-bearer request; bearer principals skip it, M.2), then a per-principal rate limit (10 per minute), then body cap plus read deadline, then schema validation of the input, then deny namespaces (`actions.denyNamespaces`) plus the token's `actionNamespaces`, then the agent policy (`actions.agents`, clients-and-ai.md O.1), then an audit **intent** record (if the write fails, the request returns 503 `audit_unavailable` and `Act` does not run; denials that end the chain earlier are audited too, but best effort, C.7), then `Actor.Act`, then an audit **outcome** record (a failure there is logged at ERROR; the response still reports the action's real result). Action errors carry `APIError.Code` (clients-and-ai.md L.2).
- **Opt-in.** A plugin with actions also has an `actions.enabled` config field. The default is `false` for every plugin (decided, J.11), and the homelab sets `true`. With actions disabled, the action is not mounted and the SPA hides its buttons.
- **Confirm semantics stay server-side.** `ConfirmTypeName` reproduces today's rule: in a protected namespace, `confirm` must equal the target name (`actions.go:82-87`). The plugin decides per request whether typed confirmation applies (e.g. a namespace carrying the configured protected label). `ConfirmDialog` is a UI-only confirmation.
- **Capability matrix after migration:**

| Plugin | Action | Today | MinRole | Confirm |
|---|---|---|---|---|
| kubernetes | `restart` (Deployment/StatefulSet/DaemonSet) | `actions.go:58-107` | admin | TypeName if protected, else Dialog |
| flux | `reconcile` (5 kinds) | `actions.go:109-134` | admin | Dialog |
| alertmanager | `silence.create` | `internal/api/silences.go:51-89` | admin | Dialog (with the same validation: `silences.go:26-49`) |
| alertmanager | `silence.expire` | `silences.go:91-103` | admin | Dialog |

- The admission guardrail stays the real fence for Kubernetes writes. The Helm chart ships an optional generic ValidatingAdmissionPolicy (D.4), written fresh rather than copied from the private homelab repo. `writeKubeErr` keeps passing 403/404/409/422 messages through verbatim (`actions.go:40-52`).
- **Silence rules** (F.3): `protectedAlerts` can never be silenced, and `silence.expire` works only on silences dash created.
- **Agents.** `ConfirmDialog` is a UI-only confirmation, so scripts and agents bypass it. Requests from MCP clients are held for human approval instead (clients-and-ai.md K.20, O).
- **As shipped (G2).** The framework is `(*Server).runAction` (`internal/api/plugin_actions.go`). Steps that need a token or an agent have no principal to apply to yet and are skipped until phase 7: the `actions:<plugin>` scope, the token's `actionNamespaces`, and the agent policy. The principal is `auth.Principal{Kind, User, Role}` (`internal/auth`), and every authenticated proxy, dev or demo principal is `admin`, as today, until P4.2. The rate limit is a sliding window of 10 per minute per principal (`internal/api/ratelimit.go`; 429 `rate_limited` with `Retry-After`). Input is decoded strictly into `reflect.New(Input())`, required fields are checked, and `Validate() error` is called when the input has it. `actions.enabled` and `actions.denyNamespaces` are read from `plugin.InstanceConfig`; none of the 11 G2 sections has them yet, because the legacy restart, reconcile and silence handlers are not framework actions until G3 (their migration must not flip the homelab's actions off, J.11). Audit records carry `plugin` and `requestId`. A missing action, a stopped instance, disabled actions and a plugin that is not an `Actor` all answer the same 404 `not_found`.
- **As shipped (G3 PR-1, v0.7.0): action configuration.** G2 shipped the chain but nothing that fed it: no section carried `actions.enabled`. A section value now implements `ActionsConfig() pluginconf.Actions` (action policy: `InstanceConfig.ActionsEnabled` and `DenyNamespaces`) and `ReadDenyConfig() []string` (route policy: `InstanceConfig.ReadDenyNamespaces`), and `cmd/dash` fills the instance configuration through them. `plugins.alertmanager.actions.enabled` (`pluginconf.ActionsToggle{Enabled}`) is the first; a silence target has no namespace, so alertmanager offers **no** `denyNamespaces`. Either value changing changes the `InstanceConfig`, so a reload restarts the instance. **Actions are off by default**: without `actions.enabled`, Silence and Expire are not mounted and the SPA does not render them. `ActionInfo.minRole` (`admin` or `viewer`) is published in the plugin row. `GET /api/v1/me` reports `capabilities.actions` and the `actions:<id>` scope only to a role that meets the `minRole` of at least one of that plugin's actions, and carries `ui.iconBaseUrl` (A.9). The SPA gate is `useActionAvailable(pluginId, action)` (`web/src/plugins/use-action-available.ts`): the live `core.plugins` row lists the action and `me.role` meets its `minRole` (a missing or unknown `minRole` counts as admin). It deliberately does not read `me.scopes` or invalidate `/me`: scopes derive from the same rows, so they add no information and `useMe` never refetches. Silence and Expire use it; the Silence button is also hidden for a `protected` alert and when the topic offers no `silenceDurations`, and Expire shows only for a silence with `dash: true`. **Outcome:** `Act` returns `plugin.Outcome{Body, Target *ActionTarget}` when the audit target exists only after the action ran. The core answers 200 with `Body` (202 `{"status":"accepted"}` when `Body` is nil) and, on success only, replaces the target of the audit record it wrote before `Act`. `silence.create` returns `{silenceID}` and the target `Silence/<id>` (the intent record names `Silence/<alertname>`); `silence.expire` answers 202 (it was 200 `{"status":"expired"}`). Audit action names (`silence.create`, `silence.expire`) are unchanged, records now carry the plugin, and the `dash_actions_total` labels are unchanged.
- **As shipped (G3 PR-1, v0.7.0): plugin streams and read policy.** `Routes.Stream(pattern, StreamHandler, StreamSpec)` mounts a `GET` that serves server-sent events, so the core keeps the stream hygiene (slot caps, write deadline, heartbeat, `Last-Event-ID`, `dash_sse_streams{kind="tail"}`). `StreamSpec` is `{Summary, Scope, Query, Validate, Target, Namespace}`; the handler gets a `plugin.Stream{Begin, Event}`. `Scope: "logs"` is documented now, and the core checks the role only until bearer tokens exist (phase 7). The core runs this **step order** (`internal/api/plugin_stream.go`): the role (viewer), `RouteSpec.Namespace` against the instance's `ReadDenyNamespaces` (403 `forbidden`, audited as a namespace denial; GET routes and streams alike), `StreamSpec.Validate` (a request that can never succeed answers 400 before any slot or record), the tail slot (429 per user, 503 in all: `server.limits.tailsPerUser` and `tails`), the audit record of the open (`logs.tail` for scope `logs`, otherwise `stream.open`; the plugin ID and the target `StreamSpec.Target` names), then the handler under `server.limits.tailMaxDuration`. A denied tail therefore answers 403 even when every slot is taken. The record is bounded because a client chooses what it opens: each text of the target is cut to 200 bytes, at most one record per user and target per minute, and at most 30 per user per minute; a record that is not written never refuses the stream. A stream ends when its plugin instance stops (a reconfiguration, a disable, shutdown). An error before `Begin` becomes the standard envelope (an `*ActionError` as written, an `upstream.ErrInvalid` as 400, anything else through `plugin.Fail`: the category-only 502, the detail logged scrubbed; the API passes it to the supervisor as `RouteGuard.Fail`); after `Begin` the stream just closes. **Scrubbing:** `Stream.Event` does not scrub. A plugin scrubs the text with `Host.ScrubLog` before it builds the payload, because scrubbing the encoded JSON would miss a secret that contains a character `json.Marshal` escapes (`& < > "`). `RouteSpec.Namespace func(r) string` is the namespace a request targets (`""` for none); the denied namespaces come from the section's `ReadDenyConfig()`.
- **As shipped (G3 PR-3a, v0.9.0): Restart is a framework action; reconcile stays legacy until v0.10.0.** `POST /api/v1/plugins/kubernetes/actions/restart` replaces `POST /api/actions/restart`, which answers 404 `not_found` (in demo mode too). The `ActionSpec` is `restart`, title "Restart", minimum role `admin`, destructive, not idempotent, input `model.RestartRequest{kind, namespace, name, confirm}`, summary `Restart <Kind> <ns>/<name>`, target kind, namespace and name. It is mounted only with `plugins.kubernetes.actions.enabled: true` (off by default, as the draft says), so a deployment that had Restart before sets `DASH_PLUGINS_KUBERNETES_ACTIONS_ENABLED=true`; without it the pod sheet, the workloads list and the palette lose the control. Through the framework chain a call is limited to 10 per minute per principal (429), audited with `plugin=kubernetes` under the unchanged action name `restart`, refused for a viewer, and answered 403 (and audited) when its namespace is in `actions.denyNamespaces`. `Act` takes the kinds Deployment, StatefulSet and DaemonSet, checks namespace and name with `ValidDNS`, and for a protected namespace (`conventions.protectedLabel`, label value `"true"`) needs `confirm` equal to the workload name (400 `invalid_input`, "type the workload name to confirm"). The patch is the strategic merge of exactly `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]` under the field manager `dash` (`TestRestartPatchBodyIsExactlyTheAnnotation`); an API error keeps its 403, 404, 409 or 422 and anything else is 502 `upstream_unavailable` with a category; success is 202 `{"status":"accepted"}`. `core.plugins` lists the action, `GET /api/v1/me` lists `actions:kubernetes` to an admin only, and the SPA draws the Restart controls from that row and the role (A.9). The legacy handler, `api.Deps.{Kube,Dyn,Data}`, the `fieldManager` constant of `internal/api` and `topics.Deps.{Services,Secrets}` are deleted because nothing reads them any more; the demo wrapper answers 202 only for the legacy reconcile route, and the kubernetes demo twin answers Restart through the framework.
- **As shipped (G3 PR-3a, v0.9.0): reconcile stays a legacy route for one more release.** `POST /api/actions/reconcile` is still served by the topic Manager (it moves with flux in P3.11), but it now patches through the kubernetes plugin's `svc.Cluster.Patch` (A.5) and answers 503 `upstream_unavailable` while the plugin is not running. It keeps v0.6.0's weaknesses until v0.10.0: no role or scope check beyond the legacy gate, no rate limit and no `denyNamespaces`.

### A.9 How the SPA renders plugins

Design (J.2): **compiled-in React modules registered by plugin ID; the backend says which are on.** As shipped in G2 (`web/src/plugins/`):

```ts
// web/src/plugins/types.ts
export interface PluginModule<Id extends string = string, T extends TopicMap = TopicMap> {
  id: Id;                                  // equals the Go Manifest.ID and the directory under modules/
  order?: number;                          // rail position among all entries; default: after the numbered ones, in registry order
  topics: T;                               // the topics its pages read, typed: { vms: topic<VMsData>() }
  routes?: readonly { path: string; page: LazyExoticComponent<ComponentType<RouteProps>> }[]; // lazy: one chunk per page
  badge?: Readonly<Record<string, ComponentType>>;   // a badge per nav path (alertmanager: the firing count)
  palette?(ctx: PaletteCtx): readonly PaletteGroup[]; // command-palette groups; groups with one id merge across modules
  linkIcons?: Readonly<Record<string, LucideIcon>>;  // icons for the Link.kind values the module's links carry
}

// web/src/plugins/registry.ts: explicit list, mirroring cmd/dash/plugins.go
export const MODULES = [alertmanager, gatus, proxmox, hardware, kubernetes, flux, launcher, grafana, headlamp] as const;
```

Differences from the first draft of this section: a module has no static `nav` list (the nav entries come from `GET /api/v1/plugins`, so a plugin without a module can have one), panels are not a module field (`web/src/plugins/panels.ts` maps module id to its slot contributions), and `settings` waits for the settings UI (P4.9). `cnpg` and `rustfs` get modules when their G3 leaf creates the plugins. A Go test (`TestSPAModulesMatchManifests`, `cmd/dash/spa_test.go`) fails when a directory under `web/src/plugins/modules` is not a manifest ID, or a manifest with nav has no module.

- **Visibility.** `usePluginsState` reads the `core.plugins` topic, and `GET /api/v1/plugins` until the stream has spoken. A plugin's pages are visible in the states `starting`, `healthy`, `degraded` and `error`, and hidden in `disabled`, `unavailable`, `unconfigured` and `blocked`; a state the SPA does not know counts as visible (`visibility.ts`; `PluginState` is a plain string in the generated TypeScript). Core pages (Overview, Storage, Apps) always exist. The rail, the mobile sheet, the palette's Pages group and the top bar title all come from `useNavEntries`: the core pages plus the visible plugins' `nav` entries that a module route (or a generic view, below) can render, ordered by the module's `order` (so the rail is Overview, VMs, Cluster, GitOps, Hardware, Storage, Alerts, Status, Apps). Until the first plugin list arrives the rail shows the core entries and three placeholders.
- **Routes and the disabled page.** `PluginRoutes` renders the core routes, every module route and `/x/:id`. Each module page is lazy-loaded behind `RouteBoundary` (an error boundary with a reload prompt) and `Suspense`. A hidden plugin's route renders `PluginDisabledPage`, never the 404 page and never the plugin's page (so it fetches nothing): "<name> is switched off. Set `plugins.<id>.enabled` in the configuration to turn it on." for `disabled`, the state in words plus the server's viewer-safe `message` for the others, and "This dash has no <id> plugin." when the server does not list it. A link to settings arrives with P4.8.
- **Topics.** `stream.ts` listens to `core.plugins` and `core.overview`, then to every topic a `core.plugins` event lists, and drops the cached envelope of any topic that no visible plugin lists (a disabled plugin's data must not stay on screen). Only the topics whose listener that event attaches, and which no envelope covers yet, are fetched once from `GET /api/v1/topics/{topic}`: a plugin this page has not heard of can have published before its listener existed, and the hub publishes only changes. A topic that already had a listener (a plugin switched off and on again while the page is open) is not fetched, because the fetch would race the plugin's first publish and answer 404 until it came. Its data arrives over the stream: `Hub.Remove` forgets the topic's dedup digest when the instance stops, so the plugin's next publish is sent even if it equals the one before. The fixed `TOPICS` list is gone. The pages still read the legacy topic names until each plugin's G3 leaf renames them.

  **Rule for G3 plugins: a row that turns visible must not leave its pages loading.** The SPA drops a hidden plugin's envelopes, and the hub drops a topic's digest only when an instance stops. A plugin that keeps running while its row goes from `unavailable` (or `unconfigured`/`blocked`) to `healthy`, with no restart, and whose next publish is identical to its last, would therefore publish nothing the SPA can see, and the pages would show skeletons until the data changes. Each G3 leaf picks one of these, and says which in its Body: (a) the supervisor re-publishes the instance's latest event of every topic when its row changes from hidden to visible (the hub resends it without the dedup check); or (b) the plugin does not publish while it reports a hidden state, so its first publish after the change is a new value. A test per leaf covers it: the plugin turns visible and a viewer's stream receives each of its topics.

  **As shipped (G3 PR-1, v0.7.0): rule (a) for every plugin of the PR.** One mechanism instead of one per plugin (rule (b) would have to be written and tested by each author, and it does not cover a row that turns visible while its last payload stays identical). `Hub.Resend(topic) bool` delivers the stored latest event of a topic to the current subscribers: same data, same digest and ETag, no dedup check and no digest change. It reads and fans out under the hub's lock, so it cannot overtake a concurrent `Publish`; it applies each subscriber's topic filter and user scope to every stored copy (global and per-user); it returns false when the topic has no latest event. `plugin.Publisher` gains `Resend`. `Supervisor.publishInfos` keeps `prevVisible` inside the publish lock, right after it publishes `core.plugins` and from the same rows; `Run`'s first `publishInfos` seeds it. For every running instance whose `plugin.Visible(state)` went from false to true it calls `Resend` for each of the instance's topics. `plugin.Visible` is the Go twin of `isVisible` in `visibility.ts`: not `disabled`, `unavailable`, `unconfigured` or `blocked`; an unknown state is visible. Pins: (1) the restart path counts, because a plugin whose hidden state stops it (`disabled`, `unconfigured`, `blocked`) publishes on `Start`, and the detector still fires for it. That resend covers the interleaving "topic event, then another plugin's `core.plugins` event drops the SPA's listener for the topic, then the row turns visible", where the fresh event alone would be lost. (2) There is no minimum interval between resends, because skipping one would lose the data of a client that dropped the listener when the row went hidden. Instead a plugin must not flap faster than the 1 s status tick. (3) The legacy shims pass through the detector too, which fixes the same stale-topic trap for them. The SPA needs no change: `stream.ts` stores any event for a listed topic and drops envelopes only on a later `core.plugins`. Each plugin's test is `plugintest.AssertTopicsReachOpenStream(t, factory, before, after)`, on the real `hub.Hub`: it starts the plugin hidden, opens a subscriber, flips, and asserts that every declared topic **with a latest event** arrives within 2 s **after the `core.plugins` event that made the row visible** (not "`core.plugins` first", which would fail on the restart path). A plugin without topics (prometheus, grafana, headlamp, victorialogs) tests that its row turns visible.
- **Composite pages** (Overview, Storage, Apps) render **slots**: `overview.kpis` (the KPI tiles, owned by alertmanager, flux, kubernetes, proxmox and hardware), `storage` (panels of kubernetes and proxmox, full or half width) and `apps.sources` (the apps section of kubernetes and the infrastructure section of launcher). A contribution has an `order` and may name the topics it `reads`; it is left out when its module is hidden or a topic it reads is unavailable, and each panel sits in an error boundary so one broken panel does not take the page down. `overview.side` and `hardware.facts` of the first draft are not defined: a slot with no contributor would be untested, so each arrives with the first G3 leaf that has one. The generic KPIs that plugins publish in `core.overview` (`kpiList`) render after the slot tiles. Attention items link to their `route` only when the owning plugin is visible and has a page for it.
- **As shipped (G3 PR-1, v0.7.0): one tile per KPI.** `PublishedKpis` draws every `kpiList` entry of a visible plugin after the slot tiles, so a plugin that also contributes an `overview.kpis` slot panel would be drawn twice. Rule: a plugin that contributes that slot owns the presentation of all its KPIs, and `PublishedKpis` skips every plugin that has one; a plugin without a tile gets the generic `Kpi`. Alertmanager's tile takes its health from the KPI. `hardware.facts` is still undefined.
- **As shipped (G3 PR-2, v0.8.0): `hardware.facts`, and rule (a) for proxmox and hardware.** The `hardware.facts` slot is defined (`SlotProps["hardware.facts"]` is `{ host?: HostData }`, a type added by the scaffold and served by `Slot` under the same rules as the others: order, visibility, `reads`). Its first contributor is proxmox's `NodeFacts` panel (order 10, `reads: ["proxmox.node"]`): uptime, PVE version, kernel and CPU model, hidden when proxmox is hidden and drawn only once the topic has arrived. The Hardware page renders `<Slot id="hardware.facts" host={host} />` in place of its whole System `<dl>` (the panel draws its own `<dl>`, so keeping the old one would nest them), so with proxmox off the page has no such card. The VM tile of the Overview and the Hardware power and CPU temperature tiles each read their plugin's `kpiList` entries (`proxmox/vms.running`, `proxmox/vms.total`, `hardware/power`, `hardware/cpuTemp`) with the KPI's own health, and draw nothing until the KPI exists. Both plugins use **rule (a)** (no per-plugin mechanism), pinned by `plugintest.AssertTopicsReachOpenStream` for `proxmox.vms`, `proxmox.node`, `proxmox.storage`, `hardware.hosts` and `hardware.machines`. Proxmox never reports a hidden state while running, so its test covers the stop/start path; **hardware may be visible while running**: its row is `unavailable` until its node is known and turns visible when it is, with no restart, and a plugin that is visible once it has published stays visible with a missing dependency shown as a source (F.7).
- **As shipped (G3 PR-3a, v0.9.0): rule (a) where `Resend` is the delivery path, and the kubernetes module.** Kubernetes is the first plugin that reports `unavailable` **while running** (the API server unreachable for 10 s, or no cluster configuration at all) and is then visible again with an identical payload, because its informers never stopped. The supervisor's detector sees `Visible` go false to true on a running instance and `Resend`s `kubernetes.cluster`, `kubernetes.storage` and `kubernetes.apps`: every earlier plugin's flip took the stop/start path, so this is the first time the resend path is the one that delivers. The row goes `unavailable` only after the probe has failed for 10 s (`unavailableAfter`, measured on `Host.Now`), so a link that flaps faster does not resend the three payloads on every flip; recovery has no debounce, because a plugin that is healthy again must show its data at once. The tests: `TestRecoveryResendsTopicsToAnOpenStream` (the real `hub.Hub`, a probe the test switches, `AssertTopicsReachOpenStream` with `before` = `unavailable` after 10 s on the fake clock and `after` = `healthy`, the data identical so the digest cannot be what delivers it), `TestTopicsReachOpenStreamAfterConfigReload` (the stop/start path), `TestKubernetesDebouncesUnavailable`, `TestUnavailableAfterTenSecondsOfFailedProbes` (the real constant) and `TestStartsWithoutACluster`. The recovery test compares **only each topic's data**: whole envelopes differ in `updatedAt` and check times after any poll that follows the recovery, and the first version failed about once in 100 runs under `-race`. A production API server that does not flap never exercises the path outside these tests.
- **As shipped (G3 PR-3a, v0.9.0): the kubernetes module.** The module declares `kubernetes.cluster`, `kubernetes.storage` and `kubernetes.apps`; the pages, the panels' `reads`, the launcher and the palette use those names. The nodes tile reads the `kpiList` entries `nodes.ready` and `nodes.total` (its health is the ready KPI's), and the Overview's "Fullest volume" line reads `pvc.worst` with the KPI's health and is hidden with the plugin. The storage panel for the backup rows is titled **Backup jobs** (empty text "No backup jobs."; the draft and v0.8.0 said "NAS backups") and lists `backupJobs`. Restart is gated everywhere by `useActionAvailable("kubernetes", "restart")`: the pod sheet, the workloads list, the confirmation dialog (whose Restart button is disabled with a note if the action disappears while it is open) and the palette. Palette functions are not hook sites and `PaletteCtx` had no role access, so **`PaletteCtx` gained `actionAvailable(pluginId, action)`**: the pure `actionAvailable()` over `core.plugins` and `/me`, built once in `PaletteBody` (this edit is outside the kubernetes module's own files). The generated `SlotProps.storage.env` is typed `TypedEnvelope<"kubernetes.storage">`. Tabs opened before the deploy keep the old topic and route names and must be reloaded.
- **Live regions.** Each launcher section's matching count is its own polite live region (`role="status"`, `aria-live="polite"`), replacing the page-level count line, so a search that changes one section's count is announced and a section a plugin adds or removes brings its announcement with it. The compact launcher on the Overview announces nothing.
- **Generic panels for simple plugins.** A plugin without a React module may declare `UI.Generic` views: `Kind` `table`, `list` or `stat`, a `Topic` (a declared topic name; `PluginInfo.generic` carries the published name), a `Path` (an RFC 6901 pointer into the topic's data) and `Columns` with a `Format` (`text`, `number`, `bytes`, `percent`, `time`, `health`). The supervisor synthesises a nav entry `/x/<id>` (`UI.Icon`, default `Puzzle`) when there is no `Nav`, and `GenericPluginPage` renders one `Panel` per view with the existing table components; a missing path or wrong shape shows "No data". No production plugin uses it in G2.
- **Chunks.** Module pages are separate chunks (`scripts/check-chunks.mjs` in `make check` verifies a chunk per page and that their code is not in the entry chunk). A tab loaded before a deploy that opens a page it had not visited asks for a chunk that no longer exists; `vite:preloadError` triggers one reload, guarded by a 30 s `sessionStorage` timestamp so it cannot loop (`plugins/chunk-reload.ts`).
- **Types per plugin.** `TopicData` is derived from the modules' `topics` declarations plus the core topics (`plugins/topic-data.ts`), and a compile-time guard requires that set to equal the generated `Topic` union. Each plugin model package, once a G3 leaf creates one, gets its own tygo entry and generated `model.gen.ts` next to its module; `make check` fails on drift in any of them and on an uncommitted generated file. Until then there is one model package.
- **As shipped (G3 PR-2, v0.8.0): per-plugin model packages and the tygo recipe (P2.9).** The first two plugin model packages exist: `internal/plugins/proxmox/model` (`NodeData`, `PVEStorage`, `PVEStorageData`) and `internal/plugins/hardware/model` (`Machine`, `MachinesData`). Each has its `tygo.yaml` entry (`output_path: web/src/plugins/modules/<id>/model.gen.ts`, `enum_style: union`, `time.Time: string`) and its generated `model.gen.ts`, which the module imports (`import type { NodeData, PVEStorageData } from "./model.gen"`) and uses in its one-per-line `topic<T>()` declarations. **The import-leaf rule:** a plugin model package imports only the standard library, because `internal/model.TopicTypes()` names the payload type of every topic and so imports it; a payload that needs a core type (`Link`, `Health`, `SourceStatus`) therefore stays in `internal/model` (the VM types, `HostData` and `TopologyHost` do). `TestPluginModelPackagesAreImportLeaves` (`internal/model/leafmodels_test.go`) globs `internal/plugins/*/model/*.go` and fails on any other import. Where a plugin type that is not a topic payload needs core types, `tygo.yaml` documents the recipe, checked with tygo v0.2.21: tygo writes a selector type such as `model.Health` by looking up its source text in `type_mappings` and as `any /* model.Health */` when there is none, so list every core type the package uses (`model.Health: Health`, and so on) and import them with a `frontmatter` line from `@/api/types`; the mapping key is the identifier as written in the Go source, so a package uses one alias for the core model package. The answer to "whether tygo imports across packages by itself" is no. Neither PR-2 package needed the recipe, since neither uses a core type.
- **No remote JavaScript.** JS fetched at runtime would run with the admin's session and could call settings or action endpoints, so it is equivalent to stored XSS. Add a CSP to SPA responses (`internal/api/static.go` sets no security headers today): `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'; form-action 'self'`, plus the `ui.iconBaseURL` origin in `img-src` when it is configured. `style-src 'self'` allows no inline `<style>`: the one that needed it, the chart colours in `web/src/components/ui/chart.tsx`, is fixed by the hardening PR (branch `fix/api-hardening`); a dependency that injects one at runtime must have its CSS imported statically (P4.6).
- **`safeHref()`.** Every `href` built from config or upstream data (launcher links, `web/src/config/links.ts`) goes through `safeHref()`, which allows `http:` and `https:` only; anything else renders as text (clients-and-ai.md K.12).
- **Launcher icons** may be a bundled name, a same-origin path, or a URL under `ui.iconBaseURL`. Any other URL is rejected.
- **As shipped (G3 PR-1, v0.7.0): icons.** There is no bundled icon set: the draft's "bundled (lucide) name" branch had nothing to resolve to in the tree, so it is dropped. Servers send the **raw icon token** (`LauncherLink.icon`), never a resolved URL, and the SPA resolves it with one function, `iconSrc(token, iconBaseURL)` in `web/src/config/links.ts`: a same-origin path is used as is; a name (`^\w[\w.-]*$`, such as `proxmox`) becomes `<iconBaseURL>/<name>.png`; an absolute URL is used only when it is http(s), has the base's origin and lies under the base's path; everything else gives the generic letter tile: a name with no base, another origin, a protocol-relative URL (`//host/x`), a path with a backslash, a relative path, `data:` and `javascript:`. `iconBaseURL` is the new top-level `ui.iconBaseURL` (`DASH_UI_ICON_BASE_URL`, default empty, an http(s) URL with a host and without query or fragment, `dash:"url,restart"`: restart-only, because it feeds the `img-src` of the CSP in P4.6). It reaches the SPA in `GET /api/v1/me` as `ui.iconBaseUrl`. With default settings the browser requests nothing from a third-party CDN for launcher links. **In v0.7.0 it covers launcher links only:** `App.iconUrl` is still the absolute CDN URL the server builds, because routing apps through the rule with an empty base would turn every app icon into a letter tile; apps change to the raw `App.icon` token in PR-3 (P3.9).
- **As shipped (G3 PR-3a, v0.9.0): app icons (P3.9).** The last sentence of the v0.7.0 note above no longer holds: `ui.iconBaseURL` now covers **launcher links and app icons**. `App.iconUrl` is gone; the kubernetes plugin sends `App.icon`, the annotation's value as written (trimmed, at most 256 bytes; empty stays empty), and the Apps page resolves it with the same `iconSrc(token, iconBaseURL)` through one `useIconBase()` hook. The server builds no icon URL and has no CDN literal. `iconSrc` gained two rules, for both uses: a token starting `mdi-`, `si-` or `sh-` (homepage-style names, which are not dashboard-icons names: a request for one is a CDN 404, and the pre-change builder gave them no icon either) gives the letter tile and makes no request, and one trailing `.png`, `.svg` or `.webp` is dropped from a name before `.png` is appended, so `grafana.png`, `grafana.svg`, `grafana.webp` and `grafana` all give `<base>/grafana.png` (an icon that existed only as an SVG shows its PNG) and `a.b.png` gives `<base>/a.b.png`. For launcher links this fixes a latent PR-1 gap: `home-assistant.png` became `home-assistant.png.png`. With `ui.iconBaseURL` empty **no app icon is requested** and every app shows a letter tile: app icons no longer come from a third-party CDN by default (the v0.8.0 behaviour). A test on the Apps page and one on the launcher assert that no `<img>` outside the dash origin appears with an empty base.
- Closed unions widen: `Link.kind` is a `string` in the generated TypeScript (`tstype:"string"`; the Go `LinkKind*` constants stay), and `LinkButtons` resolves an icon from the modules' `linkIcons`, then the `app` and `external` defaults, then a generic external-link icon, so an unknown kind still renders. `Group` stays a core enum (Platform, Apps, Tenants, Other), because grouping is a core concept that the kubernetes plugin feeds.

---

## B. Config layering and precedence

### B.1 Sources

| Source | Where | Format | Who edits | Reload |
|---|---|---|---|---|
| Built-in defaults | binary | Go | developers | n/a |
| UI settings | `$DASH_DATA_DIR/ui-settings.yaml` (default `/data/ui-settings.yaml`) | YAML, written by dash only | admins in the UI | immediate |
| Config file | `DASH_CONFIG` (default: `/data/config.yaml` if present; the chart uses `/etc/dash/config.yaml`) | YAML | operator / GitOps | polled every 10s |
| Environment | `DASH_*` | strings (JSON for lists) | operator | restart |

### B.2 Precedence (decision J.4, decided)

```
built-in defaults  <  UI settings (/data)  <  config file (DASH_CONFIG)  <  environment (DASH_*)
```

- The effective value of a field is the value from the highest layer that sets it.
- **Locked fields.** A field set by the config file or by env is read-only in the UI. The UI shows its source (`env DASH_PLUGINS_PROXMOX_URL` or `file /etc/dash/config.yaml`). Declarative sources therefore always win, and a click in the UI can never silently diverge from git.
- **Locks are enforced on the server.** The greyed-out field is only a hint. Any write (PUT, PATCH or DELETE, including secret Replace and Clear) to a file- or env-locked path returns `409 Conflict` in the L.2 error envelope (clients-and-ai.md L.2): `{"error", "code": "locked_field", "requestId", "details": {"path", "source", "ref"}}`, where `source` is `file`, `env` or `ui-ineligible` and `ref` names the env variable or file that locks the field (absent for `ui-ineligible`). The one write accepted on a locked path is "remove stored value" (below), because it deletes a shadowed UI key and cannot change the effective value. With `settings.ui: readonly` every settings write returns 409 (code `readonly_settings`), including that one.
- **Overlay at read time, never sync.** File and env values are merged in when the effective config is resolved, on every load and read. They are never copied into the UI store. Removing a value from the file or env immediately reveals the stored UI value, if any, or the default.
- **The UI store is sparse.** `ui-settings.yaml` holds only keys an admin explicitly saved, never a snapshot of the effective config. Saving a form persists only changed, unlocked fields; "reset to default" deletes the key. Vaultwarden shows the failure mode: its admin page writes every option to `config.json`, which then silently shadows later env changes ([configuration overview](https://github.com/dani-garcia/vaultwarden/wiki/Configuration-overview)).
- **Shadowed UI values.** If a UI value exists and an operator later sets the same field in the file or in env, the UI value is ignored. Settings shows "overridden by env; stored value unused", with a "remove stored value" button.
- **Path to GitOps.** Settings has an **Export as config.yaml snippet** button. It exports the admin-saved UI keys only; secrets appear as their ref, or as a placeholder for encrypted values, never as plaintext. The operator pastes the snippet into their own file. dash never writes into the operator's config file.
- **Granularity.** Scalars lock per field. Lists (launcher links, machines, critical VMs) lock as a whole. `plugins.<id>.managed: true` in file or env locks the entire plugin. `settings.ui: readonly` (or `DASH_SETTINGS_UI=readonly`) is fully managed mode: the UI can view but not save, and the server rejects writes. `settings.ui` defaults to `editable`, in the binary and in the Helm chart (decided, J.4). GitOps Kubernetes installs, including the homelab, opt into `readonly`, and so must any multi-replica deployment (D.4).
- **Why not "UI wins":** it breaks GitOps, it makes the running state differ from the reviewed state, and it means two operators fighting over a value never find out. The surveyed apps agree:

  | App | Who wins | Outcome |
  |---|---|---|
  | Grafana provisioning | file | Provisioned objects are read-only; the server answers 403/400 to UI edits ([provisioning](https://raw.githubusercontent.com/grafana/grafana/main/docs/sources/administration/provisioning/index.md), [datasources.go](https://raw.githubusercontent.com/grafana/grafana/main/pkg/api/datasources.go)) |
  | Immich | file | `IMMICH_CONFIG_FILE` locks the whole admin UI; updates return 400 ([config file](https://docs.immich.app/install/config-file/)) |
  | Home Assistant core config | `configuration.yaml` | The UI section is disabled and shown read-only ([core_config.py](https://raw.githubusercontent.com/home-assistant/core/dev/homeassistant/core_config.py), [ha-config-section-general.ts](https://raw.githubusercontent.com/home-assistant/frontend/dev/src/panels/config/core/ha-config-section-general.ts)) |
  | Vaultwarden | UI (`config.json` from the admin page) over env | The wiki warns that env changes stop working and now calls `config.json` "not recommended" ([configuration overview](https://github.com/dani-garcia/vaultwarden/wiki/Configuration-overview)) |
  | Grafana SSO settings | UI/API-saved values over ini and env | The docs warn: "nothing in the UI or the file says so, which most often surprises people during credential rotation" ([configure-grafana](https://raw.githubusercontent.com/grafana/grafana/main/docs/sources/setup-grafana/configure-grafana/_index.md), [sso-settings API](https://raw.githubusercontent.com/grafana/grafana/main/docs/sources/developer-resources/api-reference/http-api/api-legacy/sso-settings.md)) |

  dash locks per field and shows the source, which is finer than any of them (Grafana locks per object, Home Assistant per section, Immich the whole config).
- **Secrets are a value form, not a layer.** In the file and env layers a secret field may hold a literal (allowed in env; allowed in the file with a startup warning, since the file is often in git) or a reference (`env:NAME`, `file:/path`, `k8s:ns/name#key`, see C.3). The UI layer holds only an encrypted value or a `k8s:` ref: `env:` and `file:` refs are accepted only from the file and env layers, so an admin in the UI cannot point a secret at an arbitrary file or process variable (clients-and-ai.md K.22, S.14). `DASH_<NAME>_FILE` on an env var is shorthand for a `file:` ref, following the Docker secrets convention, and is resolved by the Go binary (B.3).
- **UI-ineligible fields.** `server.*`, `auth.*`, `settings.ui` and `dataDir` can never be written from the UI, whatever the other layers say. A settings write that touches one returns `409 Conflict` with the same envelope and `source: "ui-ineligible"` (above). They come from the file, env or defaults only (clients-and-ai.md K.23, S.14).

### B.3 Environment naming

- Core fields: `DASH_SERVER_PUBLIC_URL`, `DASH_SERVER_LISTEN`, `DASH_AUTH_MODE`, and so on, mechanically derived from the schema path.
- Plugin fields: `DASH_PLUGINS_<ID>_<FIELD_PATH>`. Each path segment is the JSON field name converted from camelCase to UPPER_SNAKE, and segments are joined with `_`. Example: `plugins.kubernetes.conventions.protectedLabel` becomes `DASH_PLUGINS_KUBERNETES_CONVENTIONS_PROTECTED_LABEL`, and `plugins.proxmox.tokenSecret` becomes `DASH_PLUGINS_PROXMOX_TOKEN_SECRET`. The mapping is not parsed back by guessing: dash generates the full name table from the schemas, and a unit test fails if two schema paths produce the same name. Lists and objects are JSON: `DASH_PLUGINS_HARDWARE_MACHINES='[{"name":"nas","nodeExporterNodename":"nas"}]'`.
- **As shipped (G3 PR-2, v0.8.0): plural acronyms.** The converter keeps an acronym in one word, plural or not: a lower-case `s` that ends a segment or is followed by an upper-case letter belongs to the acronym, so `criticalVMs` is `CRITICAL_VMS` (not `CRITICAL_V_MS`), `tokenIDs` is `TOKEN_IDS`, and `plugins.proxmox.criticalVMs.ids` is `DASH_PLUGINS_PROXMOX_CRITICAL_VMS_IDS` (a JSON list of integers: `'[101,102]'`). An acronym followed by a word still splits before the word (`HTTPServer` is `HTTP_SERVER`). The fix is in the scaffold, before any key needs it, and **no existing name changes**: `dash config env` is byte-equal to v0.7.0's for the keys v0.7.0 had. A list of integers has its own row in the generated reference (`docs/configuration.md`) and its own error wording. `DASH_PLUGINS_HARDWARE_HOSTS` takes the new keys `bays`, `fanMaxRPM` and `sensorAliases` inside an entry (an unknown field of a list entry is an error, so v0.7.0 rejects the v0.8.0 value: rollback needs the image and the environment together).
- An unknown `DASH_PLUGINS_*` variable is a startup error (typo protection). Other unknown `DASH_*` variables produce a warning. `DASH_API` is used by the Vite dev proxy (`web/vite.config.ts:13`) and must not trip the check.
- `DASH_<NAME>_FILE=/path` is resolved in Go when the env layer loads, not in a shell entrypoint: the image is distroless and has no shell. Grafana does it in its `run.sh` entrypoint, so `GF_*__FILE` does not work on its distroless images ([run.sh](https://raw.githubusercontent.com/grafana/grafana/main/packaging/docker/run.sh)). Setting both `DASH_X` and `DASH_X_FILE` is a startup error naming both variables, as in Grafana's `run.sh`.
- A generated reference table lives at `docs/configuration.md`, produced by `dash config env --markdown` (P2.2); `make check` fails when it drifts. `dash config schema` prints the JSON Schema 2020-12 of the whole tree (reflected by the in-house `internal/jsonschema`, which also types the OpenAPI document; no `invopop/jsonschema`), and `dash config validate [file]` resolves a file plus the environment exactly as startup does and prints every problem with its path; it reads no `file:`/`env:` secret reference unless `--resolve-secrets` is given.

### B.4 Example config file

```yaml
# /etc/dash/config.yaml (Kubernetes ConfigMap) or /data/config.yaml (Docker volume)
server:
  publicURL: https://dash.example.com
  listen: 0.0.0.0:8080
  healthListen: 0.0.0.0:8081
auth:
  mode: proxy                      # local | oidc | proxy | none
  proxy:
    trustedProxies: [127.0.0.1/32, ::1/128]
    adminUsers: [alice]
settings:
  ui: readonly                     # editable (default) | readonly (GitOps)
plugins:
  prometheus:
    url: http://prometheus.monitoring.svc:9090
  alertmanager:
    url: http://alertmanager.monitoring.svc:9093
    actions: { enabled: true }
  proxmox:
    url: https://pve.example.com:8006
    node: pve
    tokenID: dash@pve!dash
    tokenSecret: file:/run/secrets/proxmox-token-secret
    tls: { caFile: /etc/dash/pve-ca.pem }
    criticalVMs: { tags: [critical] }
  kubernetes:
    conventions: { protectedLabel: example.com/protected }
    actions: { enabled: true }
  flux: {}
  grafana:
    url: https://grafana.example.com
  launcher:
    links:
      - { name: Proxmox, href: "https://pve.example.com:8006", icon: proxmox }
```

### B.5 Back-compat mapping for existing `DASH_*` variables

Decided (J.5): accept the legacy names as aliases for one release, logging one deprecation warning per variable at startup, then remove them. Only one deployment (the homelab) uses them; a direct cutover without the alias layer was rejected. **Done in v0.5.0 (G2):** v0.4.0 was the alias release. The aliases are gone (`internal/config/env.go`): a process that still sets one of the 24 legacy names (or its `_FILE` form) refuses to start and the error names each replacement, because silently ignoring `DASH_VM_URL` would leave a source unconfigured and ignoring `DASH_PUBLIC_URL` gives an unrelated error. `KUBECONFIG` stays as a documented exception. The table below is kept as the mapping; the error table itself is deleted one release later.

| Legacy (`config.go`) | New path | Change in semantics |
|---|---|---|
| `DASH_PUBLIC_URL` | `server.publicURL` | none |
| `DASH_LISTEN` | `server.listen` | container default becomes `0.0.0.0:8080` (D.5) |
| `DASH_HEALTH_LISTEN` | `server.healthListen` | none |
| `DASH_ALLOWED_HOSTS` (new) | `server.allowedHosts` | none; extra Host names the API answers besides the `server.publicURL` host (`["*"]` accepts any). Added by the hardening PR (branch `fix/api-hardening`) as a comma-separated variable; the replacement `DASH_SERVER_ALLOWED_HOSTS` is a JSON list (`["dash.example.com"]`), so the value changes form |
| `DASH_DEV`, `DASH_DEMO` | unchanged env names (process modes); `DASH_DEMO` can also be set as a config flag (decision J.15) | opt-in, off by default; demo uses each plugin's `Demo()` |
| `DASH_VM_URL` | `plugins.prometheus.url` | **No default**: today `vmsingle-vm.monitoring.svc:8428` |
| `DASH_ALERTMANAGER_URL` | `plugins.alertmanager.url` | **No default** |
| `DASH_VLOGS_URL` | `plugins.victorialogs.url` | **No default** |
| `DASH_GATUS_URL` | `plugins.gatus.url` | **No default** |
| `DASH_PROXMOX_URL`, `DASH_PROXMOX_NODE` | `plugins.proxmox.url`, `.node` | none |
| `DASH_PROXMOX_TOKEN` (`user!token=secret`) | `plugins.proxmox.tokenID` + `.tokenSecret` | Split, so that only the secret is secret; the old single value is no longer parsed, so split it at the first `=` into `DASH_PLUGINS_PROXMOX_TOKEN_ID` and `DASH_PLUGINS_PROXMOX_TOKEN_SECRET` |
| `DASH_HOST_NODENAME`, `DASH_HOST_MODEL` | `plugins.hardware.hosts[0].nodename`, `.model` | becomes a list |
| `DASH_HEADLAMP_URL`, `DASH_HEADLAMP_CLUSTER` | `plugins.headlamp.url`, `.cluster` | none |
| `DASH_GRAFANA_URL` | `plugins.grafana.url` | none |
| `DASH_LAUNCHER_FILE` | `plugins.launcher.file` | inline `links` also allowed |
| `DASH_LABEL_PROTECTED` | `plugins.kubernetes.conventions.protectedLabel` | **Default empty** (today `homelab.io/protected`) |
| `DASH_LABEL_TENANT` | `plugins.kubernetes.conventions.tenantLabel` | **Default empty** |
| `DASH_APPS_FLUX_PREFIX` | `plugins.kubernetes.conventions.appsFluxPrefix` | **Default empty** (today `app-`) |
| `DASH_ANNOTATION_SUSPEND_REASON` | `plugins.flux.suspendReasonAnnotation` | **Default empty** |
| `DASH_ZFS_SCRUB_METRIC_PREFIX` | `plugins.hardware.zfsScrubMetricPrefix` | **Default empty** (scrub panel hidden) |
| `DASH_NAS_BACKUP_NAMESPACE`, `DASH_NAS_BACKUP_CRONJOB` | `plugins.kubernetes.backupJobs: [{namespace, cronjob, label}]` | **Default empty**; becomes a list |
| `KUBECONFIG` | `plugins.kubernetes.kubeconfig` (still honours `KUBECONFIG`) | none |

`DASH_DEV` is loopback-only (dash refuses to start with it on a non-loopback `DASH_LISTEN`), and `DASH_DEMO` no longer implies dev: both fixed by the hardening PR (branch `fix/api-hardening`; clients-and-ai.md K.1).

Sequencing risk: if the homelab deployment relies on today's `.svc` defaults, removing them blanks Prometheus, Alertmanager, VictoriaLogs and Gatus. The leaf that removes the defaults (P1.3 in the epic) is blocked on the homelab manifest setting those values explicitly first.

### B.6 Hot reload

| Change | Detection | Effect |
|---|---|---|
| UI save | API call | Validate, write `ui-settings.yaml` atomically (B.7) with a revision/ETag, then apply |
| Config file | Poll every 10s on mtime, size and SHA-256. This handles the ConfigMap symlink swap; inotify does not see it reliably | Parse and validate the whole tree. If invalid, keep the running config, log an error, and show "config file invalid: ..." in Settings; `/readyz` is unaffected |
| `file:` secret ref | Same poll, per referenced file | Reconfigure only the plugins that use it. Kubernetes Secret volume updates propagate except with `subPath` mounts; document that |
| `k8s:` secret ref | Informer on the named Secrets only | Reconfigure the affected plugins |
| Env var | Not detectable | Needs a restart; documented |
| `x-dash-restart` field | Any source | Saved, and Settings shows "restart required"; nothing is applied live |

**Apply** diffs the old and new resolved config for each plugin. An unchanged plugin is untouched. A changed one goes through `Stop` (cancel its context, wait up to 10s) and then `Start` with the new config. Its last published events stay in the hub until the new instance publishes, so the UI does not flicker. If the plugin is now disabled, its events are cleared (A.7). In G2 the supervisor does this for plugins that can be restarted in-process; the legacy shims hosted by `topics.Manager` are `RestartOnly`, so a change to their sections is still logged as "applies after a restart" until their G3 leaf (`config.ReloadOptions.Live`, `internal/config/reload.go`).

**As shipped (G3 PR-2, v0.8.0).** `proxmox` and `hardware` apply live, so only `kubernetes` and `flux` are still `RestartOnly`. A change restarts the plugin and the plugins that use or require it: a `plugins.prometheus.*` change restarts proxmox and hardware, a `plugins.gatus.*` change restarts hardware, and a `plugins.proxmox.*` change restarts hardware (and re-runs node discovery when `node` is unset). The `enabled` doc text (the schema, `docs/configuration.md`, the README row and the env table footers) names these pairs, and `TestEnabledDocNamesTheUsers` derives them from the built-in manifests, so a missing pair or a stale list of shims fails the build. The proxmox leaf named only the pairs among real plugins ("a prometheus change restarts proxmox"), because the hardware shim never restarted and "a proxmox change restarts hardware" would have been false until the hardware leaf; that leaf added the three hardware pairs. A change in the environment still needs a restart, as for every plugin.

**As shipped (G3 PR-3a, v0.9.0).** `kubernetes` applies live, so `flux` is the only `RestartOnly` plugin left. A change to `plugins.kubernetes.*` from a config file restarts the plugin (the informers re-sync: its row is `starting` for a few seconds and its topics disappear and reappear) and **restarts proxmox**, which `Uses` it (node discovery re-runs when `node` is unset); a `plugins.prometheus.*` change now also restarts kubernetes, and so does a `plugins.gatus.*` change. The `enabled` doc text and `TestEnabledDocNamesTheUsers` name the pairs (prometheus restarts proxmox, hardware and kubernetes; gatus restarts hardware and kubernetes; proxmox restarts hardware; kubernetes restarts proxmox; grafana restarts alertmanager). A `k8s:` reference that a reload adds marks its setting as needing a restart (as a literal added after startup does), but the consumer starts live through `K8sRefs.OnChange` (C.3). A change in the environment still needs a restart.

### B.7 Settings store format (decision J.3, decided)

| | YAML file (`ui-settings.yaml`) + `audit.jsonl` (decided) | SQLite (`modernc.org/sqlite`, pure Go, still CGO-free) |
|---|---|---|
| Plain volume | yes | yes |
| Human-readable and diffable backups | yes | no |
| Concurrency | single writer under a mutex; an `flock` on `/data/.lock` refuses a second instance | real transactions |
| Sessions and audit queries | stateless signed cookies (E.3); JSONL with rotation | tables |
| Footprint | `sigs.k8s.io/yaml` (already a dependency, `go.mod`) | new dependency of a few MB |

**Decided (research, 2026-10-31):** no database. dash keeps one YAML store, `$DASH_DATA_DIR/ui-settings.yaml`, with atomic writes. SQLite is added only if dash needs multiple users with roles, server-side sessions or a searchable audit log. It would then use `modernc.org/sqlite` (pure Go, CGO-free; [doc.go](https://raw.githubusercontent.com/modernc-org/sqlite/master/doc.go)) in a separate `/data/dash.db`, never mixed into the YAML store. bbolt is skipped: it has no migrations or query language, `Open` blocks on the file lock unless a timeout is set, and it is single-process like SQLite, so it offers nothing over SQLite here except a smaller binary ([bbolt README](https://raw.githubusercontent.com/etcd-io/bbolt/main/README.md)). The store is behind a `settings.Store` interface. Without a writable data dir, the UI is read-only and shows a banner explaining why.

The store file:

- **Name.** `ui-settings.yaml`, so it is not confused with the operator's `config.yaml`. It is sparse (B.2).
- **Schema version.** A top-level `schemaVersion` field. On load, ordered migration functions upgrade older versions before use. Home Assistant's versioned `.storage` envelope is the precedent ([storage.py](https://raw.githubusercontent.com/home-assistant/core/dev/homeassistant/helpers/storage.py)).
- **Atomic write.** Write a temp file in the same directory, fsync the file, rename it over `ui-settings.yaml`, then fsync the directory. renameio skips the directory fsync ([tempfile.go](https://raw.githubusercontent.com/google/renameio/master/tempfile.go)), so dash does this itself.
- **Lock.** `flock` on a separate, never-replaced `/data/.lock`, which refuses a second instance. A lock on `ui-settings.yaml` itself would follow the old inode after the rename.
- **Backups.** Each write first rotates the last 5 copies, `ui-settings.yaml.bak.1` (newest) to `ui-settings.yaml.bak.5`. Clearing or replacing a secret rewrites every `.bak.N` copy without the old value (P1.5), so a restore cannot bring back a cleared secret.

**`credentials.yaml`.** Credential state lives in a second file, `$DASH_DATA_DIR/credentials.yaml`: mode 0600, `schemaVersion`, the same atomic write and writer mutex, no `.bak` rotation, so a restore can never bring back a revoked credential. Still no database (J.3). P4.3 creates it for per-user session epochs; P7.1 adds personal access tokens and OAuth grants; P7.7 adds push subscriptions and the VAPID key pair. Details: clients-and-ai.md M.7.

---

## C. Secret handling

### C.1 Never sent to the browser

`GET /api/settings` returns metadata only:

```json
{ "path": "plugins.proxmox.tokenSecret", "secret": true,
  "set": true, "source": "file", "ref": "file:/run/secrets/proxmox-token-secret",
  "locked": true, "updatedAt": "2026-10-31T10:00:00Z" }
```

`ref` shows *where* the value comes from (env var name, file path, Secret name): useful to admins and not secret. The value is never serialised. `secret.Value.MarshalJSON` emits only this metadata, so an accidental `writeJSON(cfg)` cannot leak it.

### C.2 Write-only fields in the UI

Secret fields render as "Set (file) · updated 2 days ago" with two buttons, **Replace** and **Clear**. The current value is never shown and never read back. `PUT /api/settings/plugins/{id}/secrets/{field}` with `{value}` replaces it; `DELETE` clears it. Both are refused server-side with `409 Conflict`: code `locked_field` if the field is locked (set by file or env), or code `readonly_settings` if settings are read-only (error envelope and codes as in B.2, the source of truth). "Test connection" (`Checker`) can use a candidate value from the form without saving it. Neither "Test connection" nor save ever sends a stored secret to a URL or host other than the saved one: if the form changes the URL or host, the secret must be entered again in the same form (clients-and-ai.md K.22).

### C.3 Secret references

| Form | Example | Resolver | Notes |
|---|---|---|---|
| `env:NAME` | `env:PVE_TOKEN_SECRET` | process env | Good for PaaS secret managers that inject env |
| `file:/path` | `file:/run/secrets/proxmox` | file read, trimmed | Docker/Compose `secrets:`, Kubernetes Secret volumes, Vault Agent, External Secrets |
| `k8s:namespace/name#key` | `k8s:dash/proxmox#tokenSecret` | `kubernetes` plugin | Needs RBAC `get` on that Secret only (`resourceNames`); unavailable without a cluster |
| `op://...` | | **not built in** | Use `op run`, the 1Password Connect operator or External Secrets to materialise it as env or a file. dash does not embed vendor SDKs |

**As shipped (G3 PR-3a, v0.9.0):** `k8s:<namespace>/<name>#<key>` is accepted in every secret field (`plugins.{prometheus,alertmanager,gatus,victorialogs}.auth.bearerToken` and `.auth.basic.password`, `plugins.proxmox.tokenSecret`), from the file and the environment layers. `secret.Resolve` stays strict (a `k8s:` reference without a registry is an error); `config.Sources.K8s`, a `secret.K8sRefs` made once per process in `run`, hands out the holder of each reference: the same `secret.Value` on every resolution and reload, a pending handle (a reference and no value, `Value.Pending()`) until the kubernetes plugin fills it, and already in the scrub registry. The syntax is checked at load (RFC 1123 label, subdomain and `[-._a-zA-Z0-9]+` key; an error names the field and layer, never the reference); `dash config validate` checks the syntax and reads nothing. The kubernetes plugin reads the Secrets (`internal/plugins/kubernetes/secrets.go`): one goroutine per distinct Secret, a `get` and then a watch with `fieldSelector=metadata.name=<name>` from that `resourceVersion` (a 410 starts over), copying only the referenced keys and keeping no object; RBAC is `get`, `list` and `watch` with `resourceNames` in the Secret's namespace. A plugin whose `Configured` needs the value is not configured while the handle is pending, and its row ends `; waiting for a Kubernetes Secret (see plugins.<id>.<field>)`; namespace, name, key and reason (`kubernetes_unavailable`, `not_found`, `key_missing`, `forbidden`, `unreachable`) go to the log once per change. `K8sRefs.OnChange` (a first fill, a changed reason or a reference seen for the first time; not a rotation) runs one `Supervisor.Reload`, so the consumer starts through the normal path (the contract text names `OnFill`, which exists too and fires on the first fill only). A deleted Secret keeps the last value; a stopped plugin leaves the values and sets `kubernetes_unavailable`. A reload that adds a `k8s:` reference is a `Change` and, like a literal added after startup, marks the setting as needing a restart, but the consumer starts live. The UI layer has no settings editor in this release, so the draft's `k8s:` refs entered in the UI are not reachable yet; the values are held in memory only.

**As shipped (G3 PR-3a, v0.9.0), where the epic's acceptance changed (O-2, option A).** The epic asked for a load error naming the field when a `k8s:` reference is used while kubernetes is `unavailable`. Resolution runs before any plugin exists and cannot see plugin state, so that error cannot be produced. What shipped: **the load accepts a well-formed reference** (a malformed one fails the load and names the field and the layer, never the reference), and **the consumer's row carries the generic message from startup** instead of a load error: `waiting for a Kubernetes Secret (see plugins.<id>.<field>)`, in state `disabled` when its `enabled` is unset and `unconfigured` with `enabled: true`. It is a runtime message, not a load error, and it names no namespace, name, key or reason (viewers see categories only; the log has the detail). `dash config validate` (with or without `--resolve-secrets`) checks the syntax only, reads nothing and never echoes the reference.

**Optional credentials start without the value (a deviation from "a pending secret is not configured").** A plugin is configured on its `url` alone when its credential is optional, which holds for the `auth` fields of prometheus, alertmanager, gatus and victorialogs. With a `k8s:` bearer token that has not arrived, such a plugin starts and queries without the credential until the value is filled, and shows no pending message; only a plugin that cannot run without the value (proxmox's `tokenSecret`) waits with the pending row. The README says so. Making an optional credential hold its plugin back would change each plugin's `Configured`; that was not done. `TestK8sRefFillStartsTheConsumer` covers the waiting case (the supervisor level in `internal/plugin`, and the process level in `cmd/dash` with the real proxmox plugin).

**RBAC and what lives in memory.** The reading is fenced by RBAC alone. The operator grants `get`, `list` and `watch` with `resourceNames` on the named Secrets in a namespaced `Role` and `RoleBinding` (the README has the example); a `list` or `watch` restricted by `resourceNames` is authorised only when it carries the matching `metadata.name` field selector, which the plugin always sends, and it never lists or watches Secrets without one (`TestSecretsAreNeverListedUnfiltered`). dash can read nothing else. The value is held in memory only, in a `secret.Value` that prints `[redacted]`, is scrubbed from logs and errors from the moment its handle exists and is never in a topic, the audit log or a `SourceStatus`. A reference removed by a reload keeps its holder and its one watcher until the process ends (bounded by the references ever seen); `file:` rotation still restarts its consumer, as before.

### C.4 Encryption at rest (values entered in the UI)

- Format: `enc:v1:<kid>:<base64(nonce24 || ciphertext || tag)>`, using XChaCha20-Poly1305 (`golang.org/x/crypto/chacha20poly1305`), a random 24-byte nonce, and the field path as AAD so a ciphertext cannot be moved to another field.
- Key: 32 bytes from `DASH_SECRET_KEY` (base64) or `DASH_SECRET_KEY_FILE`. There is never a default or auto-generated key; Grafana's static default `secret_key` is the example to avoid ([configure-grafana](https://raw.githubusercontent.com/grafana/grafana/main/docs/sources/setup-grafana/configure-grafana/_index.md)).
- **No key:** UI-entered secrets are disabled. Secret fields render "set via env/file only", and their write endpoints return 409 naming the missing key. The rest of dash runs, including `env:`, `file:` and `k8s:` refs.
- **Wrong or missing key with stored secrets:** if `ui-settings.yaml` holds any `enc:v1` value that the configured key cannot decrypt, or no key is set, dash refuses to start and lists the affected field paths. It never drops, blanks or regenerates a stored value. Recovery is restoring the key, or deleting the listed entries and entering them again.
- age (filippo.io/age) was considered. It is built for file and recipient workflows. XChaCha20-Poly1305 with a key ID is smaller and does exactly this job.
- **Rotation:** set `DASH_SECRET_KEY` to the new key and `DASH_SECRET_KEY_PREVIOUS` to the old one. On startup dash decrypts with either, re-encrypts every value with the new key, rewrites the store atomically, and audits `secrets.rekey`. Remove the previous key afterwards.

### C.5 Rotation of referenced secrets

`file:` and `k8s:` refs are watched (B.6). A change reconfigures only the consuming plugin. `env:` refs and literals need a restart.

### C.6 Redaction

- `secret.Value` implements `String()`/`GoString()` returning `[redacted]`, `slog.LogValuer`, and `MarshalJSON` (metadata only). A plain `string` never holds a secret after config load.
- A redacting `slog.Handler` scrubs every currently resolved secret value from messages and string attributes. That covers secrets that leak through third-party error text.
- The same scrubber runs on `SourceStatus.Error` before publishing (`manager.go:189-199` today), and on `writeErr` messages built from upstream errors (`internal/api/read.go:47-50,104-117`; `silences.go:51-103`).
- `Host.HTTPClient` puts credentials in headers, never in URLs (as Proxmox already does at `internal/upstream/proxmox.go:86`), so `url.Error` text stays clean.
- **Pattern redaction.** The slog scrubber also removes values by shape, whether or not they are currently resolved: `dash_pat_`, `dash_at_`, `dash_rt_` and `enc:v1:` followed by their token characters (clients-and-ai.md K.24). It does not match `dash_setup_`: the one-time setup line is written once at startup through a writer that bypasses the scrubber (E.2). The logs API redacts every pattern above and `dash_setup_` in every line it returns (clients-and-ai.md K.19, M.9).
- **Encodings.** Besides the exact value, the scrubber removes the base64 and URL-encoded forms of every resolved secret of 8 bytes or more.
- **Error categories.** `SourceStatus.Error` for viewers is one category: `unreachable`, `timeout`, `tls`, `auth`, `http_<code>`, `invalid_response` or `not_configured`. The detail (which can hold internal URLs) goes only to the logs and to the admin plugin status (clients-and-ai.md K.3).

### C.7 Audit log

- Every settings change, secret replace or clear, auth event (login, logout, failed login, setup) and action goes to the existing structured `audit` slog record (`actions.go:30-38`) and to `$DASH_DATA_DIR/audit.jsonl`, rotated at 10 MiB, 5 files. Token, OAuth grant and consent changes and approval decisions are recorded the same way.
- Records hold user, action, plugin, the field paths changed and their source, and the result (the outcome record; the intent record holds the same fields without it). Never values; secrets are recorded as `replaced` or `cleared`.
- Records also carry `actor{type, user, tokenID, grantID, clientID, surface: ui|api|mcp}`, the TCP `peer`, the `requestId` (clients-and-ai.md L.2) and the `authMode`.
- Denials are audited too (403, CSRF, rate limit), at most one record per peer and reason per minute.
- Each record carries a `prev` field, the SHA-256 of the previous record, forming a chain that `dash audit verify` checks.
- **Fail closed for writes, best effort for auth events.** Every audited write (an action from the UI, the API, an approval execution or MCP; a settings change; a secret Replace or Clear; a PAT, OAuth grant or consent change; an approval decision) writes an **intent** record before it changes anything and an **outcome** record after. If the intent write fails on a writable data dir, the request returns 503 with code `audit_unavailable` and nothing is changed (for an action, `Act` does not run; clients-and-ai.md K.21). A failure writing an outcome is logged at ERROR; the response still reports the real result. Auth events (logins, logouts and denials) are best effort: a failed write is logged at ERROR and does not block the login or the denial response. `dash audit verify` reports intents that have no outcome.
- `GET /api/settings/audit` (admin) serves the latest N records.

---

## D. Deployment targets

### D.1 Single `docker run` with a volume

```sh
docker volume create dash-data
openssl rand -base64 32 > dash-secret.key        # once; keep it outside the volume and back it up
docker run -d --name dash --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v dash-data:/data \
  -v "$PWD/dash-secret.key:/run/secrets/dash_secret_key:ro" \
  -e DASH_SECRET_KEY_FILE=/run/secrets/dash_secret_key \
  -e DASH_SERVER_PUBLIC_URL=http://localhost:8080 \
  ghcr.io/techdufus/dash:0.1
docker logs dash 2>&1 | grep -m1 'setup token'   # one-time admin setup URL (E.3)
```

You then configure plugins in Settings. Values are stored in `/data/ui-settings.yaml`, with secrets encrypted under the key. Without the key, dash runs but UI-entered secrets are disabled; once secrets are stored, losing the key stops dash from starting (C.4). For an infrastructure-as-code setup, add `-v ./config.yaml:/data/config.yaml:ro`, or set `DASH_CONFIG`. To use the `kubernetes` plugin from Docker, mount a kubeconfig: `-v ~/.kube/config:/kube/config:ro -e DASH_PLUGINS_KUBERNETES_KUBECONFIG=/kube/config`.

### D.2 Docker Compose

```yaml
# examples/compose/compose.yaml
services:
  dash:
    image: ghcr.io/techdufus/dash:0.1
    restart: unless-stopped
    ports: ["127.0.0.1:8080:8080"]
    environment:
      DASH_SERVER_PUBLIC_URL: http://localhost:8080
      DASH_PLUGINS_PROMETHEUS_URL: http://prometheus:9090
      DASH_PLUGINS_ALERTMANAGER_URL: http://alertmanager:9093
      DASH_PLUGINS_PROXMOX_URL: https://pve.example.com:8006
      DASH_PLUGINS_PROXMOX_NODE: pve
      DASH_PLUGINS_PROXMOX_TOKEN_ID: dash@pve!dash
      DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE: /run/secrets/proxmox_token_secret
      DASH_SECRET_KEY_FILE: /run/secrets/dash_secret_key
    secrets: [proxmox_token_secret, dash_secret_key]
    volumes: ["dash-data:/data"]

  # Optional example backends: docker compose --profile monitoring up
  prometheus:
    image: prom/prometheus
    profiles: [monitoring]
    volumes: ["./prometheus.yml:/etc/prometheus/prometheus.yml:ro"]
  alertmanager:
    image: prom/alertmanager
    profiles: [monitoring]

secrets:
  proxmox_token_secret:
    file: ./secrets/proxmox_token_secret   # git-ignored
  dash_secret_key:
    file: ./secrets/dash_secret_key        # git-ignored; openssl rand -base64 32
volumes:
  dash-data: {}
```

The example pins images by tag in docs and by digest in the CI smoke test (P5.3).

### D.3 Generic cloud and PaaS (Fly.io, Render, Cloud Run, ECS, Nomad...)

- Configuration comes from env plus a mounted file or secret. Every platform supports at least env.
- **Persistent volume available** (Fly volumes, Render disks, ECS with EFS): behaves exactly like Docker.
- **No volume** (Cloud Run by default, scale-to-zero platforms): `$DASH_DATA_DIR` is ephemeral or unwritable.
  - If the data dir is unwritable, dash forces `settings.ui: readonly` and shows a banner: "settings are read-only: no writable data directory; configure via env/config file".
  - If it is writable but ephemeral, which dash cannot detect, operators set `DASH_SETTINGS_UI=readonly`. The PaaS doc says so.
  - Local auth then needs `DASH_ADMIN_PASSWORD_HASH`, and sessions need `DASH_SECRET_KEY` so logins survive restarts.
  - Use `auth.mode: oidc` or the platform's identity proxy (`proxy` mode with the platform's header and CIDRs).
- Only one replica should hold the volume. dash keeps its hub in memory and its settings on one disk, so scaling out means read-only replicas at best (a non-goal; see Goal).

### D.4 Kubernetes

**Helm chart (decided, J.7)** at `charts/dash`. It is published as an OCI chart to `ghcr.io/techdufus/charts/dash` from the same release workflow.

```yaml
# values.yaml (excerpt)
image: { repository: ghcr.io/techdufus/dash, tag: "", digest: "" }  # tag defaults to appVersion
config: {}                  # rendered into a ConfigMap mounted at /etc/dash/config.yaml (DASH_CONFIG)
env: []                     # extra DASH_* env
secretEnv: []               # [{name: DASH_PLUGINS_PROXMOX_TOKEN_SECRET, secretKeyRef: {name: proxmox, key: tokenSecret}}]
settings: { ui: editable }  # chart default (J.4); set readonly for GitOps installs and for replicaCount > 1
secretKey:                  # required, no default: DASH_SECRET_KEY_FILE from an existing Secret
  existingSecret: ""
  key: secretKey
persistence:
  enabled: true             # PVC for /data (ui-settings.yaml, backups, audit); false => emptyDir + readonly UI
  size: 256Mi
  accessModes: [ReadWriteOnce]  # or [ReadWriteOncePod] where the CSI driver supports it
rbac:
  create: true
  read: true                # ClusterRole: get/list/watch nodes, namespaces, pods, events, PVCs, workloads, jobs,
                            # cronjobs + optional CRDs (flux, cnpg, cert-manager, external-secrets, httproutes)
  actions:
    restart: false          # Role/ClusterRole: patch deployments, statefulsets, daemonsets
    fluxReconcile: false    # patch flux kinds
  secretRefs: []            # Secret names dash may `get` for k8s: refs (resourceNames)
guardrails:
  enabled: auto             # auto renders the ValidatingAdmissionPolicy whenever any rbac.actions.* is true; true/false force it (clients-and-ai.md K.7)
auth:
  oauth2ProxySidecar: { enabled: false, args: [] }  # today's pattern; sets auth.mode=proxy, listen 127.0.0.1:8080
service: { port: 8080 }
ingress: { enabled: false }
httpRoute: { enabled: false, parentRefs: [] }
replicaCount: 1             # >1 only with settings.ui readonly and persistence off (template fails otherwise), so multi-replica means opting into readonly
```

- **Strategy:** the Deployment always uses `strategy: Recreate`. `ReadWriteOnce` is per node, so during a rolling update two pods on the same node can both mount the volume; `ReadWriteOncePod` limits it to one pod in the cluster ([access modes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/)). The `/data/.lock` flock (B.7) is the last line of defence.
- **Template guards:** rendering fails if `secretKey.existingSecret` is empty; if any `x-dash-secret` field in `config` is a literal instead of an `env:`, `file:` or `k8s:` ref (secrets come from `existingSecret`, `secretEnv` or refs, never plaintext in values); and if `replicaCount > 1` while `settings.ui` is not `readonly` or persistence is enabled. The guard stays strict: running more than one replica requires opting into `settings.ui: readonly`.
- **Settings default:** the chart ships `settings.ui: editable` (decided, J.4), so a fresh install can be configured in the UI. GitOps installs set `settings.ui: readonly`; the UI then shows everything read-only and the server rejects every settings write (B.2).
- Plain manifests: `examples/kubernetes/` is a kustomize base rendered from the chart with `helm template` and checked in CI. It is for people who do not use Helm.
- **Kubernetes Secrets are one option, not the only one:** `secretEnv` (env from Secret), `file:` refs to mounted Secret volumes, or `k8s:` refs read through the API with `rbac.secretRefs`.
- The homelab migrates to the chart, or keeps its manifests and adds a ConfigMap, and opts into `settings.ui: readonly`. That change lives in the private homelab repo and is listed as an external dependency in the epic.

### D.5 Dockerfile assessment

The current `Dockerfile` is mostly fine:

| Aspect | Today | Verdict / change |
|---|---|---|
| Multi-stage, digest-pinned bases | `Dockerfile:3,10,22` | Good. Keep, and let Renovate or Dependabot bump the digests |
| Runtime base | `gcr.io/distroless/static-debian13:nonroot` (`:22`) | Good: no shell, nonroot |
| User | `USER 65532:65532` (`:24`) | Good |
| Static binary | `CGO_ENABLED=0`, `-trimpath`, `-s -w` (`:19-20`) | Good |
| Multi-arch | `--platform=$BUILDPLATFORM` with `TARGETOS`/`TARGETARCH` cross-compile (`:3,10,17-19`) | **Ready.** The final stage has no `RUN`, so `--platform linux/amd64,linux/arm64` builds without QEMU on one amd64 runner. CI never passes `--platform` today (`ci.yml:35`), so releases are amd64-only |
| `EXPOSE` | none | Add `EXPOSE 8080 8081` (documentation) |
| `VOLUME /data` | none | Add `/data`, owned by 65532 (distroless has no shell: create it in the `go` stage and `COPY --chown=65532:65532`), plus `ENV DASH_DATA_DIR=/data`. Declaring `VOLUME` makes `docker run` without `-v` create an anonymous volume, so settings survive container recreation but not `docker rm -v`; document that |
| `HEALTHCHECK` | none | No shell or curl in distroless, so add a `dash healthcheck` subcommand that GETs `http://127.0.0.1:8081/healthz` (or the port of `$DASH_SERVER_HEALTH_LISTEN`), then `HEALTHCHECK CMD ["/dash","healthcheck"]`. Kubernetes ignores it and keeps its probes |
| Listen default | `127.0.0.1:8080` (`config.go:97`) | Unreachable through `-p` in a container. The new default is `0.0.0.0:8080`, which is only safe with in-app auth (E); the sidecar chart option sets loopback explicitly |
| OCI labels | none | `org.opencontainers.image.source=https://github.com/TechDufus/dash` (links the GHCR package to the repo), plus `licenses`, `version`, `revision`; `docker/metadata-action` provides them |
| Build context | `.dockerignore` lacks `dev/` and `.github/` | Add them (audit row) |

### D.6 Feature table: what needs a cluster

"Needs a cluster" means access to a Kubernetes API. Running *inside* Kubernetes is never required: Docker with a mounted kubeconfig works.

| Feature | Code today | Needs a cluster? | Without a cluster (after this plan) |
|---|---|---|---|
| Process start | `main.go:59-62` exits without one | **Today yes**; after: no | Starts; the `kubernetes` plugin shows "unavailable: no in-cluster config or kubeconfig" |
| `/readyz` | informer sync (`manager.go:105`) | **Today yes**; after: no | Ready once plugins have started (A.6) |
| Cluster pages: nodes, workloads, pods, events, namespaces, certificates, External Secrets | informers `kube.go:119-165`; `topics/cluster.go` | yes | `kubernetes` plugin unavailable; nav, routes and palette groups hidden |
| Pod detail sheet | `cluster.go:365-412`; `/api/pods/...` | yes | Hidden with the cluster pages |
| Node CPU/mem series (`node.*`) | `read.go:30-39` resolves InternalIP via informers | yes (+ prometheus) | Series not registered |
| Pod CPU/mem series (`pod.*`) | cAdvisor metrics only | Prometheus only, but the only UI entry is the pod sheet | Hidden with the pod sheet |
| Rollout restart | `actions.go:58-107` | yes (+ RBAC patch) | Action not mounted |
| Flux GitOps page and reconcile | `topics/flux.go`; `actions.go:109-134` | yes (+ Flux CRDs) | `flux` plugin `blocked` (needs kubernetes) or `unavailable` (CRDs absent) |
| Apps discovery (HTTPRoute + `gethomepage.dev/*`) | `feeds.go:99-150` | yes (+ Gateway API CRD) | The Apps page shows `launcher` links only |
| Namespace groups and topology groups | `cluster.go:91-117`; `overview.go:75-140` | yes | Topology shows host and VMs only |
| PVCs fill and growth, etcd size | `storage.go:80-112`; `queries.go:30-47` | PVC list yes; etcd Prometheus only | PVC panel hidden; etcd panel needs the kubernetes plugin's query pack |
| CNPG clusters and backups | `kube.go:37`; `storage.go:139-158` | yes (+ CNPG CRD); backup age needs Prometheus | `cnpg` plugin blocked or unavailable |
| Backup CronJob age (today "NAS backups") | `queries.NASBackupAge` (kube-state-metrics) | Prometheus only | Lives in `kubernetes.backupJobs`, so hidden without the plugin. Backup freshness from Prometheus alone, without cluster access, is deferred post-v1 (decision J.13) |
| `.svc` service-proxy transport | `transport.go:15-75`, dev only | yes (dev only) | Moves into the `kubernetes` plugin as an opt-in `upstreamViaAPIServer` for dev or out-of-cluster use (needs `services/proxy`, a powerful verb; off by default) |
| `k8s:` secret refs | new | yes | Resolution error at load: "k8s: refs need the kubernetes plugin"; use `env:`/`file:` |
| Pod logs via VictoriaLogs | `internal/upstream/vlogs.go:38-75` queries `kubernetes.pod_*` fields | VictoriaLogs only (no API access), but the entry point is the pod sheet | `victorialogs` healthy, but no page: a standalone logs page is deferred post-v1 (decision J.13) |
| Proxmox VMs, tasks, VM detail, consoles | `proxmox.go`; `vms.go` | no | Works; VM-to-node badge absent |
| Host hardware (IPMI, hwmon, ZFS, SMART), machines | `host.go`; `queries.go:62-118` | no (Prometheus) | Works |
| Alerts and silences | `alertmanager.go`; `silences.go` | no | Works |
| Service status (Gatus) | `gatus.go`; `feeds.go:46-73` | no | Works |
| Grafana links, launcher | `links.go:103-111`; launcher file | no | Works |
| Headlamp deep links | `links.go:52-74` | Only meaningful with cluster objects | Plugin enabled, but no objects to link |
| Overview KPIs, attention, topology | `overview.go`; `attention.go` | no | Composed from whatever is enabled |
| Demo mode | `internal/demo` | no | Unchanged in spirit; built from each plugin's `Demo()`. Opt-in via `DASH_DEMO` or a config flag, off by default; no separate demo image tag (decision J.15) |

**As shipped (G3 PR-3a, v0.9.0), for the rows above.** "Rollout restart" is a framework action of the kubernetes plugin, mounted only with `actions.enabled` (A.8). "Node CPU/mem series" and "Pod CPU/mem series" are the kubernetes plugin's, answered only while it runs (A.3). "Apps discovery" is configuration with generic defaults and reads Ingresses only when `apps.ingress` is on (F.8). "`k8s:` secret refs" are **not** a resolution error at load (O-2, C.3): the load accepts a well-formed reference, a consumer that cannot run without the value shows the generic row message "waiting for a Kubernetes Secret" until the kubernetes plugin reads it, and a plugin whose credential is optional starts without it. With no cluster the kubernetes plugin is `unavailable`, the pages the table lists as hidden are hidden, and Proxmox publishes its VMs without the node flags (A.5).

---

## E. Auth for the settings page and the app

### E.1 Today

oauth2-proxy runs as a sidecar and admits only the GitHub user `TechDufus` (`README.md:26-28`). dash trusts `X-Forwarded-Preferred-Username` / `X-Forwarded-User` from any peer (`middleware.go:32-51`), which is safe only through the loopback listener. In dev, a missing header becomes user `dev` (`middleware.go:42-44`), and demo mode injects `demo` (`internal/demo/demo.go:329-345`). Every authenticated user can perform every action.

### E.2 Modes

| Mode | For | How | Notes |
|---|---|---|---|
| `local` (**default**, decided, J.6) | docker run, compose, small installs | One admin account with an argon2id hash (`golang.org/x/crypto/argon2`). Bootstrap: if no hash is stored and `DASH_ADMIN_PASSWORD_HASH` is unset, dash prints a one-time setup URL with a random token, valid 1 hour, single use. The line is written once at startup through a writer that bypasses the log scrubber (C.6), so the operator can read it; the logs API redacts the token in every line it returns (clients-and-ai.md M.9). `/setup?token=...` sets the password. `dash hash-password` prints a hash for env-only setups | Opt-in `auth.local.anonymousViewer: true` for wall displays: unauthenticated users get read-only access. Off by default (decided, J.6) |
| `proxy` | oauth2-proxy, Authelia, Authentik, Pomerium, Cloudflare Access, Tailscale serve | Configurable `userHeaders` (default today's two), `groupsHeader`, **`trustedProxies` CIDRs (default `127.0.0.1/32, ::1/128`)**. Headers from any other peer are ignored, and the request is 401. Trust is the TCP peer only: `X-Forwarded-For` never decides it. Requests with `Authorization: Bearer` ignore identity headers entirely (clients-and-ai.md M.1) | The homelab sets this mode. The sidecar keeps working unchanged, and binding `0.0.0.0` no longer allows spoofing |
| `oidc` | users with an IdP | Authorization code + PKCE (`golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3`); `issuer`, `clientID`, `clientSecret` (secret), `scopes`, `allowedUsers` / `allowedGroups`, `adminGroups`, `roleClaim` | Session cookie as in E.3 |
| `none` | local-only trials | Everyone is admin. A WARN on every startup and a persistent red banner in the UI. Docs say to publish only to `127.0.0.1` | Requires explicit `DASH_AUTH_MODE=none`; never the default |

### E.3 Sessions and CSRF

- `local` and `oidc` use a cookie, `__Host-dash_session` when the public URL is https (`dash_session` on http, where `Secure` and the `__Host-` prefix are unavailable): an AEAD-sealed payload (user, the groups and role-claim value captured at login (OIDC; local users have none), expiry, per-user session epoch) under a key derived via HKDF from the secret key (C.4). The payload holds no role. On every request the role is derived from the *current* mapping (`adminUsers`, `adminGroups`, `roleClaim`, E.4) applied to those captured values (in `proxy` mode, to the request's headers), so a mapping change applies on the next request; a group or claim change at the IdP applies at the next login (clients-and-ai.md M.8). PATs and OAuth grants record the creator's groups and role-claim value at creation and derive the role the same way; scopes then narrow it (clients-and-ai.md M.5, M.6). Tokens follow the identity provider: whenever dash sees a user interactively (a login in `local`/`oidc` mode; any request in `proxy` mode), it rewrites the groups and role-claim value recorded on that user's PATs and OAuth grants, only when they changed (no write otherwise). "Log out everywhere" also revokes the user's OAuth grants; PATs are revoked separately in Settings → Tokens, and admins can list and revoke any user's PATs and grants. Removing a user at the identity provider or proxy does not reach tokens that user no longer uses interactively, so offboarding includes revoking their dash PATs and grants. Without `DASH_SECRET_KEY`, the session key is random per process, so logins do not survive a restart (D.3). The cookie is `HttpOnly; Secure` (unless the public URL is http); `SameSite=Lax`; 12h idle and 7d absolute expiry. Each user has an epoch, bumped by "log out everywhere" and by a password change; the global epoch stored in settings remains. Epochs are kept in `credentials.yaml` (B.7), created in P4.3. No server-side session table is needed.
- CSRF stays as it is: `X-Dash-Csrf: 1` plus `Origin == PublicOrigin()` on every non-GET request (`middleware.go:18-29`; the client already sends the header, `web/src/api/client.ts:36`). Login and setup POSTs get the same Origin check. The check applies to every non-bearer request (session, proxy, dev, demo and anonymous principals); bearer principals skip it, because a browser never attaches a bearer token on its own (clients-and-ai.md M.2).
- The 401 path in the SPA (`client.ts:24-32`, `stream.ts:40-47`) becomes mode-aware: `proxy` reloads, as today; `local`/`oidc` redirect to `/login?next=...`.

### E.4 Roles

| Role | Can |
|---|---|
| `viewer` | read every enabled page, the SSE stream, series and logs |
| `admin` | everything a viewer can, plus plugin actions (`MinRole` per action), plus `/api/settings/*` and the audit log |

Role mapping:

- `local`: the account is admin; anonymous viewer if enabled.
- `proxy`: `adminUsers` / `adminGroups`. If both are empty, every authenticated user is a **viewer** (fail closed; clients-and-ai.md K.2). `adminUsers: ["*"]` restores everyone-is-admin, today's behaviour, explicitly and with a startup warning.
- `oidc`: `adminGroups` / `roleClaim`.

Only `viewer` and `admin` exist in v1. A third `operator` role (actions but not settings) was rejected for v1 (decision J.12).

Tokens narrow the role ceiling with scopes and a single audience; the effective permission is role ∩ scopes ∩ feature switches (clients-and-ai.md M.3–M.4).

### E.5 Route groups

```
/api/v1/** (GET: me, stream, series, topics, context, plugins, plugins/{id}/...)  principal -> viewer (tokens: `read`; log routes: `logs`)
/api/v1/plugins/{id}/actions/{name} (POST)                                       principal -> actions:<plugin> scope -> MinRole -> csrf -> rate limit -> ... -> audit intent -> Act -> audit outcome (A.8)
/api/v1/settings/**, /api/v1/tokens/**, /api/v1/oauth/consents/**                interactive only -> admin where applicable -> csrf -> audit (bearer: 403)
/api/v1/approvals/**                                                             interactive or `approvals` token -> csrf (non-bearer) -> audit
/api/v1/push/**                                                                  interactive or `push` token
/mcp                                                                             mcp-audience bearer only
/oauth/authorize                                                                 interactive (authenticates through the auth mode)
/oauth/token, /oauth/revoke, /.well-known/*                                      public
/login, /setup, /auth/callback, /auth/logout                                     unauthenticated, Origin-checked POSTs
SPA routes /consent/*, /approve/*                                                interactive
/healthz, /readyz                                                                health listener only (unchanged)
```

Settings API (paths shown without the `/api/v1` prefix they get in P2.10):

- `GET /api/settings`: the effective tree, resolved at read time, with per-field `{value | secret-meta, source, locked}`.
- `GET /api/settings/schema`
- `PUT /api/settings/plugins/{id}`: JSON merge patch with `If-Match: <revision>`; 412 on conflict; 409 `locked_field` if it touches a locked path, or `readonly_settings` if settings are read-only (error envelope as in B.2).
- `POST /api/settings/plugins/{id}/test`
- `PUT|DELETE /api/settings/plugins/{id}/secrets/{field}`
- `DELETE /api/settings/plugins/{id}/stored/{field}`: "remove stored value" for a shadowed field (B.2).
- `GET /api/settings/export`: the admin-saved UI keys as a `config.yaml` snippet, secrets as refs or placeholders (B.2).
- `GET /api/settings/audit`

### E.6 Default for open source (decision J.6)

Default `auth.mode=local` with bootstrap. It is the only mode that is safe on `0.0.0.0` with zero extra infrastructure. Document `proxy` (with the homelab's oauth2-proxy sidecar as the worked example) and `oidc` as options, both first-class. The anonymous read-only viewer is opt-in and off by default. The homelab sets `DASH_AUTH_MODE=proxy` in the release that introduces auth modes.

### E.7 Clients beyond the browser

Everything above authenticates a browser. Non-browser clients (a native Android app, AI agents over MCP) need credentials that are not cookies or forwarded headers: personal access tokens, a built-in OAuth 2.1 server, scopes and audiences that narrow the role, human approval for agent writes, and push notifications. They are specified in [`clients-and-ai.md`](clients-and-ai.md) sections M (principals, tokens and OAuth), N (MCP), O (approvals) and P (mobile), and implemented in phase 7 (I).

---

## F. Migration of each integration into a plugin

### F.0 Order and rationale

| # | Plugin | Why here |
|---|---|---|
| 1 | `prometheus` | A service with no topic of its own. Almost everything consumes it (host, cluster usage, storage, VM series), so it must exist before any consumer moves. It proves the `Service` lookup and the series registry |
| 2 | `grafana`, `headlamp`, `launcher` (links) | `links.Builder` (`internal/links/links.go`) is called by most builders. Turning it into a provider registry first means no later migration touches link code twice. Trivial plugins, so they prove settings and schema end to end |
| 3 | `alertmanager` | One topic, one upstream, two actions. Proves the action framework, roles and audit on the simplest write |
| 4 | `gatus` | One topic. Proves an optional service consumed by others (apps health, machines) |
| 5 | `victorialogs` | A capability (logs) with plugin-scoped routes and streaming (`sse.go:96-127`) |
| 6 | `proxmox` | First plugin with a secret (token) and TLS options; owns `vms`, node facts and console links |
| 7 | `hardware` | The `host` topic minus Proxmox facts; a query pack plus machines |
| 8 | `kubernetes` | The largest piece: makes the cluster optional, adds discovery-gated informers, owns cluster, apps, PVC storage, restart and `k8s:` refs. It goes after the leaf plugins, so the old Manager shrinks to only Kubernetes-backed code before this cut |
| 9 | `flux` | Requires kubernetes; reconcile action; removes the `flux-system` literals |
| 10 | `cnpg`, `rustfs` | Storage packs that depend on kubernetes and/or prometheus |
| 11 | cleanup | Delete `internal/topics` legacy code, `model.Topic` constants and legacy routes; `internal/demo` becomes per-plugin `Demo()` |

During Phase 3 the not-yet-migrated code keeps running inside `topics.Manager`. It receives migrated services from the registry. Each migration PR deletes its code from the Manager, renames its topic, and keeps `DASH_DEMO=true` rendering. That is a staged cutover, not a shim: nothing old survives Phase 3. Every plugin that declares actions gets `actions.enabled`, default `false` (A.8, J.11).

### F.1 `prometheus` (Prometheus-compatible: Prometheus, VictoriaMetrics, Thanos, Mimir)

- **From:** `internal/upstream/prom.go`; `internal/queries/queries.go` (the selectors move to the consuming plugins).
- **Config:** `url`, `timeout` (default 5s, as in `prom.go:27`), `auth.basic {username, password*}` or `auth.bearerToken*`, `headers` (e.g. `X-Scope-OrgID` for Mimir), `tls {caFile, insecureSkipVerify}`. Fields marked * are secrets.
- **Topics:** none. **Service:** `Querier`. **Series:** registry host for every `SeriesSpec`. **Actions:** none.
- **k8s:** no.
- Naming (decided, J.10): the ID `prometheus`, displayed as "Prometheus-compatible", because VictoriaMetrics speaks the same `/api/v1/query` API that `prom.go:44-100` uses. The name `victoriametrics` was rejected.
- **As shipped (G3 PR-1, v0.7.0, P3.1):** `internal/plugins/prometheus`. `Configured` is `url != ""`: unset gives `disabled`, `enabled: true` without a URL gives `unconfigured`. `Validate` is `pluginconf.Prometheus.Validate`: basic auth needs both halves and excludes the bearer token, header names must be valid tokens and may not be `Authorization`, `Proxy-Authorization`, `Cookie` or `Host`. `headers` values are plain configuration, not secrets: credentials belong in `auth`. `Start` builds the client with `Host.HTTPClient` (CA, skip-verify, per-request credentials through `Authorize`, so a rotated secret applies) and `upstream.NewProm(url, hc.Transport, upstream.Timeout(t), upstream.CheckRedirect(hc.CheckRedirect))`. Passing only `hc.Transport` would lose the timeout and the same-host redirect policy, so every upstream constructor now takes `opts ...upstream.Option`. The row is `healthy` once started and never turns `degraded` or `error`: a failing query shows on the pages that ran it, as that page's source. `Service()` returns the `svc.Querier`. **Series:** `Deps.Prom` is gone. `GET /api/v1/series` validates first (range, `node` against the node index: 400 `invalid node`, 404 `unknown node`, then the series arguments: 400), then looks up the running prometheus plugin through `Supervisor.Service("prometheus")`, and without one answers 404 `not_found` `unknown series` (v0.6.0 dereferenced a nil client). A series a plugin registered is served only while that plugin runs, and it wins over dash's built-in series of the same name. The `node` argument is the one that already existed; it did not change.

### F.2 Link plugins: `grafana`, `headlamp`, `launcher`

- **From:** `internal/links/links.go:12-111`; `config.LoadLauncher` (`config.go:160-176`); `/api/config` (`api.go:155-161`).
- **grafana:** `url`, with links `alerts` and `home`.
- **headlamp:** `url`, `cluster` (default `main`), with object links for the kinds in `links.go:30-50`.
- **launcher:** `file` or inline `links[] {name, href, icon, description}` (the same validation as `config.go:170-174`). The links ship in the `/api/plugins` payload for the Apps and Overview launchers.
- **Topics/actions:** none. **k8s:** no.
- **As shipped (G3 PR-1, v0.7.0, P3.2):** the launcher's data does **not** travel in the `/api/plugins` payload (deviation from the draft). `PluginInfo` is republished on every status tick to every viewer, so a link list in it would make `core.plugins` heavier and untyped. The launcher publishes the topic `launcher.links` (payload `model.LauncherData{links}`): it is typed, has a snapshot GET and an ETag, follows the visibility rule and updates live on a reconfiguration. `GET /api/config` is removed. Validation is split: the static rules (a `file` or `links`, not both; every link has a name and an http(s) href) stay at configuration load through `pluginconf.Launcher.Validate`, so an invalid inline link rejects a load or reload and the last good configuration stays; the **file is read in `Start`** (`pluginconf.LoadLauncher`, once: a changed file applies at the next start), and an unreadable or malformed file makes the row `error` instead of aborting startup. `dash config validate --resolve-secrets` still reads the file. Icons: see A.9. grafana (`Alerts` and `Host` links) and headlamp (the Kubernetes kinds, with `cluster` defaulting to `main`) are `plugin.LinkProvider`s, and the launcher's twin publishes the five links of `world.Launcher()`.

### F.3 `alertmanager`

- **From:** `internal/upstream/alertmanager.go`; `topics/feeds.go:21-44`; `attention.go:111-124`; `api/silences.go`.
- **Config:** `url`, `auth`, `tls`, `ignoreAlerts` (default `[Watchdog, InfoInhibitor]`, `health.go:145`), `severityMap` (default `critical`→crit, `warning`→warn, `health.go:133-142`), `silenceDurations` (default `1h,4h,24h,7d`, `silences.go:18-23`), `actions.enabled`.
- **Topics:** `alertmanager.alerts`. **Actions:** `silence.create`, `silence.expire`. **Attention:** firing alerts. **KPI:** "Alerts firing".
- **Action rules:** `protectedAlerts` (default `[Watchdog]`) lists alerts that can never be silenced; a `silence.create` that matches one returns 403. `silence.expire` works only for silences whose `createdBy` starts with `dash:`, so dash never expires a silence it did not create (clients-and-ai.md K.6).
- **k8s:** no.
- **As shipped (G3 PR-1, v0.7.0, P3.3):** `internal/plugins/alertmanager`; topic `alertmanager.alerts` (`model.AlertsData`), polled every 15 s. `ignoreAlerts` (default `[Watchdog, InfoInhibitor]`) is the only filter for those names (`upstream.NewAlertmanager(url, rt, ignore, opts...)`); `severityMap` (default `critical`→`crit`, `warning`→`warn`) sets severity and health; `silenceDurations` (default `1h,4h,24h,7d`, each one of those four) reaches the dialog as `AlertsData.silenceDurations`; `protectedAlerts` (default `[Watchdog]`) marks `Alert.protected`. A `silence.create` whose `alertname` matcher names a protected alert is refused with 403, whatever the role. Silences are created as `dash:<user>`, and `Silence.dash` marks them. `silence.expire` reads Alertmanager's silence list first and refuses a silence dash did not create with 403, so **Alertmanager down makes expire fail** (502) rather than expire blind; it answers **202** `{"status":"accepted"}`. There is no `denyNamespaces` (A.8). Request bodies are decoded strictly: an unknown field is 400. The row is `starting` until the first poll ends, then `healthy`, or `error` when the poll fails (one source, so never `degraded`); `Sources = [{Name: "alertmanager"}]`. The manifest keeps the Alerts nav entry, and the alerts page moved into the module. Action configuration, the SPA gate and `Outcome`: A.8.

### F.4 `gatus`

- **From:** `internal/upstream/gatus.go`; `feeds.go:46-73`; `attention.go:216-225`; `health.go:100-109`.
- **Config:** `url`, `auth`, `tls`, `criticalGroups` (default `[]`; the homelab sets `[infrastructure]`), `pageSize`.
- **Topics:** `gatus.status`. **Service:** `EndpointHealth`. **Attention:** failing checks. **k8s:** no.
- **As shipped (G3 PR-1, v0.7.0, P3.4):** `internal/plugins/gatus`; topic `gatus.status` (`model.StatusData`), polled every 30 s; `Endpoint.ref` is `gatus:Endpoint/<Endpoint.Key>`. `criticalGroups` (default `[]`) is compared **case-insensitively**; a failing check in a listed group is critical, in any other group a warning, so with `[]` every failure warns. The Status page orders the groups by `StatusData.criticalGroups` exactly (critical groups first, then alphabetical; the old guess that sorted "Infra" first is gone). `pageSize` (default 30, greater than zero) is the number of **check results per endpoint** Gatus returns (`upstream.NewGatus(url, rt, pageSize, opts...)`), which the Status page draws as the endpoint's history; it is not a count of endpoints. The service is `svc.EndpointHealth` (A.5): `ByHost` returns the `model.Health` value (`ok`, `warn` or `crit`), which the Manager's apps map to a dot (anything but `ok` is not healthy). A failed poll keeps the previous endpoints and marks the source with the error category; the row is `starting`, then `healthy` or `error`; `Sources = [{Name: "gatus"}]`.

### F.5 `victorialogs`

- **From:** `internal/upstream/vlogs.go`; `api/read.go:93-117`; `api/sse.go:96-127`.
- **Config:** `url`, `auth`, `tls`, `fields {namespace: kubernetes.pod_namespace, pod: kubernetes.pod_name, container: kubernetes.container_name}` (today hard-coded at `vlogs.go:60-67`), `limit` (500).
- **Routes:** `GET /api/v1/plugins/victorialogs/logs`, `/logs/tail`. **Service:** `Logs`. **Topics/actions:** none.
- **Namespace policy and audit:** `denyNamespaces` (default `[]`) lists namespaces whose logs are never served (403), for queries and tails alike. Every tail open is audited. Size, duration and concurrency limits on reads and tails are fixed by the hardening PR (branch `fix/api-hardening`) and become settings in P2.11 (clients-and-ai.md K.4).
- **k8s:** no API access. Its only UI entry point is the kubernetes pod sheet. A standalone logs page is deferred post-v1 (decision J.13).
- **As shipped (G3 PR-1, v0.7.0, P3.5):** `internal/plugins/victorialogs`. The `Logs` service is **deferred to P7.3**, its first Go consumer: the pod sheet uses the HTTP routes, so no Go caller exists in G3. `fields` (defaults as above) is validated (`^[A-Za-z_][A-Za-z0-9_.]*$`), because the names are concatenated into LogsQL and must not inject it; `limit` (500) must be greater than zero. `upstream.LogFields`, `BuildQuery(q, fields, tail)` and the record reader take the mapping; the "skipped fields" rule still hides the `kubernetes.` family, derived from each configured field up to and including its first `.`. `NewVLogs(url, rt, limit, opts...)` keeps its signature and the mapping is set with `(*VLogs).WithFields`, which returns a copy. `Query` trims the answer to `limit` even when the upstream returns more. The routes are `GET logs` (400 on invalid input, a category-only 502, `[]` and never `null`) and the stream `GET logs/tail` (`Routes.Stream`, scope `logs`, target `Pod/<ns>/<pod>`, `RouteSpec.Namespace` reading `ns|namespace`, `StreamSpec.Validate` so that a bad query is a 400 before any slot or audit record). The **core** enforces and audits `denyNamespaces` (`plugins.victorialogs.denyNamespaces`, through `ReadDenyConfig`) for both routes, with 403 before any slot, and **audits every tail open** (A.8); the plugin scrubs each `Msg` with `Host.ScrubLog` before it builds the payload. The row is `healthy` while the plugin runs; a failing query answers that request. The pod sheet shows its Logs tab only while the plugin is visible, and hides it until the plugin list has loaded.

### F.6 `proxmox`

- **From:** `internal/upstream/proxmox.go`; `topics/vms.go`; `host.go:22-30`; `links.VMConsole/HostShell/ProxmoxUI` (`links.go:84-101`); `health.go:75-87`; `attention.go:187-196`; `queries.go:40-42` and the `vm.*` series (`queries.go:167-185`).
- **Config:** `url`, `node`, `tokenID`, `tokenSecret*`, `tls {caFile, insecureSkipVerify}` (self-signed Proxmox is the common case), `criticalVMs {ids: [], tags: []}` (replaces `IsTalosVM`), `storage.source: api|prometheus` (default `api`: `/nodes/{node}/storage` removes the pve-exporter dependency for the storage panel).
- **TLS warning:** `tls.insecureSkipVerify: true` (here and in every plugin with a `tls` block) logs a startup WARN and adds a `tls: verification disabled` source note (clients-and-ai.md K.11). **As shipped (proxmox):** the note is an `ok: true` entry in the plugin's `Status().Sources`, so it shows in `core.overview.sources`; it is not on the plugin's row and draws no chip.
- **Topics:** `proxmox.vms`, `proxmox.node`, `proxmox.storage`. **Routes:** `GET vms/{vmid}`. **Series:** `vm.*` when `prometheus` is enabled and pve-exporter is scraped (`series.vmSelector` default `id="qemu/{vmid}"`). **Links:** console, shell, UI. **Uses:** kubernetes (VM-to-node), prometheus.
- **Actions:** none today. VM start/stop is explicitly out of scope until requested.
- **k8s:** no. Multi-node clusters: `/cluster/resources` already lists every VM (`proxmox.go:141`), but node facts read a single `node`. `nodes: []` is future work.
- **As shipped (G3 PR-2, v0.8.0, P3.6):** `internal/plugins/proxmox`; topics `proxmox.vms` (`model.VMsData`, every VM carries `ref` `proxmox:VM/<vmid>` and `critical`; polled every 15 s), `proxmox.node` (`pvemodel.NodeData`, 30 s) and `proxmox.storage` (`pvemodel.PVEStorageData`, 60 s; `PVEStorageData` rather than `StorageData` because the OpenAPI generator qualifies a name two packages share); route `GET /api/v1/plugins/proxmox/vms/{vmid}`; series `vm.{cpu,mem,netIn,netOut,diskRead,diskWrite}`; nav `/vms`; `Uses` kubernetes and prometheus, softly, and no `Requires`.
  - **Activation and validation.** `Configured` needs `url`, `tokenID` and `tokenSecret` all set (`ConfigHint`: `plugins.proxmox.url, tokenID and tokenSecret`): **nothing set, or a URL alone, is `disabled`**, where v0.7.0 showed the console and shell links with a URL and no token. A token half without the other, or a token without a URL, is not `disabled`: `Validate` keeps the token halves rule ("tokenID and tokenSecret must be set together") and "a token needs a URL", and it is a configuration error at load that stops startup. `Validate` adds: `storage.source` is `api` or `prometheus`, every `criticalVMs.ids` entry is a valid VM id, no tag is empty, and `series.vmSelector` is **one label matcher** `<label>(=|=~)"<value>"` whose value holds no `"`, `\`, `{` or `}` except exactly one `{vmid}` (the selector goes into PromQL, so an injected selector is a config error, not a query). The settings and their env names are in the README table; only `criticalVMs.ids` has a new name shape (B.3).
  - **The HTTP client** is `Host.HTTPClient(plugin.HTTPConfig{CAFile, InsecureSkipVerify})` through `upstream.NewProxmox(url, node, tokenID, token, hc.Transport, upstream.Timeout(...), upstream.CheckRedirect(...))` (the constructor gains `opts ...Option`; the token stays a `secret.Value` read per request, so a rotation applies without a restart). **This changes behaviour:** the v0.7.0 client used the default transport and so honoured the environment proxy; the plugin client never does, refuses link-local dials, follows a redirect only to the same host and never from https to http. `tls.insecureSkipVerify: true` keeps the startup WARN of `Host.HTTPClient` and adds the source `{Name: "tls: verification disabled", OK: true}` to `Status().Sources`. The row (`PluginInfo`) has no `sources` field, so the note shows in `core.overview.sources`; it draws no chip, because chips are drawn for sources that are not ok only. The self-signed case is tested with an `httptest` TLS server that works with `caFile` and fails without it (category `tls`).
  - **Node discovery** runs after `Start` returns, on `Host.Go`, when `node` is empty: `GET /nodes`, exactly one node is taken, none or several is a config-class failure (no retry; `error`, "set plugins.proxmox.node"), and a transient failure retries from 1 s doubling to 8 s, shows `starting` for the first minute, then `error` with the category message and keeps retrying every 30 s until `Stop`. `Start` itself returns nil: an error from `Start` would show the generic "failed to start" message and retry only on a config change. A typo in `node` is therefore silent unless the row is watched. `Start` builds a nodeless client (used for `Nodes()` only) and, once the node is known, a node client over the same transport, options and token. The service answers `svc.NodeName` (A.5), non-nil from `Start`; `ok` is true from `Start` when `node` is configured (`TestNodeNameIsOKFromStartWhenConfigured`), and only an unset node waits for discovery. `cfg.ProxmoxNode` is no longer mutated at startup (the flat Proxmox fields of `config.Config`, `ProxmoxConfig`, `Deps.PVE` and `cmd/dash/proxmox.go` are deleted; `runtime.go` of the plan is `service.go`).
  - **Storage.** `proxmox.storage` takes `source: api` (default; `Storages()` is `GET /nodes/{node}/storage`, and the token needs the `Datastore.Audit` privilege on `/storage`) or `prometheus` (today's `PVEStorage*` selectors through `Host.Service("prometheus")`, byte-equal to the old builder's output for the same samples of one node). When the exporter reports the storages of several nodes (ids `storage/<node>/<name>`), only this node's are listed if any id carries its name, and a name that more than one listed storage has uses its series id as the ref, so refs never repeat. A deployment that fed the Storage page from the pve-exporter in v0.7.0 sets `source: prometheus` to keep it. The envelope `sources` of each topic name only its own source (`proxmox`, or `prometheus` for storage by prometheus); the `kubernetes` source that `vms` carried is gone. `storage.pveStorages` is `proxmox.storage.storages`, and `pages/storage.tsx` opens on `useTopicAvailable("proxmox.storage")` as well as `storage`; with both topics off it names the kubernetes plugin and the proxmox plugin with their states.
  - **Critical VMs.** A VM is `critical` when its id is in `criticalVMs.ids` or one of its tags equals, ignoring case, an entry of `criticalVMs.tags`; the default is empty, so no VM is critical until the operator says so (`IsTalosVM` and its ids are gone). A template and a running VM are `ok`; a stopped VM is `crit` when critical and `warn` otherwise. Attention items keep their ids (`vm:<vmid>`), source `proxmox` and route `/vms/<vmid>`, and gain `ref`; `Since` comes from `plugin.FirstSeen`. KPIs `vms.running` and `vms.total` carry their own health.
  - **VMs and the node index.** The first `proxmox.vms` publish waits for the node and, when the kubernetes service is present, for `NodeIndex.Synced()`. A one-second watcher polls both `NodeIndex.Revision()` and `Synced()`, because `Synced()` can turn true with no node event (`TestFirstVMsPublishFollowsSyncedWithoutARevisionChange`); a change of either rebuilds and republishes the VMs, so a node's readiness reaches `k8sNode` as fast as before. Without the kubernetes service the VMs publish without `k8sNode`.
  - **Route and links.** The route is `queries.ValidVMID` first (400 `invalid_input` without touching Proxmox), an unknown id is `*plugin.ActionError{404, not_found}` and a Proxmox failure the category-only 502 through `plugin.Fail`; the body equals v0.7.0's `/api/vms/{vmid}` after dropping `vm.ref` and `vm.critical` (`TestVMDetailBodyMatchesV07`), and `/api/vms/{vmid}` answers 404 `not_found`. The plugin is a `LinkProvider` for the kinds `VM` (`LinkRequest.ID` is the VMID, `Name` the VM name), `HostShell` and `ProxmoxUI`; `internal/links` is deleted. **A console or shell link is omitted while the node is unknown** (the old builder emitted an empty `node=`). A VM that is a Kubernetes node also gets the Headlamp node link through `Host.Links()`.
  - **Series.** The expressions close over the running instance's settings and return `queries.ErrBadSeries` before `Start`; with the plugin not running, `vm.cpu` is an unknown series, **400** `invalid_input` (the unknown-name path), not the 404 that prometheus not running gives.
  - **SPA.** `modules/proxmox` declares the three topics and imports `NodeData` and `PVEStorageData` from its `model.gen.ts` (A.9); the VMs page and sheet moved there and fetch the new route; the VM tile reads the `vms.running` and `vms.total` KPIs; the Hardware page's System facts are a `hardware.facts` slot contribution reading `proxmox.node`, hidden when proxmox is hidden (A.9).

### F.7 `hardware` (host metrics; depends on `prometheus`)

- **From:** `topics/host.go:15-196`; `queries.go:57-118`; `vms.go:42-47,96-103` (machines); `attention.go:235-259`; `web/src/pages/hardware.tsx:46-48,148-149`.
- **Config:**
  - `hosts[] {name, nodename, model, bays, fanMaxRPM, sensorAliases {inlet, exhaust}}`
  - `zfsScrubMetricPrefix` (validated like `config.go:153-155`)
  - `selectors {nodeExporterJob: node-exporter, hwmonChip: "platform_coretemp_.*"}`
  - `machines[] {name, nodeExporterNodename? | gatusEndpoint?}`, which replaces `mac-mini` and `rpi-control`
  - `tempThresholds` (today `TempHealth`)
- **Topics:** `hardware.hosts`, `hardware.machines`. **Series:** `host.*`. **Attention:** ZFS, SMART, CPU temperature. **KPIs:** power, CPU temperature. **Requires:** prometheus. **Uses:** proxmox (facts), gatus (machines).
- **k8s:** no.
- This is a separate plugin rather than a query pack inside `prometheus`, so it can be enabled on its own, has its own page, status and config, and keeps `prometheus` generic (decided, J.10).
- **As shipped (G3 PR-2, v0.8.0, P3.7):** `internal/plugins/hardware`; topics `hardware.hosts` (`model.HostData`, polled every 15 s, 25 s per poll) and `hardware.machines` (`hwmodel.MachinesData`, from `internal/plugins/hardware/model`; polled every 15 s, a Gatus change reaches it within a second); series `host.{power,cpuTemp,cpu,mem,temps,fans}`; KPIs `power` (W) and `cpuTemp` (°C); nav `/hardware`.
  - **Activation.** `Configured` is `hosts` or `machines` non-empty, so a deployment that relied on v0.7.0's implicit host sets `hosts: [{}]`; `ConfigHint` is `plugins.hardware.hosts or machines`. The manifest **`Uses` prometheus, proxmox and gatus and has no `Requires`** (the draft's "Requires: prometheus" is dropped): a Gatus-only list of machines must not be `blocked` for want of Prometheus, so whichever half cannot run says so in its `sources` and the rest goes on. ADR 0001 decision 10 carries the same correction.
  - **Validation is strict and the defaults are required.** `pluginconf.Hardware.Validate` rejects an empty `selectors.nodeExporterJob` or `hwmonChip`, a `hwmonChip` that is not RE2, `tempThresholds` that break `0 < warn < crit`, more than one host, `bays` outside 0 to 128, `fanMaxRPM` outside 0 to 100000, a machine name that is empty, over 64 bytes or repeats an earlier one ignoring case, and a machine that sets both or neither of `nodeExporterNodename` and `gatusEndpoint`. A zero `Hardware{}` therefore fails, so `config.defaults()` carries `pluginconf.DefaultHardware()` (`node-exporter`, `platform_coretemp_.*`, 85, 90, `inlet`, `exhaust`). `Hardware.Normalized()` (trimmed names, default sensor aliases) is the one form the plugin reads. Thresholds are integers because the env table has no float column. Every selector value goes through the existing PromQL string quoting, so a quote in a value cannot break a query.
  - **Names on the wire.** The config key is `fanMaxRPM`; the wire key of `HostData` is `fanMaxRpm`, because the OpenAPI linter's camel-case rule rejects `RPM`. New `HostData` keys: `ref` (`hardware:Host/<name>`), `bays`, `fanMaxRpm` (0 means "the page derives it"), `sensorAliases`, `tempWarnC`, `tempCritC`, `cpuTempHealth`; `Pool.ref` and `Disk.ref`; `TopologyHost.cpuTempHealth`. The four Proxmox facts left `HostData` in P3.6. `Kpis.powerWatts` and `cpuTempMaxC` are gone (the `Kpis` struct stays until P3.13).
  - **The host's node and name.** `hosts[0].nodename`, else `svc.NodeName` of the proxmox plugin, re-read on every poll and series call. The proxmox plugin provides it only while it runs (URL, token ID and token secret all set), so a deployment that set `plugins.proxmox.node` without a token, which v0.7.0 accepted, needs the token or `hosts[0].nodename`: until the node is known the row is `unavailable` ("waiting for the Proxmox node name"). Until it is known the host half runs no query and its source reads `not_configured`. `HostData.Name` and `TopologyHost.Name` are `hosts[0].name`, else the Proxmox node, else the nodename. Flat `HostNodename`, `HostModel` and `Conventions.ZFSScrubMetricPrefix` are deleted; `plugins.hardware.zfsScrubMetricPrefix` keeps its name and env name, and an empty prefix runs no scrub query (`scrubTracked: false`).
  - **Health is computed on the server.** `cpuTempHealth` comes from `tempThresholds` once, and the Hardware page, the topology card and the `cpuTemp` KPI's own `health` all show that value (the web's `cpuHealth` and `tempHealth` are deleted). The attention items keep the rules and ids of v0.7.0 (`zfs:pool:<name>`, `zfs:scrub:<name>`, `smart:<bay>`, `host:cpu-temp`; sources `zfs`, `smart`, `hardware`; route `/hardware`) and gain `ref`; `Since` comes from `plugin.FirstSeen`, so it is stable across calls.
  - **Machines.** `hardware.machines` is `[]` (never null) when none are configured, and it is published whenever the row is visible, because an unpublished topic would leave `useTopic` pending. It waits for its first node-exporter poll, so a machine does not flash as down, and the row is `starting` ("waiting for the first poll") until the gatus service has polled once (`EndpointHealth.Revision() > 0`, whatever the outcome) when Gatus machines are listed. A `nodeExporterNodename` machine is up when `up{job=<selectors.nodeExporterJob>,nodename=<name>}` has a sample of 1 or more (source `prometheus`); a `gatusEndpoint` machine is `ByName`'s `up`, unknown counting as down (source `gatus`), compared ignoring case. A watcher on `Host.Go` (at most one second) compares `EndpointHealth.Revision()` and the service identity and republishes on any difference, so a reload of gatus cannot hide a change; the envelope appends `SourceStatuses()`, and with no gatus plugin the machine is down and the chip reads `not_configured`. The machine's source label is `prometheus` or `gatus` (it was `victoriametrics` or `gatus`). **`hardware.hosts` is not published for a machines-only configuration**; the page then shows the machines panel alone, titled "Machines".
  - **The row.** It is `unavailable` only while no half can run and nothing has been published (the host half waits for its node or for prometheus, the machines for prometheus or gatus). After the first publish it stays visible and a missing dependency shows as a `not_configured` source: pinned by `TestHardwareUnavailableUntilTheNodeIsKnown`, `TestHardwarePublishesAroundTheFlip` (the publish may come before or after the row turns visible) and `TestHardwareNeverHoldsDataWhileUnavailable`. Visibility is rule (a), tested with `AssertTopicsReachOpenStream` for both topics.
  - **Series.** The `host.*` names are unchanged and are answered from the running plugin's own settings (the closure reads them through an `atomic.Pointer` set in `Start` and returns `queries.ErrBadSeries` until the node is known). With the plugin not running they are unknown series: 400 `invalid_input`. `Args.HostNodename` and its line in `api/read.go` are deleted. The envelope sources of both topics name `prometheus`, and the Overview shows a `prometheus` chip next to the legacy `victoriametrics` one until the legacy key is renamed.
  - **SPA.** `modules/hardware/hardware-page.tsx` (moved from `pages/hardware.tsx`) reads `bays` (the grid is `bays` slots; when 0 it numbers its slots from 1, or from 0 when a disk sits in slot 0, up to the highest populated slot, at most `MAX_BAYS` = 128 slots, and above that, or with labels that are not distinct numbers, it lists the disks sorted; with no disks and no bays it says "No disks reported."), `fanMaxRpm` (the gauge scale is `max(fanMaxRpm, highest observed rpm)`, or the highest observed when 0), `sensorAliases` for the Inlet and Exhaust tiles (an empty alias matches no sensor), `cpuTempHealth` with the "warn X · crit Y" line from `tempWarnC` and `tempCritC`, and `model` for the title; `BAY_COUNT` and `FAN_SCALE_MIN` are deleted and the "iDRAC log" panel is "System Event Log". `machines-panel.tsx` draws nothing for an empty list. `SCRUB_STALE_DAYS = 35` stays a named constant and is not a setting.

### F.8 `kubernetes`

- **From:** `internal/kube/kube.go`; `topics/cluster.go`; `feeds.go:75-150` (apps); `storage.go` PVC/etcd/NAS parts; `api/actions.go:58-107`; `upstream/transport.go`; `links.Headlamp` consumers; `attention.go:147-178`.
- **Config:**
  - `mode: auto|inCluster|kubeconfig`, `kubeconfig`, `context`, `qps/burst` (20/40, `kube.go:72`)
  - `conventions {protectedLabel, tenantLabel, appsFluxPrefix}` (all default empty)
  - `apps {gateways: [{namespace, name}], excludeNamespaces: [], annotationPrefix: gethomepage.dev, ingress: false}`. This replaces `gateway/home` and `"dash"`; the default with no `gateways` is to accept all HTTPRoutes.
  - `backupJobs[] {namespace, cronjob, label}`, `etcd {job: etcd}`
  - `optional {certificates: auto, externalSecrets: auto, httpRoutes: auto}`
  - `upstreamViaAPIServer: false`
  - `actions.enabled`, `actions.denyNamespaces` (default `[]`; a restart that targets a listed namespace returns 403 and is audited)
- **Topics:** `kubernetes.cluster`, `kubernetes.apps`, `kubernetes.storage`. **Routes:** `GET pods/{ns}/{name}`. **Series:** `node.*`, `pod.*` (with prometheus). **Actions:** `restart`. **Services:** `Cluster`, `NodeIndex`, `SecretResolver`. **Attention:** nodes, pods, workloads, certificates, PVCs.
- **Discovery gating:** at Start, `ServerGroupsAndResources` decides which dynamic informers to create. Missing optional CRDs become a `degraded` source note, never a blocked sync. A periodic re-discovery (5 min) picks up CRDs installed later.
- **No cluster:** in `auto` mode, if there is no in-cluster env and no kubeconfig, the state is `unavailable` with the reason. No crash, and `/readyz` is unaffected.
- **As shipped (G3 PR-3a, v0.9.0, P3.8 to P3.10):** `internal/plugins/kubernetes` (the informer library `internal/kube` no longer imports `internal/config`); topics `kubernetes.cluster` (`model.ClusterData`), `kubernetes.storage` (`model.StorageData`) and `kubernetes.apps` (`model.AppsData`); route `GET /api/v1/plugins/kubernetes/pods/{ns}/{name}`; series `node.{cpu,mem}` and `pod.{cpu,mem}`; the action `restart`; the KPIs `nodes.ready`, `nodes.total` and `pvc.worst`; attention items, topology and the Headlamp links; nav, version and description are the shim's. `Configured` is always true (`ConfigHint` is `plugins.kubernetes.kubeconfig or run in a cluster`) and the manifest `Uses` prometheus and gatus with no `Requires`. The plugin exports **one service object that is both `svc.NodeIndex` and `svc.Cluster`** (A.3 and A.5 have the details); the `SecretResolver` service of the Body above is **not built**: `k8s:` references go through `secret.K8sRefs` (C.3). Points that differ from the Body above:
  - **Modes and connection.** `mode` defaults to `auto`: the kubeconfig when `plugins.kubernetes.kubeconfig` or the bare `KUBECONFIG` is set, else the in-cluster configuration, else no cluster. `inCluster` and `kubeconfig` insist on their source; one that cannot work, or a `context` that does not exist, is an `error` row whose message names the setting and never a secret. `qps` and `burst` default to 20 and 40 (an unset value is the default; `burst` below `qps` is a validation error).
  - **`optional.*` are booleans, not `auto`.** `optional.{certificates,externalSecrets,httpRoutes}` default to `false`: a CRD of that group that is not served is a `not_configured` source note and makes the row `degraded`; `true` says the cluster may lack it and silences the note. Only the groups the plugin reads count: an aggregated API that fails discovery and is not one of them (a metrics-server `APIService`, say) is logged at INFO and never degrades the row.
  - **`etcd.job`** (default `etcd`) is the `job` label of the etcd queries and is validated, because it goes into PromQL. **`backupJobs`** takes up to eight entries `{namespace, cronjob, label}`: `label` names the row (empty keeps the metric's `cronjob` label), a repeated `namespace/label` is a validation error, and a `cronjob` also matches `cronjob-<suffix>`. Each entry is one row with the ref `kubernetes:BackupJob/<ns>/<name>`; the Storage page calls the panel **Backup jobs** (the draft's "NAS backups" is gone, with the type `NASBackup` and the field `nasBackups`, now `backupJobs`). The `conventions` keys are unchanged and still empty by default.
  - **Restart** is a framework action (A.8). The route `GET pods/{ns}/{name}` has no `RouteSpec.Namespace` and no read deny list: `actions.denyNamespaces` governs Restart only and never refuses a viewer's pod sheet (the legacy route had no read deny either).
  - **`upstreamViaAPIServer`** replaces `DASH_DEV` as the switch for the API-server service proxy. The plugin exports `APIServerTransport()` only while it runs with the flag on, and `cmd/dash` resolves it **per request** (the other plugins build their clients before this one starts), so a plugin that started earlier is wrapped at its next request. `DASH_DEV` no longer builds it. Changing the flag restarts the plugin like any other key.
  - **Discovery gating and the readiness gate (A.3 has the recipe).** Only the 11 typed informers (nodes, namespaces, pods, PVCs, Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs, CronJobs and the warning events) gate readiness; the dynamic ones (cert-manager, external-secrets, HTTPRoutes, Ingresses when `apps.ingress` and, for this release only, the CNPG clusters of the storage extras) are added after discovery, and a CRD installed later is found within five minutes or at once when a consumer asks for it through `svc.Cluster.Watch`. A CRD that disappears keeps failing in its informer's reflector (a shared factory cannot stop one informer) until the plugin restarts: `Has` turns false at the next discovery and `List` answers nil.
  - **States.** `unavailable` (hidden, no service): `mode: auto` with no cluster configuration, or the API-server probe (`/version`, every 5 s, 3 s timeout) failing for 10 s; recovery is immediate. `error`: an explicit mode that cannot work, or the typed informers not synced 60 s after `Start`, with the category of their last watch error. `starting` until the typed informers have synced, the first discovery has ended and the first usage and storage polls have ended, successfully or not. `degraded` for a wanted group that is missing or failed, and for an Ingress that is served but refused (RBAC; see the `WatchError` deviation in the epic's PR-3a table). `healthy` otherwise. A Prometheus failure is a source note named `prometheus`, not a degraded row; the legacy source name `victoriametrics` is gone from the overview.
  - **Apps and icons (P3.9).** `apps.gateways` (`[{namespace, name}]`, empty accepts every HTTPRoute), `apps.excludeNamespaces` (empty skips nothing), `apps.annotationPrefix` (default `gethomepage.dev`, a trailing `/` ignored) and `apps.ingress` (default `false`; `true` also reads `networking.k8s.io` Ingresses, one app per rule host, through an informer that exists only then and needs `ingresses` `get`, `list` and `watch`) replace the built-in gateway, the `dash` namespace and the annotation literals. **The defaults are generic**, so a deployment that relied on the built-in behaviour sets the first two (the release notes in the epic say how). A `parentRef` without a namespace means the route's own namespace (the Gateway API default; the old code required an explicit one). `App.icon` is the annotation's value as written (trimmed, at most 256 bytes); a homepage-style token (`mdi-`, `si-`, `sh-`) is empty. The server never builds an icon URL: the SPA resolves the token (A.9). An app's ref is `kubernetes:App/<ns>/<source>/<resource>/<host>`.
  - **Series.** The `node` argument resolves through the plugin's node cache to `<ip>:9100`; a malformed node name is 400 `invalid_input` before any lookup, and an **unknown node is 404 `not_found` "unknown node"** as before (A.3 has the plugin hook that makes it so). `pod.*` arguments are checked with `ValidDNS`. As with every plugin's series (A.3), `node.*` and `pod.*` are answered only while the plugin runs; otherwise the name is unknown (400 `invalid_input`).
  - **Reload.** The plugin reloads live: a change to `plugins.kubernetes.*` restarts it (informers re-sync: the row is `starting` for a few seconds and its topics disappear and reappear) and restarts proxmox, which `Uses` it. Only `flux` still needs a restart of the process.
  - **No cluster.** As the Body says, with `TestStartsWithoutACluster` running the real `run()` with no kubeconfig and no in-cluster environment: `/readyz` is 200, kubernetes and `flux` are `unavailable`, proxmox is healthy and publishes its VMs without `k8sNode`, and `/api/v1/me` answers. dash no longer exits at startup for want of a cluster.

### F.9 `flux` (requires `kubernetes`)

- **From:** `topics/flux.go`; GVRs at `kube.go:32-36`; `actions.go:22-28,109-134`; `attention.go:126-145`; `overview.go:44-48`; `web/src/pages/gitops.tsx:144-184,315`; `command-palette.tsx:81-88`.
- **Config:** `verify {requireWhenSpecVerify: true, sources: [{kind, namespace, name}]}`, `suspendReasonAnnotation`, `actions.enabled`.
  - Any `GitRepository`/`OCIRepository` that has `spec.verify` set but no `SourceVerified=True` is critical. This needs no names and replaces the `flux-system/flux-system` literal.
  - `sources` additionally pins named sources that must be verified, e.g. a source that must never lose its `spec.verify`.
  - The GitOps header panel and the palette's "Reconcile ..." entries iterate over these sources instead of the literal.
- **Topics:** `flux.objects`. **Actions:** `reconcile`. **KPI:** GitOps ready/total, verified.

### F.10 Storage packs: `cnpg`, `rustfs`

- **cnpg** (requires kubernetes; uses prometheus): from `kube.go:37`, `storage.go:139-158` and `queries.go:44`. Config: `backupMetric` (default the barman metric). Topic: `cnpg.clusters`. Panel slot: `storage`.
- **rustfs** (requires prometheus): from `queries.go:35-38` and `storage.go:49-52,114-128`. Config: metric names, defaulting to today's. Topic: `rustfs.usage`. Panel slot: `storage`. A generic "S3 bucket usage" plugin can follow if another backend (MinIO, Garage) is requested.
- `cnpg` and `rustfs` are separate plugins, not packs inside `prometheus` (decided, J.10).
- `model.StorageData` (`model.go:521-529`) is removed. The Storage page renders the `storage` panel slots of enabled plugins.

---

## G. Open-source blockers checklist

### G.1 Homelab-specific values

- [ ] Remove the `.svc` defaults (`config.go:100-103`) and the homelab convention defaults (`config.go:114-120`) (F, B.5).
- [ ] Replace the literals: `flux-system` (5 places, audit), `mac-mini`/`rpi-control` (`vms.go`), Talos VMIDs (`health.go:75-76`), the `infrastructure` group (`health.go:105`), `gateway/home` and `dash` (`feeds.go:92,117`), 16 bays (`hardware.tsx:47`), Intel coretemp chip (`queries.go:77`).
- [ ] Copy: "Homelab mission control" (`topbar.tsx:125`) stays (J.14, decided). Check that the package comments (`cmd/dash/main.go:1`, `internal/demo/demo.go:2`) name no private lab details.

### G.2 Test fixtures with real identifiers

- [ ] Rewrite the fixtures listed in 1.1: `clients_test.go`, `transport_test.go`, `queries_test.go`, `api_test.go`, `silences_test.go`, `attention_test.go`, `links_test.go`, `actions_test.go` (`homelab guardrail:` → `admission denied:`).
- [ ] Use RFC 5737 IPs (`192.0.2.0/24`) and RFC 2606 domains (`example.com`).
- [ ] Use a token fixture that is not shaped like a real ID (`dash@pve!test=...` with zeros).
- [ ] Add a repo-wide denylist test (generalising `internal/demo/demo_test.go:44`) that fails CI on `techdufus` (outside the module path, image name, LICENSE and the Android application ID `io.github.techdufus.dash`, clients-and-ai.md M.6 and P.4), `home.techdufus`, `10.0.20.`, `pve-730`, `talos-cp`, `yahwai`, `registry.home`, `omp-cloud`, `mac-mini`, `rpi-control` and similar lab identifiers.

### G.3 Dev tooling

- [ ] `Makefile:1-2,16-18`: make `HL_OP` optional. Without it, `dev-api` runs `go run` with `DASH_CONFIG=dev/config.yaml` and whatever `KUBECONFIG` is set. Keep an opt-in `make dev-api-op` for the homelab's 1Password broker, or move that wrapper into the homelab repo.
- [ ] Delete `dev/secrets.env` (a reference only, but it names the private repo layout), or rename it to `dev/secrets.env.example` with a placeholder.
- [ ] Optional: a `kind`/`k3d` dev cluster recipe and `DASH_DEMO=true` as the default contributor path.

### G.4 README and docs

- [ ] Rewrite the README: product intro without the homelab-specific URL, Talos or R730; quick start (docker run, compose, Helm); link to docs; screenshots from demo mode.
- [ ] Remove `README.md:23-28,51-53,150-151,166-174`, or move it to a "Running dash in TechDufus's homelab" note in the homelab repo.
- [ ] `docs/`: `configuration.md` (generated), `plugins/<id>.md` (generated from schema plus a hand-written intro), `auth.md`, `deploy/{docker,compose,kubernetes,paas}.md`, `security.md` (threat model: actions, admission guardrail, secrets).

### G.5 Third-party assets and licences

Licences below are expected values; P6.5 confirms them with tools.

| Asset | Where | Licence | Action |
|---|---|---|---|
| dashboard-icons (homarr-labs) | runtime CDN `feeds.go:77`, `config/links.ts:4` | Apache-2.0 (repo `LICENSE`); the logos are trademarks of their owners | Attribution in NOTICE. Decided (J.17): icons are bundled or self-hosted by default, so the browser makes no third-party request. `ui.iconBaseURL` opts into the jsDelivr CDN (the browser then fetches icons from jsDelivr, a privacy cost) or another mirror |
| shadcn/ui components | `web/src/components/ui/*` (21 files, `web/components.json`) | MIT | NOTICE |
| Magic UI components | `web/src/components/magicui/*` (4 files) | MIT [verify at source] | NOTICE |
| lucide-react | npm | ISC | generated report |
| Inter, JetBrains Mono (`@fontsource-variable/*`) | bundled, `globals.css:4-5` | SIL OFL 1.1 | generated report |
| Go modules (client-go etc.) | `go.mod` | Apache-2.0 / BSD / MIT | `go-licenses report` in CI; THIRD_PARTY_NOTICES |

### G.6 Licence (decided: MIT, J.1)

| Licence | Pros | Cons |
|---|---|---|
| MIT (**decided**) | Simplest; maximum adoption | No patent grant; no copyleft |
| Apache-2.0 | Patent grant; NOTICE mechanism; matches client-go and the Kubernetes ecosystem | Slightly heavier (NOTICE, change statements) |
| MPL-2.0 | File-level copyleft: changes to dash files must be shared, but it can be combined freely | Less familiar; weaker than AGPL against hosted forks |
| AGPL-3.0 | Hosted modifications must be published; protects against closed SaaS forks | Many companies ban AGPL, so fewer contributors and users; overkill for a self-hosted dashboard |
| BSL 1.1 / FSL | Commercial protection with a delayed open licence | **Not OSI open source**, which contradicts "open-sourcing"; community friction |

Decided (J.1): MIT, with the copyright line "Matthew DeGarmo (TechDufus)" in `LICENSE`. The table is kept as the comparison behind the choice; Apache-2.0 was the earlier lean.

### G.7 Community files

- [ ] `LICENSE` (MIT, copyright "Matthew DeGarmo (TechDufus)"), `NOTICE` / `THIRD_PARTY_NOTICES`.
- [ ] `SECURITY.md`: GitHub private vulnerability reporting enabled; supported versions; scope (actions, auth bypass, secret leakage, tokens, OAuth, MCP, approvals and push).
- [ ] `CONTRIBUTING.md`: `mise install`, `make check`, `make generate`, demo mode, conventional commits, how to add a plugin.
- [ ] `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1).
- [ ] Issue and PR templates; `CODEOWNERS`.
- [ ] Examples: `examples/compose`, `examples/kubernetes`, `examples/config.yaml`.
- [ ] Demo mode as the screenshot source: keep `DASH_DEMO=true`, add a `make screenshots` (Playwright against demo) leaf so docs images stay current after the plugin UI changes.

### G.8 Self-hosted runner (blocker)

- [ ] `ci.yml:13,22` run on `[self-hosted, linux, x64, homelab, dash]`. **Today the workflow has no `pull_request` trigger**: only `push` to `main` and `v*` tags (`ci.yml:2-5`). That alone does not make a public repo safe. For `pull_request` events, GitHub runs the workflow files from the PR's merge commit, so a fork PR could add its own workflow targeting those labels.
- [ ] Remove the self-hosted labels entirely and move to GitHub-hosted runners (H) **before** the visibility flip.
- [ ] Deregister the runner from this repository. User-owned repos can only have repo-level runners. If it lives in an org runner group, untick "Allow public repositories" and remove this repo from the group.
- [ ] Repo settings: Actions → "Require approval for all external contributors" (fork PR workflows); default `GITHUB_TOKEN` permissions read-only; disallow Actions from creating or approving PRs.
- [ ] Audit every workflow for `pull_request_target`, `workflow_run` and `issue_comment` triggers (none today, since `ci.yml` is the only workflow). Add a CI lint (actionlint/zizmor) that fails on unsafe triggers and on unpinned actions.

### G.9 Git history (decided, J.8)

Section 1.3 lists the identifiers in history; the secret scan is clean (1.4).

| Option | Pros | Cons |
|---|---|---|
| Keep history | Honest provenance; signed commits preserved; zero work | Internal hostnames, a lab IP, node names, a tenant name and the private repo layout remain readable in 7 commits |
| Fresh history (new repo or orphan squash) | Clean slate; trivial with 7 commits | Loses history and signatures; existing clones and refs are orphaned |
| `git filter-repo --replace-text` (**decided**, as a late step) | Keeps the commit structure and the history, and removes the identifiers | Rewrites every SHA, so signatures are lost, existing clones and refs are orphaned, and it needs a force-push. Verify with the denylist test and a re-run of gitleaks and trufflehog |

Decided (J.8): keep the history, no squash. Right before the repository goes public, scrub the homelab identifiers listed in 1.3 with `git filter-repo --replace-text`. That is a separate, later step that needs TechDufus's explicit go-ahead at that time, because it is a history rewrite plus a force-push. A fresh squash or new repo was rejected.

### G.10 Final gate before the flip

- [ ] History scrub (G.9) done, with TechDufus's explicit go-ahead given at that time.
- [ ] gitleaks and trufflehog over all refs, both clean.
- [ ] Denylist test green.
- [ ] Settings → Code security: secret scanning and push protection on; Dependabot alerts on.
- [ ] Branch protection or rulesets on `main`: required checks, signed commits if desired, no force-push. Enable it after the G.9 force-push.
- [ ] GHCR package visibility set to public, linked to the repo.

---

## H. CI and release after going public

### H.1 Runners

- `check`, `scan` and `release` run on `ubuntu-24.04` (GitHub-hosted, free for public repos).
- Multi-arch needs neither QEMU nor an arm runner. The Dockerfile cross-compiles from `$BUILDPLATFORM`, and the final stage has no `RUN` (D.5).
- `ubuntu-24.04-arm` stays an option for running `go test` natively on arm64 later.

### H.2 Workflow sketch

```yaml
# .github/workflows/ci.yml (sketch; pin every action to a full commit SHA, as ci.yml does today)
name: ci
on:
  pull_request:
  push:
    branches: [main]
    tags: ["v*"]
permissions:
  contents: read
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}
jobs:
  check:
    runs-on: ubuntu-24.04
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@<sha> # v7.0.1
        with: { persist-credentials: false }
      - uses: jdx/mise-action@<sha> # v5.2.4
      - run: make check

  scan:
    runs-on: ubuntu-24.04
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@<sha>
        with: { fetch-depth: 0, persist-credentials: false }
      - run: make scan   # pinned gitleaks + trufflehog binaries verified by checksum; govulncheck; actionlint/zizmor

  release:
    if: startsWith(github.ref, 'refs/tags/v')
    needs: [check, scan]
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    environment: release            # required reviewer (repository setting) before publishing
    permissions:
      contents: read
      packages: write               # push to ghcr.io with GITHUB_TOKEN
      id-token: write               # OIDC for attestations (and keyless cosign)
      attestations: write
    steps:
      - uses: actions/checkout@<sha>
        with: { persist-credentials: false }
      - name: Validate tag and refuse to overwrite a version (create-only, as today)
        run: |
          set -euo pipefail
          version=${GITHUB_REF_NAME#v}
          [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "::error::tag must be vX.Y.Z"; exit 1; }
          if docker buildx imagetools inspect "ghcr.io/techdufus/dash:$version" >/dev/null 2>&1; then
            echo "::error::ghcr.io/techdufus/dash:$version already exists; tag a new version"; exit 1
          fi
          echo "VERSION=$version" >> "$GITHUB_ENV"
      - uses: docker/setup-buildx-action@<sha>
      - uses: docker/login-action@<sha>
        with: { registry: ghcr.io, username: "${{ github.actor }}", password: "${{ secrets.GITHUB_TOKEN }}" }
      - id: meta
        uses: docker/metadata-action@<sha>
        with:
          images: ghcr.io/techdufus/dash
          tags: |
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=semver,pattern={{major}},enable=${{ !startsWith(github.ref, 'refs/tags/v0.') }}
          flavor: latest=auto
      - id: build
        uses: docker/build-push-action@<sha>
        with:
          platforms: linux/amd64,linux/arm64
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          build-args: VERSION=${{ env.VERSION }}
          provenance: mode=max
          sbom: true                  # BuildKit SPDX SBOM attached to the image index
      - uses: actions/attest-build-provenance@<sha>
        with:
          subject-name: ghcr.io/techdufus/dash
          subject-digest: ${{ steps.build.outputs.digest }}
          push-to-registry: true
      - uses: anchore/sbom-action@<sha>
        with:
          image: ghcr.io/techdufus/dash@${{ steps.build.outputs.digest }}
          format: spdx-json
          output-file: sbom.spdx.json
      - uses: actions/attest-sbom@<sha>
        with:
          subject-name: ghcr.io/techdufus/dash
          subject-digest: ${{ steps.build.outputs.digest }}
          sbom-path: sbom.spdx.json
          push-to-registry: true
      # Required: keyless signing (clients-and-ai.md K.25)
      - uses: sigstore/cosign-installer@<sha>
      - run: cosign sign --yes ghcr.io/techdufus/dash@${{ steps.build.outputs.digest }}
      - run: |
          echo "image: \`ghcr.io/techdufus/dash:${VERSION}@${{ steps.build.outputs.digest }}\`" >> "$GITHUB_STEP_SUMMARY"
      # Helm chart: helm package charts/dash --version "$VERSION" && helm push ... oci://ghcr.io/techdufus/charts
```

Notes:

- `X.Y.Z` stays create-only, enforced by the pre-push check. `X.Y`, `X` and `latest` move by design; the `X` tag is not published for `v0.*` (see the `enable` condition above). GHCR offers no native tag immutability as far as this plan knows [INFERENCE], so keep the check.
- Users verify with `gh attestation verify oci://ghcr.io/techdufus/dash:X.Y.Z --owner TechDufus`.
- Dependabot (or Renovate) for `github-actions`, `gomod`, `npm` and `docker` keeps the SHAs and digests current.
- Releases are cut from git tags `vX.Y.Z` (semver) and stay `0.x` until the plugin API is stable (decision J.19). The chart is pushed to `oci://ghcr.io/techdufus/charts` from the same job (J.7), at the same version.
- Demo mode ships in the normal image and is switched on by `DASH_DEMO` or a config flag. The workflow builds no `:demo` tag (decision J.15).
- Test matrix (decision J.18): the kubernetes, flux and cnpg plugin tests run against the last 3 Kubernetes minor versions, with current Flux v2 and Gateway API v1 CRDs; the proxmox plugin tests cover Proxmox VE 8 and 9 through recorded API fixtures.
- Cosign keyless signing is required, not optional. Users can verify with `cosign verify ghcr.io/techdufus/dash@<digest> --certificate-identity-regexp '^https://github.com/TechDufus/dash/' --certificate-oidc-issuer https://token.actions.githubusercontent.com`.
- The `release` environment has a required reviewer, and a tag ruleset restricts creating `v*` tags to the owner, so a compromised workflow or collaborator cannot publish a release alone.
- Every install of web dependencies uses `npm ci --ignore-scripts` (Makefile, Dockerfile and CI), so a dependency's install script never runs in the build. The `shadcn` CLI moves from runtime to dev dependencies (clients-and-ai.md K.13).

### H.3 The private Zot push (`registry.home.techdufus.com`, `ci.yml:31`)

| Option | How | Verdict |
|---|---|---|
| **(a) The homelab pulls from GHCR** | Bump the homelab manifest to `ghcr.io/techdufus/dash:X.Y.Z@sha256:...`. Optionally configure Zot's sync extension as an on-demand pull-through mirror of `ghcr.io`, so nodes pull through Zot and cache | **Decided (J.9)**: one artefact, publicly attested; no homelab secret in a public workflow |
| (b) Separate private mirror job | A workflow in the **private** homelab repo (on its own self-hosted runner) copies the attested digest from GHCR into Zot (`crane copy` / `skopeo copy`) after verifying the attestation | Rejected (J.9). If the cluster ever must not reach GHCR, it must never run in, or be triggered by, the public repo |
| (c) Keep pushing to Zot from this repo | Needs homelab credentials and a runner reachable from a public repo's workflow | Rejected |

README step 3 (bump the homelab manifest, `README.md:172-174`) remains in the homelab repo's docs, not in dash's.

---

## I. Phased plan

Each phase maps 1:1 to an epic group in [`epic-draft.md`](epic-draft.md). Leaves are one PR each.

| Phase | Epic group | Outcome | Depends on |
|---|---|---|---|
| 0 | Decisions and ADR | e2b7f93 merged; decisions in J (all 19 decided on 2026-10-31) recorded as `docs/adr/0001-plugins-config-auth.md` (P0.2) | this proposal |
| 1 | Config core and secrets | Typed config tree; file and env layers with provenance; legacy mapping; homelab defaults removed; `secret.Value`, refs, encryption, store, reload, audit log | 0 |
| 2 | Plugin framework and SPA registry | `internal/plugin`; schemas; `/api/plugins`; namespaced topics; core overview composition; decoupled readiness; SPA registry, slots, generic panels | 1 |
| 3 | Migrate integrations | 13 plugins in the order of F.0; Kubernetes optional; `internal/topics` legacy deleted; demo per plugin | 2 |
| 4 | Settings UI and auth | Auth modes (proxy with CIDRs, local, OIDC, none); roles; settings API; Settings pages; security headers | 1, 2 (UI forms need schemas); role enforcement on actions needs P2.3 and P3.3 |
| 5 | Packaging and deployment targets | Dockerfile updates; healthcheck command; docker/compose/PaaS docs; Helm chart (default `settings.ui: editable`); manifests; CI smoke tests | 3 (plugin config paths); P4.1 and P4.3 (in-app auth before the `0.0.0.0` default) |
| 6 | Open-source readiness and public CI | Fixtures scrubbed; denylist test; dev tooling; docs; MIT licence; community files; GitHub-hosted CI; GHCR release; repo settings; history scrub (needs the owner's explicit go-ahead); flip | Most leaves are independent. The CI move (P6.6) can start once Phase 0 and P6.1 (the denylist test) have landed. The flip (P6.10) depends on everything except phase 7 |
| 7 | Clients and AI | `/api/v1` clients: personal access tokens, built-in OAuth 2.1 server, MCP server, human approval for agent actions, PWA, push, native-client and agent guides, optional assistant plugin ([`clients-and-ai.md`](clients-and-ai.md) L–Q) | G2, G3, P1.5, P4.2–P4.4, P4.6, P4.7 and P5.4; independent of phase 6 and of the public flip (P6.10) |

Parallelism: P6.1, P6.2, P6.4, P6.5 and P6.6 do not depend on Phases 1-5 and can run alongside them. Auth (P4.1-P4.5) can start once Phase 1 lands. The leaf IDs (P<phase>.<n>) are the ones used in [`epic-draft.md`](epic-draft.md).

Phase 7 may start as soon as its dependencies land (including the Helm chart, P5.4, for the OAuth `routes.direct` value), in parallel with phase 6. It is not on the path to the public flip (P6.10).

---

## J. Decisions (2026-10-31)

TechDufus decided all 19 items on 2026-10-31. They are recorded as an ADR in `docs/adr/0001-plugins-config-auth.md` (P0.2).

1. **Licence (G.6):** **Decided:** MIT, with the copyright line "Matthew DeGarmo (TechDufus)" in `LICENSE`. Rejected: Apache-2.0 (the earlier lean), MPL-2.0, AGPL-3.0, BSL/FSL.
2. **Plugin boundary (A.1):** **Decided:** in-process, compile-time plugin list. `internal/plugin` stays internal until the API is stable at 1.0, then is promoted to `pkg/plugin`. Rejected for v1: out-of-process plugins (HTTP/gRPC sidecar, go-plugin, WASM); a remote adapter may come later behind the same interface.
3. **Settings store (B.7):** **Decided:** no database; one YAML store (`ui-settings.yaml`) with atomic writes. Rejected: SQLite (`modernc.org/sqlite`, separate `dash.db`), which would be considered only if multiple users with roles, server-side sessions or a searchable audit log are needed; bbolt.
4. **Precedence (B.2, D.4):** **Decided:** defaults < UI < file < env, with locked fields. Removing a stored value on a locked field stays allowed. The Helm chart defaults to editable UI settings (`settings.ui: editable`), with `readonly` as the option for GitOps; the homelab can opt into readonly. The strict replica guard stays (`replicaCount > 1` only with readonly settings and persistence off), so multi-replica requires opting into readonly. Rejected: UI-overrides-everything; a readonly chart default.
5. **Legacy `DASH_*` names (B.5):** **Decided:** a one-release alias layer that logs a deprecation warning per variable, then removal. Rejected: a direct cutover without the alias layer.
6. **Default auth mode (E.2, E.6):** **Decided:** `local` admin with a bootstrap token; `proxy` and `oidc` are options. The anonymous read-only viewer for wall displays (`auth.local.anonymousViewer`) is opt-in, off by default. Rejected: `proxy` or `oidc` as the default.
7. **Kubernetes packaging (D.4):** **Decided:** Helm chart in this repo, published as an OCI chart to GHCR. Rejected: a separate charts repo; manifests/kustomize only.
8. **Git history (G.9):** **Decided:** keep the history, no squash. Scrub the homelab identifiers with `git filter-repo` right before going public, as a separate later step that needs TechDufus's explicit go-ahead at that time (history rewrite and force-push). Rejected: fresh squash or new repo.
9. **Homelab image source (H.3):** **Decided:** the homelab pulls from GHCR directly, with an optional Zot pull-through cache. Rejected: a private mirror job in the homelab repo.
10. **Plugin naming (F.1, F.7, F.10):** **Decided:** `prometheus`, displayed as "Prometheus-compatible" (it covers VictoriaMetrics); `victorialogs` and `alertmanager` keep their names; `hardware`, `rustfs` and `cnpg` are separate plugins. Rejected: the name `victoriametrics`; packs inside `prometheus`.
11. **Actions default (A.8):** **Decided:** off per plugin (`actions.enabled: false`). Rejected: on by default.
12. **Roles (E.4):** **Decided:** viewer and admin only in v1. Rejected for v1: an `operator` role (actions without settings).
13. **Scope gaps (D.6, F.5):** **Decided:** a standalone logs page (VictoriaLogs without the kubernetes plugin) and backup freshness without cluster access are deferred to post-v1. Rejected: building either in v1.
14. **Branding:** **Decided:** keep the name `dash` and the tagline "Homelab mission control". The name collision with the Debian `dash` shell in search is accepted. Rejected: renaming.
15. **Demo mode (D.6, H.2):** **Decided:** stays opt-in through a config flag (`DASH_DEMO` or the config file); no separate `:demo` image tag. A public demo instance comes later, post-v1. Rejected: a `:demo` tag.
16. **Multiple instances per plugin (A.2):** **Decided:** one instance per plugin type in v1; the schema is ready for more (`plugins.<instance>.type`). Rejected: full multi-instance in v1.
17. **Icon source (G.5):** **Decided:** icons are bundled or self-hosted by default; the jsDelivr CDN is opt-in through `ui.iconBaseURL`. Rejected: the CDN as the default.
18. **Supported upstream versions to test against (H.2):** **Decided:** Proxmox VE 8 and 9, the last 3 Kubernetes minor versions, current Flux v2, and Gateway API v1. Rejected: testing only the latest upstream versions.
19. **Release cadence and versioning (H.2):** **Decided:** stay on 0.x until the plugin API is stable; semver releases are cut from git tags (`vX.Y.Z`). `pkg/plugin` does not exist before 1.0 (J.2), so it gets no compatibility promise until then. Rejected: a 1.0 release before the plugin API is stable.

Later decisions (API clients, agents, mobile and AI) are in [`clients-and-ai.md`](clients-and-ai.md) section S and are recorded in ADR 0002 (P0.3).
