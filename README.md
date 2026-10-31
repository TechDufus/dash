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

dash reads `DASH_*` environment variables and refuses to start when a required one is missing.
Nothing about a particular lab is compiled in: an unset Headlamp, Grafana or Proxmox URL simply
drops those links.

| Variable | Default | What |
|---|---|---|
| `DASH_PUBLIC_URL` | required (dev: `http://localhost:5173`) | the URL people open; the only `Origin` accepted on writes |
| `DASH_LISTEN` | `127.0.0.1:8080` | API and SPA, reached through oauth2-proxy |
| `DASH_HEALTH_LISTEN` | `0.0.0.0:8081` | `/healthz` and `/readyz` |
| `DASH_VM_URL` | `http://vmsingle-vm.monitoring.svc:8428` | VictoriaMetrics |
| `DASH_ALERTMANAGER_URL` | `http://vmalertmanager-vm.monitoring.svc:9093` | Alertmanager |
| `DASH_VLOGS_URL` | `http://vl-server.monitoring.svc:9428` | VictoriaLogs |
| `DASH_GATUS_URL` | `http://gatus.gatus.svc:8080` | Gatus |
| `DASH_PROXMOX_TOKEN` | empty (Proxmox off) | API token, `<user>!<token>=<secret>` |
| `DASH_PROXMOX_URL` | required with a token | Proxmox API |
| `DASH_PROXMOX_NODE` | discovered from the API when the cluster has one node | the node to read; set it for multi-node clusters or to skip discovery |
| `DASH_HOST_NODENAME` | `DASH_PROXMOX_NODE` (or the discovered node) | `nodename` label of the host's node-exporter and IPMI series |
| `DASH_HOST_MODEL` | empty | shown next to the host's name, e.g. `Dell R730` |
| `DASH_HEADLAMP_URL`, `DASH_HEADLAMP_CLUSTER` | empty, `main` | Headlamp deep links |
| `DASH_GRAFANA_URL` | empty | Grafana links |
| `DASH_LAUNCHER_FILE` | empty | YAML list of infrastructure links for the launcher |
| `KUBECONFIG` | in-cluster | used outside the cluster |

The launcher file is a list of `name`, `href`, `icon` and `description`; `icon` is a
[dashboard-icons](https://github.com/homarr-labs/dashboard-icons) name or an image URL. Mount it
from a ConfigMap and point `DASH_LAUNCHER_FILE` at it. See
[`docs/launcher.example.yaml`](docs/launcher.example.yaml).

dash also reads a few labels, annotations and metric names from the cluster. The defaults match
the homelab it was built for; change them to match yours.

| Variable | Default | What |
|---|---|---|
| `DASH_LABEL_PROTECTED` | `homelab.io/protected` | namespace label (`"true"`) that makes a restart ask you to type the name |
| `DASH_LABEL_TENANT` | `homelab.io/tenant` | namespace label that puts it under Tenants |
| `DASH_ANNOTATION_SUSPEND_REASON` | `homelab.io/suspended` | Flux annotation shown as the reason an object is suspended |
| `DASH_APPS_FLUX_PREFIX` | `app-` | namespaces applied by a Kustomization with this prefix go under Apps |
| `DASH_ZFS_SCRUB_METRIC_PREFIX` | `homelab_zfs_scrub` | `<prefix>_last_end_timestamp_seconds`, `_last_errors`, `_in_progress` |
| `DASH_NAS_BACKUP_NAMESPACE`, `DASH_NAS_BACKUP_CRONJOB` | `rustfs`, `nas-backup` | the CronJob behind the NAS backup age |

## Layout

| Path | What |
|---|---|
| `cmd/dash` | entry point, listeners, shutdown |
| `internal/model` | the API contract (Go structs → `web/src/api/types.ts` via `make generate`) |
| `internal/config` | environment variables (`DASH_*`) |
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
