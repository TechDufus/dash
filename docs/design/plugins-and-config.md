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

`internal/plugin.Supervisor` supervises each instance the way `topics.Manager` supervises pollers today (`manager.go:141-176`): a panic in `Start`, `Status`, `Act` or a `Host.Go` goroutine is recovered and the instance becomes `error` without taking the process down. It evaluates an instance in a fixed order: `enabled: false` → `disabled`; a section `type` that is not the instance, or a config of the wrong type → `error`; `Configured` false → `disabled` (auto) or `unconfigured` (explicitly enabled); `Validator` failure → `unconfigured`; a `Requires` instance that is unknown, not started or `unavailable` → `blocked`; `Start` failing → `error`; otherwise the plugin's own `Status()`. It starts instances in dependency order over `Requires` and `Uses` (ties by registration order) and stops them in reverse. On a config change (`config.Reloader` emits one `Change` per `plugins.<id>` section; `ReloadOptions.Live` keeps a live-applied change out of the "restart required" list) the changed instance and its dependents go through Stop (at most 10s) then Start. `RestartOnly` instances are never touched. Whenever any instance's state, message, topics, actions or nav changes, the supervisor publishes `core.plugins`, and when an instance stops it removes its topics from the hub.

`Host.HTTPClient` guards every dial, so a settings value cannot turn dash into a request proxy: the guard inspects the resolved address at dial time and always refuses link-local addresses (`169.254.0.0/16` and `fe80::/10`) and the cloud metadata address `fd00:ec2::254`, and it refuses loopback and the unspecified address when `HTTPConfig.FromUI` is set (clients-and-ai.md K.22). Redirects are re-checked and may not change host, and the client ignores `HTTP_PROXY` because a proxy would hide the real destination from the guard. `InsecureSkipVerify` logs a startup WARN; the "tls: verification disabled" source note comes with the first G3 plugin that builds a real client.

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

### A.5 Cross-plugin services

Some plugins provide services instead of, or in addition to, topics. Consumers declare `Requires` or `Uses` and look the service up through `Host.Service(id)`, which returns a narrow interface:

| Provider | Service | Consumers |
|---|---|---|
| `prometheus` | `Querier{Instant, Range}` (today `upstream.Prom`, `internal/upstream/prom.go:61-100`) | hardware, kubernetes (pod and node usage, PVC stats, etcd), proxmox (VM series), cnpg, rustfs |
| `kubernetes` | `Cluster` (listers from `kube.go:78-104`), `NodeIndex`, `SecretResolver` | flux, cnpg, proxmox (VM to node), apps discovery, `k8s:` secret refs |
| `gatus` | `EndpointHealth(host) (bool, bool)` | kubernetes apps (health dot), hardware machines |
| `victorialogs` | `Logs{Query, Tail}` (today `api.VLogsAPI`, `internal/api/api.go:33-37`) | kubernetes pod sheet |
| `headlamp`, `grafana`, `proxmox` | link providers (`links.Registry`) | every builder that attaches `[]model.Link` |

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
- When a plugin is disabled, the core removes its latest events from the hub and publishes `core.plugins`. The SPA drops the corresponding pages.
- Per-plugin API: `GET /api/v1/plugins` returns `[{id, name, version, state, message, topics, actions, nav, configurable}]`. Plugin GET routes live under `/api/v1/plugins/{id}/...`: today's `/api/vms/{vmid}` becomes `/api/v1/plugins/proxmox/vms/{vmid}`, `/api/pods/{ns}/{name}` becomes `/api/v1/plugins/kubernetes/pods/{ns}/{name}`, and `/api/logs` becomes `/api/v1/plugins/victorialogs/logs`. `/api/v1/series` stays a core endpoint that dispatches to the `SeriesSpec` registered under the series name, so the browser can still only ask for named, validated series. The whole public API moves under `/api/v1` (clients-and-ai.md L.1, P2.10).
- **Snapshot GET.** `GET /api/v1/topics/{topic}` returns the Envelope with an `ETag` (the hub digest); `If-None-Match` gets 304 (clients-and-ai.md L.5).
- **Topic filter and user-scoped topics.** `GET /api/v1/stream?topics=a,b` sends only the listed topics. There is no resumption: a reconnect replays current state. User-scoped topics such as `core.approvals` reach only their owner (clients-and-ai.md L.5).
- **Object refs.** Every object in a plugin payload carries `ref`, `<plugin>:<Kind>/<id>` (for example `kubernetes:Pod/media/app-0`). Refs are opaque to clients; the prefix routes to the owning plugin's `Objects` (clients-and-ai.md L.6).
- **Overview composition (P2.5).** `core.overview` no longer waits for the other topics: `topics.compose` merges the attention items of every running `AttentionSource` (each item carries `plugin`), `KPISource` outputs (`OverviewData.kpiList`, new) and `TopologySource` fragments, with the legacy per-source builders adapted as one contributor. The `Kpis` struct of today's payload stays for the SPA until each KPI moves into a `KPISource` in G3.
- **G2 routes.** Only the core routes are under `/api/v1` in G2 (`me`, `stream`, `series`, `plugins`, `topics/{topic}`, `plugins/{id}/…`). The integration routes that still run inside `topics.Manager` (`/api/config`, `/api/vms/{vmid}`, `/api/pods/{ns}/{name}`, `/api/logs`, `/api/logs/tail`, `/api/actions/*`, `/api/silences`) keep their unversioned paths until their plugin migrates and the P3.13 cleanup deletes them; they already use the L.2 error envelope, but they are not part of the OpenAPI document (`Route.Legacy` in `internal/api/routes.go`, with an allowlist test).

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

