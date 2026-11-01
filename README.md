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
- **Live, not polled from the browser.** Informers and upstream pollers rebuild nine topics
  (`overview`, `host`, `vms`, `cluster`, `flux`, `storage`, `alerts`, `status`, `apps`) and push
  only what changed over a single SSE stream.
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
    tokenSecret: <secret>
  hardware:
    hosts: [{nodename: pve, model: Dell R730}]
```

Every setting also has an environment variable: `DASH_` plus the path in UPPER_SNAKE case
(`plugins.proxmox.tokenSecret` is `DASH_PLUGINS_PROXMOX_TOKEN_SECRET`). Lists and objects are JSON
(`DASH_PLUGINS_HARDWARE_HOSTS='[{"nodename":"pve"}]'`). Append `_FILE` to read the value from a
file (`DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE=/run/secrets/pve`); setting both forms is an
error. A misspelled `DASH_PLUGINS_*` variable stops dash from starting, and any other unknown
`DASH_*` variable is logged. `dash config env` prints every path with its variable name.

| Setting (file path) | Default | What |
|---|---|---|
| `server.publicURL` | required (dev and demo: `http://localhost:5173`) | the URL people open, with scheme and host (`https://dash.example.com`); the only `Origin` accepted on writes |
| `server.listen` | `127.0.0.1:8080` | API and SPA, reached through oauth2-proxy |
| `server.allowedHosts` | empty | extra Host names the API answers besides the `server.publicURL` host (a JSON list in the environment; `["*"]` accepts any) |
| `server.healthListen` | `0.0.0.0:8081` | `/healthz` and `/readyz` |
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
| `plugins.kubernetes.kubeconfig` | in-cluster | used outside the cluster; `KUBECONFIG` is honoured too |

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
| `plugins.hardware.zfsScrubMetricPrefix` | empty (scrub panel hidden) | `<prefix>_last_end_timestamp_seconds`, `_last_errors`, `_in_progress`; a valid metric name when set |
| `plugins.kubernetes.backupJobs` | empty (no NAS backup row) | `[{namespace, cronjob}]`: the CronJob behind the NAS backup age; one entry for now |

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

### Legacy variable names

The names dash used before the config file keep working for one release. Each one logs a
deprecation warning naming its replacement, and the generated name wins when both are set.

| Legacy | Replacement |
|---|---|
| `DASH_PUBLIC_URL`, `DASH_LISTEN`, `DASH_HEALTH_LISTEN` | `DASH_SERVER_PUBLIC_URL`, `DASH_SERVER_LISTEN`, `DASH_SERVER_HEALTH_LISTEN` |
| `DASH_ALLOWED_HOSTS` (comma-separated) | `DASH_SERVER_ALLOWED_HOSTS` (JSON list) |
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

`DASH_DEV`, `DASH_DEMO` and `DASH_CONFIG` are process settings, not part of the config tree.

## Layout

| Path | What |
|---|---|
| `cmd/dash` | entry point, listeners, shutdown |
| `internal/model` | the API contract (Go structs → `web/src/api/types.ts` via `make generate`) |
| `internal/config` | layered configuration: defaults, config file, environment (`DASH_*`) |
| `internal/kube` | informers and the actions' Kubernetes patches |
| `internal/upstream` | VictoriaMetrics, Alertmanager, VictoriaLogs, Gatus, Proxmox clients |
| `internal/topics` | topic builders, health and attention rules |
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
start unless `DASH_LISTEN` is a loopback address (`127.0.0.1:8080`, `[::1]:8080`, `localhost:8080`).

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
