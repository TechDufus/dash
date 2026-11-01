<p align="center">
  <img src="docs/images/banner.png" alt="dash: Proxmox, Kubernetes, Flux, storage, alerts and service status for the homelab on one live screen">
</p>

<p align="center">
  <a href="#features">Features</a> •
  <a href="#screenshots">Screenshots</a> •
  <a href="#try-it-with-demo-data">Demo mode</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#develop">Develop</a> •
  <a href="#release">Release</a>
</p>

<p align="center">
  <img alt="Go 1.27" src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white">
  <img alt="React 19" src="https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black">
  <img alt="Tailwind CSS v4" src="https://img.shields.io/badge/Tailwind-v4-38BDF8?logo=tailwindcss&logoColor=white">
  <img alt="Kubernetes" src="https://img.shields.io/badge/Kubernetes-client--go-326CE5?logo=kubernetes&logoColor=white">
  <img alt="Flux" src="https://img.shields.io/badge/GitOps-Flux-5468FF?logo=flux&logoColor=white">
  <img alt="Proxmox VE" src="https://img.shields.io/badge/Proxmox-VE%209-E57000?logo=proxmox&logoColor=white">
</p>

**dash** is mission control for the homelab: one Go binary (stdlib `net/http`, client-go
informers) that serves an embedded React SPA and a live JSON/SSE API over Proxmox, Kubernetes,
Flux, the R730, storage, Alertmanager and Gatus. It runs in the Talos cluster as
`kubernetes/apps/dash` in [TechDufus/homelab](https://github.com/TechDufus/homelab), behind an
oauth2-proxy sidecar that admits only the GitHub user `TechDufus`, at
https://dash.home.techdufus.com.

![dash overview: KPIs, live topology from the Proxmox host through the VMs to the cluster's namespaces, and the attention list](docs/images/overview.png)

## Features

- **One screen for the whole lab.** The overview rolls alerts, GitOps, nodes, VMs, power and CPU
  temperature into a KPI row, draws the topology from the Proxmox host through its VMs to every
  namespace, and lists what needs attention, worst first.
- **Live, not polled from the browser.** Informers and upstream pollers rebuild the topics
  (`host`, `vms`, `cluster`, `flux`, `storage`, `alerts`, `status`, `apps`), the core composes
  `core.overview` from what the integrations contribute, and only what changed goes out over a
  single SSE stream. `core.plugins` says which integrations run and whether each is healthy.
- **Proxmox and the hardware.** VMs with their Kubernetes node, config, snapshots and charts;
  IPMI power, temperatures, fans and PSUs; ZFS pools and per-bay SMART health.
- **Kubernetes and GitOps.** Nodes, workloads, pods with logs and metrics, events, namespaces,
  certificates and External Secrets; every Flux source, Kustomization and HelmRelease with its
  revision and signature verification.
- **Storage and alerts.** PVC fill and growth with days-to-full, object storage buckets, Proxmox
  storage, CNPG clusters and their backups, NAS backups, etcd size; firing Alertmanager alerts and
  their silences.
- **Deep links everywhere.** Headlamp, Proxmox consoles and Grafana are one click from the object
  you are looking at, and a command palette (`Ctrl K`) jumps to any page, VM, node,
  workload or pod.
- **Safe by design.** Writes are limited to three operations: a workload rollout restart, a Flux
  reconcile request, and Alertmanager silences that expire. The homelab guardrail
  `kubernetes/platform/guardrails/policy-dash.yaml` fences the ServiceAccount to exactly those.

## Screenshots

All screenshots run on the built-in [demo data](#try-it-with-demo-data); every name and address
in them is made up.

| | |
|:---:|:---:|
| ![Virtual machines](docs/images/vms.png) **Virtual machines** | ![Hardware](docs/images/hardware.png) **Hardware** |
| ![Cluster pods](docs/images/pods.png) **Cluster** | ![Pod detail](docs/images/pod-sheet.png) **Pod detail** |
| ![GitOps](docs/images/gitops.png) **GitOps** | ![Storage](docs/images/storage.png) **Storage** |

<details>
<summary><b>More screenshots</b></summary>

| | |
|:---:|:---:|
| ![Alerts and silences](docs/images/alerts.png) **Alerts and silences** | ![Service status](docs/images/status.png) **Service status** |
| ![Apps](docs/images/apps.png) **Apps** | |

</details>

## Try it with demo data

`DASH_DEMO=true` runs dash without a cluster: `internal/demo` publishes a small, obviously fake
homelab on every topic (with values that drift so the UI looks live) and answers the REST
endpoints from the same fixtures. Nothing contacts Kubernetes, Proxmox or the monitoring
services, the restart and reconcile actions answer 202 without doing anything, and every link
points at `example.com`.

```sh
cd web && npm ci && npm run build && cd ..   # the SPA is embedded at build time
DASH_DEMO=true go run ./cmd/dash             # http://127.0.0.1:8080
```

For UI work against the demo data, run `DASH_DEMO=true go run ./cmd/dash` and `make dev-web`,
then open http://localhost:5173.

## Configuration

dash reads a YAML config file and `DASH_*` environment variables, and refuses to start when a
required setting is missing or invalid. Nothing about a particular lab is compiled in: an unset
Headlamp, Grafana or Proxmox URL simply drops those links.

The file is `DASH_CONFIG`, or `config.yaml` in the data directory (`DASH_DATA_DIR`, default
`/data`) when it exists. Unknown keys are an error that names the full path
(`plugins.proxmox.urll: unknown field`). The environment overrides the file, field by field.

```yaml
server:
  publicURL: https://dash.example.com
plugins:
  proxmox:
    url: https://pve.example.com:8006
    tokenID: dash@pve!dash
    tokenSecret: file:/run/secrets/pve
  hardware:
    hosts: [{nodename: pve, model: Dell R730}]
```

Every setting also has an environment variable: `DASH_` plus the path in UPPER_SNAKE case
(`plugins.proxmox.tokenSecret` is `DASH_PLUGINS_PROXMOX_TOKEN_SECRET`). Lists and objects are JSON
(`DASH_PLUGINS_HARDWARE_HOSTS='[{"nodename":"pve"}]'`). Append `_FILE` to read the value from a
file (`DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE=/run/secrets/pve`); setting both forms is an
error. A misspelled `DASH_PLUGINS_*` variable stops dash from starting, and any other unknown
`DASH_*` variable is logged. `dash config env` prints every path with its variable name;
[`docs/configuration.md`](docs/configuration.md) lists every setting with its default and
description (generated from the code).

`dash config schema` prints the JSON Schema of the file; save it and add
`# yaml-language-server: $schema=./config.schema.json` to the file for editor completion. `dash config validate [file]`
checks a file and the environment the way startup does, without starting dash or touching the
data directory, prints every problem with its path and layer, and exits 1 when there is one. It
does not read `env:` and `file:` secret references, which often point at files that exist only
where dash runs; `--resolve-secrets` reads them.

Secret fields (`plugins.proxmox.tokenSecret`) take a literal, `env:NAME` (the value of that
variable) or `file:/path` (the file's contents, trimmed); `..._FILE` is shorthand for a `file:`
reference. A literal in the config file logs a warning, because such files often live in git. A
reference dash cannot read stops it from starting and names the field, never the value. dash
scrubs every secret it has read, in its encoded forms too, and credential-shaped tokens
(`dash_pat_…`, `enc:v1:…`) from its log, from error messages and from the pod logs the UI shows.
A failing upstream is reported to viewers as one category only (`unreachable`, `timeout`, `tls`,
`auth`, `http_<code>`, `invalid_response`); the detail, with URLs, is in dash's log.

The data directory holds `ui-settings.yaml`, the layer reserved for values saved from the
settings UI. The settings editor arrives with P4.7: this release has no settings page or API, so
the layer only holds what was put there by hand. It is a sparse layer below the file and the
environment: whatever they set wins, and removing it from them shows the saved value again.
`server.*`, `settings.ui` and `dataDir` cannot be saved from the UI. dash
locks the directory, so a second instance on it exits with a message. When the directory is
missing or not writable, or `settings.ui` is `readonly`, the store is read-only: dash logs one
INFO line and runs on the file and the environment alone. Demo mode uses no data directory.

Every 10 seconds dash checks the config file and every file a secret points at (`file:` and
`..._FILE`, symlinks followed, so a Kubernetes ConfigMap or Secret update is seen) and reads the
layers again. A rotated secret file reaches the running credentials without a restart. An edit
that does not validate is logged once as an ERROR and changes nothing; dash keeps running on
the configuration it has. Any other changed setting is logged as a WARN that it applies after a
restart. Environment variables only change with a restart. Rotate a Proxmox secret without a
restart with `DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE`.

| Setting (file path) | Default | What |
|---|---|---|
| `server.publicURL` | required (dev and demo: `http://localhost:5173`) | the URL people open, with scheme and host (`https://dash.example.com`); the only `Origin` accepted on writes |
| `server.listen` | `127.0.0.1:8080` | API and SPA, reached through oauth2-proxy |
| `server.allowedHosts` | empty | extra Host names the API answers besides the `server.publicURL` host (a JSON list in the environment; `["*"]` accepts any) |
| `server.healthListen` | `0.0.0.0:8081` | `/healthz` and `/readyz` |
| `server.readyRequires` | empty | plugins `/readyz` waits for, e.g. `["kubernetes"]` for the informer sync (a JSON list in the environment); each must have left `starting` |
| `server.readyTimeout` | `60s` | how long `/readyz` waits for `server.readyRequires`; after that it reports ready anyway and logs one WARN naming the plugins still starting |
| `server.limits.streamsPerUser`, `.streams` | 16, 256 | open `/api/stream` connections per user and in all; more get 429 or 503 |
| `server.limits.tailsPerUser`, `.tails` | 4, 32 | the same for `/api/logs/tail` |
| `server.limits.tailMaxDuration` | `30m` | a log tail ends after this; the browser reconnects (a duration string, also in the environment) |
| `server.limits.sseWriteTimeout`, `.bodyReadTimeout` | `10s`, `10s` | a stalled stream write, or a request body that does not arrive, is cut off after this |
| `server.limits.maxHeaderBytes` | `65536` | request header size of the API listener |
| `settings.ui` | `editable` | `editable` or `readonly` (fully managed) |
| `dataDir` (`DASH_DATA_DIR`) | `/data` | where dash keeps its state and looks for `config.yaml` |
| `plugins.prometheus.url` | empty (source not configured) | VictoriaMetrics or Prometheus |
| `plugins.alertmanager.url` | empty (source not configured) | Alertmanager |
| `plugins.victorialogs.url` | empty (source not configured) | VictoriaLogs, for pod logs |
| `plugins.gatus.url` | empty (source not configured) | Gatus |
| `plugins.proxmox.tokenID`, `.tokenSecret` | empty (Proxmox off) | API token `<user>!<token>` and its secret; set together |
| `plugins.proxmox.url` | required with a token | Proxmox API |
| `plugins.proxmox.node` | discovered from the API when the cluster has one node | the node to read; set it for multi-node clusters or to skip discovery |
| `plugins.hardware.hosts` | empty | `[{nodename, model}]`, one entry for now: `nodename` is the label of the host's node-exporter and IPMI series (default: `plugins.proxmox.node`, or the discovered node), `model` is shown next to its name, e.g. `Dell R730` |
| `plugins.headlamp.url`, `.cluster` | empty, `main` | Headlamp deep links |
| `plugins.grafana.url` | empty | Grafana links |
| `plugins.launcher.file`, `.links` | empty | the launcher's links, from a YAML file or inline (not both) |
| `plugins.kubernetes.kubeconfig` | in-cluster | used outside the cluster; `KUBECONFIG` is honoured too, as it is, with no `_FILE` form |

`/healthz` answers `ok` while dash runs. `/readyz` answers `ok` once both listeners are bound and
every plugin's start has returned; a plugin in `error`, or an upstream that is down, never makes
dash unready. It no longer waits for the Kubernetes caches to fill: list `kubernetes` in
`server.readyRequires` to keep that, so a rollout holds the old pod until the new one has data.
`GET /readyz?verbose=1` adds one `<plugin> <state> [message]` line per plugin, but only for a
loopback peer (`kubectl port-forward`, a shell in the pod); any other peer gets the plain body.

The launcher is a list of `name`, `href`, `icon` and `description`; `icon` is a
[dashboard-icons](https://github.com/homarr-labs/dashboard-icons) name or an image URL. Mount a
file from a ConfigMap and point `plugins.launcher.file` at it, or list the links in
`plugins.launcher.links`. See [`docs/launcher.example.yaml`](docs/launcher.example.yaml).

dash also reads a few labels, annotations and metric names from the cluster. None has a default:
while one is empty, the feature that depends on it is off.

| Setting (file path) | Default | What |
|---|---|---|
| `plugins.kubernetes.conventions.protectedLabel` | empty (no namespace is protected) | namespace label (`"true"`) that makes a restart ask you to type the name |
| `plugins.kubernetes.conventions.tenantLabel` | empty | namespace label that puts it under Tenants |
| `plugins.flux.suspendReasonAnnotation` | empty | Flux annotation shown as the reason an object is suspended |
| `plugins.kubernetes.conventions.appsFluxPrefix` | empty | namespaces applied by a Kustomization with this prefix go under Apps |
| `plugins.hardware.zfsScrubMetricPrefix` | empty (no scrub data: not queried, and the pool cards show none) | `<prefix>_last_end_timestamp_seconds`, `_last_errors`, `_in_progress`; a valid metric name when set |
| `plugins.kubernetes.backupJobs` | empty (no NAS backup row) | `[{namespace, cronjob}]`: the CronJob behind the NAS backup age; one entry for now |

### Secret key (secrets typed into the UI)

Secrets saved through the settings editor (it arrives with P4.7) are stored in
`ui-settings.yaml` encrypted (XChaCha20-Poly1305, the field path bound in, so a value copied to
another field does not decrypt). The key comes from the process environment only; there is no
default and dash never generates one. Without a key, secrets can only be set through env and
file references, and
saving one from the UI is refused with an error that names `DASH_SECRET_KEY`. The rest of dash
runs as usual.

```sh
openssl rand -base64 32 > /run/secrets/dash-key   # 32 random bytes, base64
DASH_SECRET_KEY_FILE=/run/secrets/dash-key        # or DASH_SECRET_KEY=<the key>
```

Setting both `DASH_SECRET_KEY` and `DASH_SECRET_KEY_FILE` is an error. Keep the key where you
keep other secrets and back it up: a stored secret cannot be recovered without it. If the store
holds an encrypted value that the configured keys cannot open (no key, the wrong key, a damaged
value, a value moved to another field), dash does not start and lists every affected field. It
never drops, blanks or regenerates a stored secret.

To rotate the key:

1. Generate a new key and start dash with it as `DASH_SECRET_KEY` and the old one as
   `DASH_SECRET_KEY_PREVIOUS` (or `DASH_SECRET_KEY_PREVIOUS_FILE`). Both open stored values.
2. On that start dash re-encrypts every value that the previous key sealed, records
   `secrets.rekey` in the audit log and logs one INFO line. On a read-only store it logs a
   warning that the rekey is pending and keeps running on both keys.
3. Once the log shows the rekey, remove `DASH_SECRET_KEY_PREVIOUS` and restart.

When the settings editor replaces or clears a secret it also rewrites the retained
`ui-settings.yaml.bak.N` copies without the old ciphertext, so restoring a backup cannot bring a
cleared secret back.

### Audit log

Every action, silence change and stored-secret change is logged as an `audit` record on
stdout. When `dataDir` is writable dash also appends each record to `audit.jsonl` there: one
JSON line with the actor, what was done, the target and the result, never a secret or a changed
value (only the field paths). Refused requests (CSRF, unauthenticated, stream caps) are recorded
too, at most once per peer and reason per minute. Each line carries the SHA-256 of the previous
one, so an edited, deleted or reordered line is detectable. At 10 MiB the file rotates to
`audit.jsonl.1` (newest) to `audit.jsonl.5` (oldest); older records are dropped.

- **Without a writable data dir** (missing, read-only, or demo mode) the records go to stdout
  only and dash logs one INFO line saying so.
- **One writer per data dir.** dash locks `.audit.lock` in `dataDir`; a second instance on the
  same directory refuses to start, even when its settings are read-only.
- **No record, no change.** An action, silence or secret change first records its intent in
  the file. If that cannot be written the change is refused (actions and silences answer
  `503 audit log unavailable`) and nothing changes. If `audit.jsonl` itself cannot be opened
  in a writable data dir, dash does not start.

`dash audit verify [--dir DIR]` (default: the configured data directory) checks the chain
across the rotated files and lists intents that never got an outcome, such as an action in
flight when dash crashed; it exits 1 on a broken chain or a dangling intent. A last line without
a newline (a crash mid-write) is reported as a torn tail and does not fail the check; dash drops
it on the next start. The chain cannot see an edit to the very last line or a truncated tail.

### Plugin routes, actions and error codes

`GET /api/v1/plugins` lists every plugin instance with its state, topics, mounted actions and
navigation (the same rows arrive on the `core.plugins` stream topic). A running plugin's own
read routes live under `/api/v1/plugins/<id>/…`, and its actions are
`POST /api/v1/plugins/<id>/actions/<name>`; those exist only while `plugins.<id>.actions.enabled`
is set and the instance runs, and are otherwise `404`. Each action request runs the same
chain: the principal's role, a rate limit of 10 actions a minute per user, the body checks
(JSON only, unknown fields refused, `required` and `enum` honoured), `plugins.<id>.actions.denyNamespaces`,
the audit intent, the action, then the audit outcome. A refusal is audited and the action does
not run.

Every error answer is `{"error": "<text for a person>", "code": "<reason>"}`. Clients branch on
`code`: `invalid_input`, `unauthenticated`, `forbidden`, `csrf_failed`, `not_found`, `conflict`,
`rate_limited`, `too_many_streams`, `upstream_unavailable`, `audit_unavailable`, `unknown_host`
or `internal`.

### Breaking: no site defaults

Earlier versions defaulted to one homelab's service addresses and names. They are gone. An empty
upstream URL turns that source off: dash makes no request to it and its panels show
"source not configured". If you relied on a default, set it explicitly:

| Removed default | Set |
|---|---|
| `http://vmsingle-vm.monitoring.svc:8428` | `plugins.prometheus.url` |
| `http://vmalertmanager-vm.monitoring.svc:9093` | `plugins.alertmanager.url` |
| `http://vl-server.monitoring.svc:9428` | `plugins.victorialogs.url` |
| `http://gatus.gatus.svc:8080` | `plugins.gatus.url` |
| `homelab.io/protected`, `homelab.io/tenant` | `plugins.kubernetes.conventions.protectedLabel`, `.tenantLabel` |
| `homelab.io/suspended` | `plugins.flux.suspendReasonAnnotation` |
| `app-` | `plugins.kubernetes.conventions.appsFluxPrefix` |
| `homelab_zfs_scrub` | `plugins.hardware.zfsScrubMetricPrefix` |
| `rustfs` / `nas-backup` | `plugins.kubernetes.backupJobs` (`[{namespace: rustfs, cronjob: nas-backup}]`) |

### Removed variable names

dash 0.5.0 no longer reads the names it used before the config file; it refuses to start and
tells you the replacement.

| Removed | Replacement |
|---|---|
| `DASH_PUBLIC_URL`, `DASH_LISTEN`, `DASH_HEALTH_LISTEN` | `DASH_SERVER_PUBLIC_URL`, `DASH_SERVER_LISTEN`, `DASH_SERVER_HEALTH_LISTEN` |
| `DASH_ALLOWED_HOSTS` (comma-separated) | `DASH_SERVER_ALLOWED_HOSTS` (JSON list, e.g. `["dash.example.com"]`) |
| `DASH_VM_URL`, `DASH_ALERTMANAGER_URL`, `DASH_VLOGS_URL`, `DASH_GATUS_URL` | `DASH_PLUGINS_PROMETHEUS_URL`, `DASH_PLUGINS_ALERTMANAGER_URL`, `DASH_PLUGINS_VICTORIALOGS_URL`, `DASH_PLUGINS_GATUS_URL` |
| `DASH_PROXMOX_URL`, `DASH_PROXMOX_NODE` | `DASH_PLUGINS_PROXMOX_URL`, `DASH_PLUGINS_PROXMOX_NODE` |
| `DASH_PROXMOX_TOKEN` (`<user>!<token>=<secret>`) | `DASH_PLUGINS_PROXMOX_TOKEN_ID` and `DASH_PLUGINS_PROXMOX_TOKEN_SECRET` |
| `DASH_HOST_NODENAME`, `DASH_HOST_MODEL` | `DASH_PLUGINS_HARDWARE_HOSTS` (JSON) |
| `DASH_HEADLAMP_URL`, `DASH_HEADLAMP_CLUSTER`, `DASH_GRAFANA_URL` | `DASH_PLUGINS_HEADLAMP_URL`, `DASH_PLUGINS_HEADLAMP_CLUSTER`, `DASH_PLUGINS_GRAFANA_URL` |
| `DASH_LAUNCHER_FILE` | `DASH_PLUGINS_LAUNCHER_FILE` |
| `DASH_LABEL_PROTECTED`, `DASH_LABEL_TENANT`, `DASH_APPS_FLUX_PREFIX` | `DASH_PLUGINS_KUBERNETES_CONVENTIONS_PROTECTED_LABEL`, `…_TENANT_LABEL`, `…_APPS_FLUX_PREFIX` |
| `DASH_ANNOTATION_SUSPEND_REASON` | `DASH_PLUGINS_FLUX_SUSPEND_REASON_ANNOTATION` |
| `DASH_ZFS_SCRUB_METRIC_PREFIX` | `DASH_PLUGINS_HARDWARE_ZFS_SCRUB_METRIC_PREFIX` |
| `DASH_NAS_BACKUP_NAMESPACE`, `DASH_NAS_BACKUP_CRONJOB` | `DASH_PLUGINS_KUBERNETES_BACKUP_JOBS` (JSON) |

The `_FILE` form of each removed name is refused the same way. `DASH_ALLOWED_HOSTS` was
comma-separated; `DASH_SERVER_ALLOWED_HOSTS` is a JSON list. `DASH_PROXMOX_TOKEN=user@realm!name=secret`
is two variables now: split the old value at the first `=`. `DASH_DEV`, `DASH_DEMO`, `DASH_CONFIG`
and `KUBECONFIG` are unchanged: `KUBECONFIG` fills `plugins.kubernetes.kubeconfig` (the generated
name wins), with no `_FILE` form because it is another tool's variable.

## Layout

| Path | What |
|---|---|
| `cmd/dash` | entry point, listeners, shutdown |
| `internal/model` | the API contract (Go structs → `web/src/api/types.ts` via `make generate`) |
| `internal/config` | layered configuration: defaults, config file, environment (`DASH_*`) |
| `internal/jsonschema` | JSON Schema from Go structs, for `dash config schema` and plugin config |
| `internal/plugin` | the plugin contract, registry and supervisor; `plugintest` has the fakes |
| `internal/auth` | who a request acts as: role and principal |
| `internal/kube` | informers and the actions' Kubernetes patches |
| `internal/upstream` | VictoriaMetrics, Alertmanager, VictoriaLogs, Gatus, Proxmox clients |
| `internal/topics` | topic builders, health and attention rules, and the shims that stand for each integration in the plugin registry until it becomes a plugin |
| `internal/hub`, `internal/api` | SSE hub, REST, middleware |
| `internal/links` | Headlamp, Proxmox and Grafana deep links |
| `internal/demo` | fake data for `DASH_DEMO=true` |
| `web/` | React 19, Vite, Tailwind v4, shadcn/ui and Magic UI |

## Develop

Tools are pinned in `mise.toml` (`mise install`). The homelab checkout must sit next to this one:
dev mode uses its kubeconfig and its 1Password broker (`../homelab/scripts/hl-op`), and reaches
in-cluster services through the API server's service proxy, so no port-forwards are needed.
Copy `dev/site.env.example` to `dev/site.env` and fill in your lab's URLs; `make dev-api` loads it.

```sh
make dev-api   # Go API on 127.0.0.1:8080 (DASH_DEV=true)
make dev-web   # Vite on http://localhost:5173, proxying /api to the Go API
```

`DASH_DEV=true` signs every request in as `dev` without authentication, so dash refuses to
start unless `server.listen` (`DASH_SERVER_LISTEN`) is a loopback address (`127.0.0.1:8080`,
`[::1]:8080`, `localhost:8080`).

Open http://localhost:5173. `make check` runs everything CI runs: the generated types must be
current, `go vet`, `go test`, `tsc`, Biome and the production build.

After changing a struct in `internal/model`, run `make generate` and commit
`web/src/api/types.ts` with it.

### Pre-push hook (opt-in)

CI runs only on `main` and tags, so `.githooks/pre-push` is the pre-merge gate. It diffs the
pushed commits against the remote (a new branch against its merge-base with `origin/main`) and
checks only what changed, usually in a few seconds:

- Go files: `go vet` and `go test` on the changed packages (`./...` if `go.mod`/`go.sum` changed
  or a package was deleted). Packages that import a changed package are not re-tested.
- `internal/model`, `tygo.yaml` or `web/src/api/types.ts`: the `make generate` drift check.
- `web/`: `tsc --noEmit`, and Biome on the changed files (all of `web/` if its config, package
  or lock files changed). Skipped with a hint if `web/node_modules` is missing (`npm ci`).
- Anything else (docs, CI, Dockerfile): nothing; the hook exits at once.

It never runs `vite build` (too slow for a push); that, and checks across every package, stay in
`make check`, which is still the full gate. Checks see the working tree, not a clean checkout of
the pushed commits. On an empty Go build cache the first Go check compiles client-go (about a
minute); after that a push takes a few seconds.

```sh
make hooks              # git config core.hooksPath .githooks
make hooks-uninstall    # git config --unset core.hooksPath
git push --no-verify    # skip once
DASH_SKIP_HOOKS=1 git push
```

`core.hooksPath` replaces `.git/hooks`, so hooks kept there stop running while it is set.

## Release

1. On `main` with CI green: `git tag vX.Y.Z && git push origin vX.Y.Z`.
2. CI on github-runner-1 (`release` job) pushes `registry.home.techdufus.com/dash/dash:X.Y.Z`
   and prints `image: registry.home.techdufus.com/dash/dash:X.Y.Z@sha256:<digest>` in the log
   and the job summary. Tags are create-only: a version can never be pushed twice.
3. In the homelab repo, bump the `image:` line of the `dash` container in
   `kubernetes/apps/dash/app/deployment.yaml` to that tag and digest, then `task validate`,
   commit (signed), push and `task k8s:status`.

## License

MIT. See [LICENSE](LICENSE).
