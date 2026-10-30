# dash

Mission control for the homelab: one Go binary (stdlib `net/http`, client-go informers) that
serves an embedded React SPA and a live JSON/SSE API over Proxmox, Kubernetes, Flux, the R730,
storage, Alertmanager and Gatus. It runs in the Talos cluster as `kubernetes/apps/dash` in
[TechDufus/homelab](https://github.com/TechDufus/homelab), behind an oauth2-proxy sidecar that
admits only the GitHub user `TechDufus`, at https://dash.home.techdufus.com.

Writes are limited to three safe operations: a workload rollout restart, a Flux reconcile
request, and Alertmanager silences that expire. The homelab guardrail
`kubernetes/platform/guardrails/policy-dash.yaml` fences the ServiceAccount to exactly those.

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