### A.9 How the SPA renders plugins

Design (J.2): **compiled-in React modules registered by plugin ID; the backend says which are on.**

```ts
// web/src/plugins/types.ts
export interface PluginModule {
  id: string;                                   // must equal the Go Manifest.ID
  nav?: NavItem[];                              // today's NAV entries move here (shell/nav.ts:21-31)
  routes?: { path: string; component: React.LazyExoticComponent<React.ComponentType> }[];
  panels?: Partial<Record<Slot, React.ComponentType>>; // "overview.kpis" | "overview.side" | "storage" | "apps.sources" | "hardware.facts"
  palette?: (ctx: PaletteCtx) => PaletteGroup[]; // replaces hard-coded groups (command-palette.tsx:80-178)
  settings?: React.ComponentType;               // optional custom form; otherwise schema-driven (E/C)
}

// web/src/plugins/registry.ts: explicit list, mirroring cmd/dash/plugins.go
export const MODULES: PluginModule[] = [proxmox, hardware, kubernetes, flux, alertmanager, gatus, cnpg, rustfs];
```

- `App.tsx:35-64` renders core routes (Overview, Apps, Storage, Settings) plus the routes of modules whose plugin is not `disabled`, `unavailable`, `unconfigured` or `blocked`, according to `GET /api/plugins` and the `core.plugins` topic. Nav, palette "Pages" and the rail badge follow the same list. A route for a disabled plugin shows a "plugin disabled" page with a link to settings, not a 404.
- `stream.ts:49-60` registers listeners for the topics listed by `/api/plugins` instead of the fixed `TOPICS` (`topics.ts:36-46`). `TopicData` stays a typed map, generated per plugin from the Go model packages; `tygo.yaml` gains one entry per plugin `model` package.
- **Composite pages** (Overview, Storage, Apps) render panel slots in registry order. Attention items come from `core.overview` and carry `plugin` and `route`. The route is shown only if that plugin's page is enabled.
- **Generic panels for simple plugins (optional, Phase 2).** A plugin without a React module may declare `UI.Generic: []GenericView{Topic, Kind: "table"|"stat"|"list", Columns}`. The SPA renders it with the existing `Panel` and table components, which is enough for link-only or simple status plugins.
- **No remote JavaScript.** JS fetched at runtime would run with the admin's session and could call settings or action endpoints, so it is equivalent to stored XSS. Add a CSP to SPA responses (`internal/api/static.go` sets no security headers today): `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'; form-action 'self'`, plus the `ui.iconBaseURL` origin in `img-src` when it is configured. `style-src 'self'` allows no inline `<style>`: the one that needed it, the chart colours in `web/src/components/ui/chart.tsx`, is fixed by the hardening PR (branch `fix/api-hardening`); a dependency that injects one at runtime must have its CSS imported statically (P4.6).
- **`safeHref()`.** Every `href` built from config or upstream data (launcher links, `web/src/config/links.ts`) goes through `safeHref()`, which allows `http:` and `https:` only; anything else renders as text (clients-and-ai.md K.12).
- **Launcher icons** may be a bundled name, a same-origin path, or a URL under `ui.iconBaseURL`. Any other URL is rejected.
- Closed unions widen: `LinkKind` becomes the provider plugin ID plus `app`/`external`, and `ICONS` (`links.tsx:6-12`) moves into modules. `Group` stays a core enum (Platform, Apps, Tenants, Other), because grouping is a core concept that the kubernetes plugin feeds.

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

### F.2 Link plugins: `grafana`, `headlamp`, `launcher`

- **From:** `internal/links/links.go:12-111`; `config.LoadLauncher` (`config.go:160-176`); `/api/config` (`api.go:155-161`).
- **grafana:** `url`, with links `alerts` and `home`.
- **headlamp:** `url`, `cluster` (default `main`), with object links for the kinds in `links.go:30-50`.
- **launcher:** `file` or inline `links[] {name, href, icon, description}` (the same validation as `config.go:170-174`). The links ship in the `/api/plugins` payload for the Apps and Overview launchers.
- **Topics/actions:** none. **k8s:** no.

### F.3 `alertmanager`

- **From:** `internal/upstream/alertmanager.go`; `topics/feeds.go:21-44`; `attention.go:111-124`; `api/silences.go`.
- **Config:** `url`, `auth`, `tls`, `ignoreAlerts` (default `[Watchdog, InfoInhibitor]`, `health.go:145`), `severityMap` (default `critical`→crit, `warning`→warn, `health.go:133-142`), `silenceDurations` (default `1h,4h,24h,7d`, `silences.go:18-23`), `actions.enabled`.
- **Topics:** `alertmanager.alerts`. **Actions:** `silence.create`, `silence.expire`. **Attention:** firing alerts. **KPI:** "Alerts firing".
- **Action rules:** `protectedAlerts` (default `[Watchdog]`) lists alerts that can never be silenced; a `silence.create` that matches one returns 403. `silence.expire` works only for silences whose `createdBy` starts with `dash:`, so dash never expires a silence it did not create (clients-and-ai.md K.6).
- **k8s:** no.

### F.4 `gatus`

- **From:** `internal/upstream/gatus.go`; `feeds.go:46-73`; `attention.go:216-225`; `health.go:100-109`.
- **Config:** `url`, `auth`, `tls`, `criticalGroups` (default `[]`; the homelab sets `[infrastructure]`), `pageSize`.
- **Topics:** `gatus.status`. **Service:** `EndpointHealth`. **Attention:** failing checks. **k8s:** no.

### F.5 `victorialogs`

- **From:** `internal/upstream/vlogs.go`; `api/read.go:93-117`; `api/sse.go:96-127`.
- **Config:** `url`, `auth`, `tls`, `fields {namespace: kubernetes.pod_namespace, pod: kubernetes.pod_name, container: kubernetes.container_name}` (today hard-coded at `vlogs.go:60-67`), `limit` (500).
- **Routes:** `GET /api/plugins/victorialogs/logs`, `/logs/tail`. **Service:** `Logs`. **Topics/actions:** none.
- **Namespace policy and audit:** `denyNamespaces` (default `[]`) lists namespaces whose logs are never served (403), for queries and tails alike. Every tail open is audited. Size, duration and concurrency limits on reads and tails are fixed by the hardening PR (branch `fix/api-hardening`) and become settings in P2.11 (clients-and-ai.md K.4).
- **k8s:** no API access. Its only UI entry point is the kubernetes pod sheet. A standalone logs page is deferred post-v1 (decision J.13).

### F.6 `proxmox`

- **From:** `internal/upstream/proxmox.go`; `topics/vms.go`; `host.go:22-30`; `links.VMConsole/HostShell/ProxmoxUI` (`links.go:84-101`); `health.go:75-87`; `attention.go:187-196`; `queries.go:40-42` and the `vm.*` series (`queries.go:167-185`).
- **Config:** `url`, `node`, `tokenID`, `tokenSecret*`, `tls {caFile, insecureSkipVerify}` (self-signed Proxmox is the common case), `criticalVMs {ids: [], tags: []}` (replaces `IsTalosVM`), `storage.source: api|prometheus` (default `api`: `/nodes/{node}/storage` removes the pve-exporter dependency for the storage panel).
- **TLS warning:** `tls.insecureSkipVerify: true` (here and in every plugin with a `tls` block) logs a startup WARN and adds a `tls: verification disabled` source note, visible in plugin status (clients-and-ai.md K.11).
- **Topics:** `proxmox.vms`, `proxmox.node`, `proxmox.storage`. **Routes:** `GET vms/{vmid}`. **Series:** `vm.*` when `prometheus` is enabled and pve-exporter is scraped (`series.vmSelector` default `id="qemu/{vmid}"`). **Links:** console, shell, UI. **Uses:** kubernetes (VM-to-node), prometheus.
- **Actions:** none today. VM start/stop is explicitly out of scope until requested.
- **k8s:** no. Multi-node clusters: `/cluster/resources` already lists every VM (`proxmox.go:141`), but node facts read a single `node`. `nodes: []` is future work.

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
