<p align="center">
  <img src="docs/images/banner.png" alt="dash: Proxmox, Kubernetes, Flux, storage, alerts and service status for the homelab on one live screen">
</p>

<p align="center">
  <a href="#features">Features</a> •
  <a href="#screenshots">Screenshots</a> •
  <a href="#try-it-with-demo-data">Demo mode</a> •
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

```sh
make dev-api   # Go API on 127.0.0.1:8080 (DASH_DEV=true)
make dev-web   # Vite on http://localhost:5173, proxying /api to the Go API
```

Open http://localhost:5173. `make check` runs everything CI runs: the generated types must be
current, `go vet`, `go test`, `tsc`, Biome and the production build.

After changing a struct in `internal/model`, run `make generate` and commit
`web/src/api/types.ts` with it.

## Release

1. On `main` with CI green: `git tag vX.Y.Z && git push origin vX.Y.Z`.
2. CI on github-runner-1 (`release` job) pushes `registry.home.techdufus.com/dash/dash:X.Y.Z`
   and prints `image: registry.home.techdufus.com/dash/dash:X.Y.Z@sha256:<digest>` in the log
   and the job summary. Tags are create-only: a version can never be pushed twice.
3. In the homelab repo, bump the `image:` line of the `dash` container in
   `kubernetes/apps/dash/app/deployment.yaml` to that tag and digest, then `task validate`,
   commit (signed), push and `task k8s:status`.
