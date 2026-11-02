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
  (`flux`), the kubernetes plugin publishes `kubernetes.cluster`, `kubernetes.storage` and `kubernetes.apps`, the hardware plugin publishes `hardware.hosts` and `hardware.machines`, the proxmox plugin publishes `proxmox.vms`, `proxmox.node` and `proxmox.storage`, the alertmanager and gatus plugins `alertmanager.alerts` and `gatus.status`, the core composes
  `core.overview` from what the integrations contribute, and only what changed goes out over a
  single SSE stream. `core.plugins` says which integrations run and whether each is healthy; the
  launcher plugin publishes its links as `launcher.links`.
- **Proxmox and the hardware.** VMs with their Kubernetes node, config, snapshots and charts;
  IPMI power, temperatures, fans and PSUs; ZFS pools and per-bay SMART health.
- **Kubernetes and GitOps.** Nodes, workloads, pods with logs and metrics, events, namespaces,
  certificates and External Secrets; every Flux source, Kustomization and HelmRelease with its
  revision and signature verification.
- **Storage and alerts.** PVC fill and growth with days-to-full, object storage buckets, Proxmox
  storage, CNPG clusters and their backups, the age of the last backup CronJob runs you name, etcd
  size; firing Alertmanager alerts and their silences.
- **Deep links everywhere.** Headlamp, Proxmox consoles and Grafana are one click from the object
  you are looking at, and a command palette (`Ctrl K`) jumps to any page, VM, node,
  workload or pod.
- **Safe by design.** Writes are limited to three operations: a workload rollout restart (off
  until `plugins.kubernetes.actions.enabled` is set), a Flux reconcile request, and Alertmanager
  silences that expire (off until `plugins.alertmanager.actions.enabled` is set). The homelab
  guardrail `kubernetes/platform/guardrails/policy-dash.yaml` fences the ServiceAccount to exactly
  those. dash also starts and serves Proxmox with no cluster at all: the kubernetes plugin is then
  `unavailable` and its pages are hidden.

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
    hosts: [{nodename: pve, model: Example 2U}]
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
where dash runs; `--resolve-secrets` reads them. A `k8s:` reference is only checked for its
syntax, with or without that flag.

Secret fields (`plugins.proxmox.tokenSecret`) take a literal, `env:NAME` (the value of that
variable), `file:/path` (the file's contents, trimmed) or `k8s:<namespace>/<name>#<key>` (a key of
a Kubernetes Secret, see [Secrets from Kubernetes](#secrets-from-kubernetes-k8s)); `..._FILE` is
shorthand for a `file:` reference. A literal in the config file logs a warning, because such files
often live in git. An `env:` or `file:` reference dash cannot read stops it from starting and
names the field, never the value. dash
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
| `server.metrics` | `false` | serve `/metrics` on the health listener (see Metrics below) |
| `server.limits.streamsPerUser`, `.streams` | 16, 256 | open `/api/v1/stream` connections per user and in all; more get 429 or 503 |
| `server.limits.tailsPerUser`, `.tails` | 4, 32 | the same for plugin streams, that is the pod log tail, `/api/v1/plugins/victorialogs/logs/tail` |
| `server.limits.tailMaxDuration` | `30m` | a log tail ends after this; the browser reconnects (a duration string, also in the environment) |
| `server.limits.sseWriteTimeout`, `.bodyReadTimeout` | `10s`, `10s` | a stalled stream write, or a request body that does not arrive, is cut off after this |
| `server.limits.maxHeaderBytes` | `65536` | request header size of the API listener |
| `settings.ui` | `editable` | `editable` or `readonly` (fully managed) |
| `dataDir` (`DASH_DATA_DIR`) | `/data` | where dash keeps its state and looks for `config.yaml` |
| `plugins.prometheus.url` | empty (source not configured) | VictoriaMetrics or Prometheus; a change of any `plugins.prometheus.*` setting applies without a restart |
| `plugins.prometheus.timeout` | `5s` | how long one query may take before dash gives up on it |
| `plugins.prometheus.auth.basic.username`, `.password` / `.auth.bearerToken` | empty (no credentials) | Basic or bearer credentials for the service, not both; the password and the token take a literal, `env:NAME`, `file:/path` or `k8s:namespace/name#key`, and a rotated file applies without a restart |
| `plugins.prometheus.headers` | empty | extra headers sent with every request, `{name: value}` (a JSON object in the environment). Plain configuration, not secrets: put credentials in `auth`. `Authorization`, `Proxy-Authorization`, `Cookie` and `Host` cannot be set |
| `plugins.prometheus.tls.caFile`, `.insecureSkipVerify` | empty, `false` | a PEM file with the CA that signed the service's certificate, added to the system roots; `insecureSkipVerify` skips verification (prefer `caFile`) |
| `plugins.alertmanager.url` | empty (source not configured) | Alertmanager; a change of any `plugins.alertmanager.*` setting applies without a restart of dash |
| `plugins.alertmanager.auth.basic.username`, `.password` / `.auth.bearerToken` | empty (no credentials) | Basic or bearer credentials for Alertmanager, not both; the password and the token take a literal, `env:NAME`, `file:/path` or `k8s:namespace/name#key`, and a rotated file applies without a restart |
| `plugins.alertmanager.tls.caFile`, `.insecureSkipVerify` | empty, `false` | a PEM file with the CA that signed Alertmanager's certificate, added to the system roots; `insecureSkipVerify` skips verification (prefer `caFile`) |
| `plugins.alertmanager.ignoreAlerts` | `["Watchdog","InfoInhibitor"]` | alert names dash drops everywhere: the alert list, the attention items and the firing count (a JSON list in the environment) |
| `plugins.alertmanager.severityMap` | `{"critical":"crit","warning":"warn"}` | how an alert's `severity` label value becomes dash's severity, `crit` or `warn`; an alert whose value is not listed is not attention (a JSON object in the environment) |
| `plugins.alertmanager.silenceDurations` | `["1h","4h","24h","7d"]` | the silence lengths the dialog offers, from those four (a JSON list in the environment) |
| `plugins.alertmanager.protectedAlerts` | `["Watchdog"]` | alert names nobody can silence through dash, whatever the role: the Alerts page has no Silence button for them and `silence.create` answers 403 (a JSON list in the environment) |
| `plugins.alertmanager.actions.enabled` | `false` | mounts the silence actions (`silence.create`, `silence.expire`); only silences dash created (`createdBy` starting `dash:`) can be expired through it. `DASH_PLUGINS_ALERTMANAGER_ACTIONS_ENABLED=true`; a change restarts the plugin |
| `plugins.victorialogs.url` | empty (source not configured) | VictoriaLogs, for pod logs; a change of any `plugins.victorialogs.*` setting applies without a restart of dash, and the pod sheet's Logs tab appears and disappears with the plugin |
| `plugins.victorialogs.auth.basic.username`, `.password` / `.auth.bearerToken` | empty (no credentials) | Basic or bearer credentials for VictoriaLogs, not both; the password and the token take a literal, `env:NAME`, `file:/path` or `k8s:namespace/name#key`, and a rotated file applies without a restart |
| `plugins.victorialogs.tls.caFile`, `.insecureSkipVerify` | empty, `false` | a PEM file with the CA that signed VictoriaLogs's certificate, added to the system roots; `insecureSkipVerify` skips verification (prefer `caFile`) |
| `plugins.victorialogs.fields.namespace`, `.pod`, `.container` | `kubernetes.pod_namespace`, `kubernetes.pod_name`, `kubernetes.container_name` | the fields of your log records that name the pod a line came from, for the query and for the pod and container shown with each line; letters, digits, `_` and `.`, not starting with a digit or a dot. The fields that share a name's prefix up to its first dot (`kubernetes.` by default) are left out of the message text |
| `plugins.victorialogs.limit` | `500` | the most lines one log query returns; greater than zero |
| `plugins.victorialogs.denyNamespaces` | `[]` | namespaces whose logs dash never shows, whatever the role: the query and the tail answer 403 and the refusal is audited (a JSON list in the environment) |
| `plugins.gatus.url` | empty (source not configured) | Gatus; a change of any `plugins.gatus.*` setting applies without a restart of dash |
| `plugins.gatus.auth.basic.username`, `.password` / `.auth.bearerToken` | empty (no credentials) | Basic or bearer credentials for Gatus, not both; the password and the token take a literal, `env:NAME`, `file:/path` or `k8s:namespace/name#key`, and a rotated file applies without a restart |
| `plugins.gatus.tls.caFile`, `.insecureSkipVerify` | empty, `false` | a PEM file with the CA that signed Gatus's certificate, added to the system roots; `insecureSkipVerify` skips verification (prefer `caFile`) |
| `plugins.gatus.criticalGroups` | `[]` | the Gatus groups whose failing checks are critical (matched without regard to case); a failing check in any other group is a warning, so with `[]` every failure is a warning. The Status page lists these groups first, then the others alphabetically. The old built-in rule made `infrastructure` critical: set `DASH_PLUGINS_GATUS_CRITICAL_GROUPS='["infrastructure"]'` to keep it (a JSON list in the environment) |
| `plugins.gatus.pageSize` | `30` | how many check results Gatus returns per endpoint (its `pageSize` query parameter), drawn as each endpoint's history on the Status page; greater than zero |
| `plugins.proxmox.tokenID`, `.tokenSecret` | empty (Proxmox off) | API token `<user>!<token>` and its secret (a literal, `env:NAME`, `file:/path` or `k8s:namespace/name#key`); `url`, `tokenID` and `tokenSecret` all set turn the plugin on, and with none of the token set it is `disabled` (a URL alone leaves it `disabled`; with a `k8s:` token still being read the row says it waits for a Kubernetes Secret). A token ID without its secret (or the secret without the ID), or a token without a `url`, is a configuration error and dash does not start; a change applies without a restart |
| `plugins.proxmox.url` | required with a token | Proxmox API; with no token it leaves Proxmox `disabled` |
| `plugins.proxmox.node` | discovered from the API when the cluster has one node | the node to read; set it for multi-node clusters or to skip discovery. The Hardware plugin reads the host's node from the running Proxmox plugin, so without `hosts[0].nodename` the Hardware row stays `unavailable` ("waiting for the Proxmox node name") until Proxmox runs (URL, token ID and token secret set): v0.7.0 used this setting as the host's node even without a token |
| `plugins.proxmox.tls.caFile`, `.insecureSkipVerify` | empty, `false` | a PEM file with the CA that signed the Proxmox certificate, added to the system roots; `insecureSkipVerify` skips verification (prefer `caFile`) and makes dash log a startup WARN and list an `ok` source named `tls: verification disabled` in the Overview's sources (not on the Proxmox row, and drawn as no chip: chips show failing sources only) |
| `plugins.proxmox.criticalVMs.ids`, `.tags` | empty (no VM is critical) | a stopped VM whose id is in `ids`, or that carries one of `tags` (matched without regard to case), is critical; any other stopped VM is a warning (JSON lists in the environment: `DASH_PLUGINS_PROXMOX_CRITICAL_VMS_IDS='[101,102]'`) |
| `plugins.proxmox.storage.source` | `api` | where the storage list comes from: `api` (the token needs the `Datastore.Audit` privilege on `/storage`) or `prometheus` (the pve-exporter series through the prometheus plugin) |
| `plugins.proxmox.series.vmSelector` | `id="qemu/{vmid}"` | the one label matcher that selects a VM in the pve-exporter series behind the VM charts, with `{vmid}` once |
| `plugins.hardware.hosts` | empty | `[{name, nodename, model, bays, fanMaxRPM, sensorAliases}]`, one entry for now, every key optional (`[{}]` reads the node of the running Proxmox plugin, which needs its URL, token ID and token secret): `name` is the host's name in the web UI (default: the Proxmox node, then `nodename`); `nodename` is the label of the host's node-exporter and IPMI series (default: the node of the running Proxmox plugin, that is `plugins.proxmox.node` or the discovered node; set it to run without a Proxmox token); `model` is shown next to the name, e.g. `Example 2U`; `bays` is the number of drive slots the Disks grid draws (0 to 128; 0 numbers the slots from 1, or from 0 when a disk sits in slot 0, up to the highest slot that holds a disk, at most 128 slots, and above that the page lists the disks sorted); `fanMaxRPM` is the top of the fan gauges (0 scales them to the fastest fan, and a gauge never ends below the speed a fan reports; the hosts payload calls it `fanMaxRpm`); `sensorAliases` is `{inlet, exhaust}`, the text the names of the Inlet and Exhaust temperature sensors contain, ignoring case (`inlet` and `exhaust` when empty). A JSON list in the environment: `DASH_PLUGINS_HARDWARE_HOSTS='[{"model":"Example 2U","bays":16,"fanMaxRPM":18000}]'` |
| `plugins.hardware.machines` | empty | machines outside the cluster to show as up or down: `[{name, nodeExporterNodename}]` (up while the node-exporter with that `nodename` label is scraped) or `[{name, gatusEndpoint}]` (up while that Gatus check passes, compared without regard to case; a check that does not exist, or no gatus plugin, counts as down). Exactly one of the two per entry, names unique ignoring case and at most 64 bytes. A JSON list in the environment |
| `plugins.hardware.selectors.nodeExporterJob`, `.hwmonChip` | `node-exporter`, `platform_coretemp_.*` | the `job` label of the node-exporter targets (which the machines' `up` query selects) and the RE2 expression for the `chip` label of the CPU package temperature (`node_hwmon_temp_celsius`) |
| `plugins.hardware.tempThresholds.warn`, `.crit` | `85`, `90` | the CPU package temperature, in whole °C, from which the host is warning and critical; `0 < warn < crit` |
| `plugins.headlamp.url`, `.cluster` | empty, `main` | Headlamp deep links; a change applies without a restart |
| `plugins.grafana.url` | empty | Grafana links (the alert list on alerts, Grafana on the host); a change applies without a restart |
| `plugins.launcher.file`, `.links` | empty | the launcher's links, from a YAML or JSON file or inline (not both); the file is read when the launcher starts, so restart dash or change the launcher's settings to read it again |
| `ui.iconBaseURL` (`DASH_UI_ICON_BASE_URL`) | empty (letter tiles) | where icons come from, for launcher links and for the apps on the Apps page: a name such as `proxmox` is requested from `<iconBaseURL>/proxmox.png` (a trailing `.png`, `.svg` or `.webp` on the name is dropped first), an absolute icon URL is used only under it, and homepage-style `mdi-`, `si-` and `sh-` names show no icon. With it empty no icon is requested and every app and link is a letter tile: app icons no longer come from a CDN by default. Set it to `https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png` to keep icons by name; a restart applies it |
| `plugins.kubernetes.mode` | `auto` | how dash reaches the cluster: `auto` uses `plugins.kubernetes.kubeconfig` (or `KUBECONFIG`) when set, else the in-cluster configuration, else runs without a cluster (the plugin is `unavailable`, its pages are hidden, and dash still serves everything else); `inCluster` and `kubeconfig` insist on that one source and report an error when it is missing. A change of any `plugins.kubernetes.*` setting applies live: the kubernetes plugin restarts, and so does `proxmox` |
| `plugins.kubernetes.kubeconfig`, `.context` | in-cluster, the kubeconfig's current context | `kubeconfig` is used outside the cluster (development); the standard `KUBECONFIG` fills it too (this setting's own variable wins), with no `_FILE` form. `context` picks the kubeconfig's context and is read only with a kubeconfig |
| `plugins.kubernetes.qps`, `.burst` | `20`, `40` | the rate limits of dash's Kubernetes client; `0` or unset means the default, a negative value is refused, and `burst` must be at least `qps` |
| `plugins.kubernetes.etcd.job` | `etcd` | the `job` label of etcd's metrics in Prometheus, for the etcd size |
| `plugins.kubernetes.optional.certificates`, `.externalSecrets`, `.httpRoutes` | `false` | the cluster may lack the cert-manager, external-secrets or Gateway API CRDs: with the flag set their absence is not a note and does not make the plugin `degraded`, and neither does a failed discovery of that group or a refused list of its resource |
| `plugins.kubernetes.upstreamViaAPIServer` | `false` | send the other plugins' requests to in-cluster service URLs (`http://<service>.<namespace>.svc`) through the API server's service proxy, with the Kubernetes credentials: for running dash on a laptop against a remote cluster (`DASH_DEV` no longer does this). Applies to requests made after the plugin starts |
| `plugins.kubernetes.actions.enabled` | `false` | mounts the `restart` action (a rollout restart of a Deployment, StatefulSet or DaemonSet, admin only); a namespace that the protected label marks needs the workload's name typed as a confirmation. `DASH_PLUGINS_KUBERNETES_ACTIONS_ENABLED=true`; a change restarts the plugin |
| `plugins.kubernetes.actions.denyNamespaces` | `[]` | namespaces where `restart` answers 403 and the refusal is audited (a JSON list in the environment) |
| `plugins.kubernetes.apps.gateways` | `[]` (every route) | `[{namespace, name}]` (a JSON list in the environment): only HTTPRoutes whose `spec.parentRefs` name one of these gateways are apps; a parent reference without a namespace means the route's own namespace. Empty accepts every HTTPRoute. `DASH_PLUGINS_KUBERNETES_APPS_GATEWAYS='[{"namespace":"gateways","name":"main"}]'` |
| `plugins.kubernetes.apps.excludeNamespaces` | `[]` (none) | namespaces whose routes and ingresses are never apps, typically dash's own (a JSON list in the environment) |
| `plugins.kubernetes.apps.annotationPrefix` | `gethomepage.dev` | the annotation keys read for an app: `<prefix>/name`, `/group`, `/description` and `/icon`; a trailing `/` is ignored. `/icon` is a [dashboard-icons](https://github.com/homarr-labs/dashboard-icons) name, a same-origin path or a URL under `ui.iconBaseURL`; homepage-style `mdi-`, `si-` and `sh-` names show no icon |
| `plugins.kubernetes.apps.ingress` | `false` | also reads `networking.k8s.io/v1` Ingresses, one app per rule host (`https://<host>`), with the same annotations and exclusions; needs the `ingresses` permission under [Kubernetes permissions](#kubernetes-permissions-rbac) |
| `plugins.<id>.enabled` | unset (on when configured) | `false` turns that plugin off: its topics are removed, and the web UI drops its rail entry, palette entries, overview tiles and panels and shows a "switched off" page at its address. Unset turns it on when it has the settings it needs: a plugin without them is `disabled` ("not configured: set plugins.<id>.url", naming the setting to make; the launcher names `links` or `file`; the hardware plugin names `hosts` or `machines`) and the web UI hides its pages; `true` with settings missing or invalid makes it `unconfigured`. That is how `prometheus`, `alertmanager`, `gatus`, `victorialogs`, `proxmox`, `hardware`, `kubernetes`, `grafana`, `headlamp` and `launcher` behave, and a change to any of their settings applies live: that plugin restarts, and so do the plugins that use or require it (a `prometheus` change restarts `proxmox`, `hardware` and `kubernetes`; a `gatus` change restarts `hardware` and `kubernetes`; a `proxmox` change restarts `hardware`; a `kubernetes` change restarts `proxmox`; a `grafana` change restarts `alertmanager`). `flux` has not moved to a plugin of its own yet: a change to it needs a restart |
| `plugins.<id>.type` | the section name | the plugin type; dash runs one instance per type, so leave it unset or equal to the section name |

`/healthz` answers `ok` while dash runs. `/readyz` answers `ok` once both listeners are bound and
every plugin's start has returned; a plugin in `error`, or an upstream that is down, never makes
dash unready. It no longer waits for the Kubernetes caches to fill: list `kubernetes` in
`server.readyRequires` to hold `/readyz` until the new pod has data, so a rollout keeps the old
pod serving. The hold lasts at most `server.readyTimeout` (60 s); after that dash reports ready
without the data and logs a WARN.
`GET /readyz?verbose=1` adds one `<plugin> <state> [message]` line per plugin, but only for a
loopback peer (`kubectl port-forward`, a shell in the pod); any other peer gets the plain body.

The launcher is a list of `name`, `href`, `icon` and `description`; `icon` is a
[dashboard-icons](https://github.com/homarr-labs/dashboard-icons) name (shown from
`ui.iconBaseURL`; without it, a letter tile), a same-origin path, or an image URL under
`ui.iconBaseURL`. Mount a file from a ConfigMap and point `plugins.launcher.file` at it, or list
the links in `plugins.launcher.links`. An unreadable file makes the launcher plugin report `error`
(dash still starts); an invalid link in `plugins.launcher.links` is refused when the configuration
loads. See [`docs/launcher.example.yaml`](docs/launcher.example.yaml).

dash also reads a few labels, annotations and metric names from the cluster. None has a default:
while one is empty, the feature that depends on it is off.

| Setting (file path) | Default | What |
|---|---|---|
| `plugins.kubernetes.conventions.protectedLabel` | empty (no namespace is protected) | namespace label (`"true"`) that makes a restart ask you to type the name |
| `plugins.kubernetes.conventions.tenantLabel` | empty | namespace label that puts it under Tenants |
| `plugins.flux.suspendReasonAnnotation` | empty | Flux annotation shown as the reason an object is suspended |
| `plugins.kubernetes.conventions.appsFluxPrefix` | empty | namespaces applied by a Kustomization with this prefix go under Apps |
| `plugins.hardware.zfsScrubMetricPrefix` | empty (no scrub data: not queried, and the pool cards show none) | `<prefix>_last_end_timestamp_seconds`, `_last_errors`, `_in_progress`; a valid metric name when set |
| `plugins.kubernetes.backupJobs` | empty (no backup rows) | `[{namespace, cronjob, label}]`, up to 8 (a JSON list in the environment): the age of the last success of the CronJob `cronjob`, or of one named `<cronjob>-<suffix>`, read from kube-state-metrics' `kube_cronjob_status_last_successful_time` through the prometheus plugin. With a `label` the entry is one row of that name, aged by its freshest CronJob; without one each matching CronJob is a row named after itself |

### Metrics

dash exposes metrics about itself, off by default. `server.metrics: true` (`DASH_SERVER_METRICS=true`)
makes the health listener serve `GET /metrics` in the Prometheus text format; with it off the path
answers 404. The registry is private: there are no Go or process collectors, only these series.

| Series | Labels |
|---|---|
| `dash_build_info` | `version`, `go_version` |
| `dash_http_requests_total`, `dash_http_request_duration_seconds` | `route` (the registered pattern, for example `GET /api/v1/topics/{topic}`; `static` for the web UI, `other` for what no route handled), `code` (counter only) |
| `dash_sse_streams` | `kind` (`events`, `tail`) |
| `dash_plugin_state` | `plugin`, `state`: 1 for the state an instance is in, 0 for the other seven |
| `dash_upstream_requests_total`, `dash_upstream_request_duration_seconds` | `plugin` (`prometheus`, `alertmanager`, `victorialogs`, `gatus`, `proxmox`), `outcome` (counter only: `ok`, `timeout`, `tls`, `auth`, `http_4xx`, `http_5xx`, `invalid_response`, `unreachable`) |
| `dash_actions_total` | `plugin`, `action`, `outcome` (`ok`, `error`, `denied`, `invalid`, `rate_limited`) |
| `dash_auth_failures_total` | `reason` (`unauthenticated`, `csrf`, `forbidden`, `rate_limit`, `namespace_denied`, `stream_limit`) |

Every label value comes from a fixed set: a route is a pattern, never the path asked for, and no
series carries a user, an object or a host name. The `/metrics` endpoint is unauthenticated like
the probes, and enabling it shows plugin names and states to any peer that reaches the health
listener, which `/readyz` otherwise hides from everyone but loopback. Keep that listener
cluster-internal (a ClusterIP Service and a NetworkPolicy, not an Ingress). The Kubernetes
API calls of the informers are not counted, and `invalid_response` stays at 0
until the clients report it.

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
value (only the field paths). Opening a pod log tail is recorded as `logs.tail` with the pod as
the target. Refused requests (CSRF, unauthenticated, stream caps, a denied namespace) are recorded
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

### HTTP API

The public API is under `/api/v1` (`me`, `stream`, `series`, `topics/<topic>`, `plugins`), described
by [docs/api/openapi.json](docs/api/openapi.json), an OpenAPI 3.1 document generated from the code
(`dash api openapi`; `make generate` rewrites it and `make check` fails when it drifts or does
not lint). The SPA uses the same paths. Before 1.0 a breaking change to them is called out in
the release notes. The unversioned `/api/actions/reconcile`
route is internal to the SPA and goes away as its plugin moves; `/api/pods/{ns}/{name}` is now
`/api/v1/plugins/kubernetes/pods/{ns}/{name}` and `/api/actions/restart` is the kubernetes
plugin's `restart` action; both old paths answer 404; `/api/vms/{vmid}` is now
`/api/v1/plugins/proxmox/vms/{vmid}` and the old path answers 404; `/api/logs` and `/api/logs/tail`
are now `/api/v1/plugins/victorialogs/logs` and `.../logs/tail` and the old paths answer 404; `/api/silences` is already
gone (silences are the alertmanager plugin's actions, and the old paths answer 404), and
`/api/me`, `/api/stream` and `/api/series` are now `/api/v1/me`, `/api/v1/stream` and
`/api/v1/series`, and the old paths answer 404.

Every response of the API listener carries an `X-Request-Id` (128 random bits as hex; a proxy on
the loopback address may supply its own), which also appears in the error body, the audit record
and the log line of an upstream failure. The health listener (`/healthz`, `/readyz`, `/metrics`)
sends none. `/api/v1` answers are `Cache-Control: no-store`, except a topic snapshot
(`private, no-cache` with an `ETag`) and `/api/v1/stream` (`no-cache`, as for any event stream).

### Plugin routes, actions and error codes

`GET /api/v1/plugins` lists every plugin instance with its state, topics, mounted actions and
navigation (the same rows arrive on the `core.plugins` stream topic). A running plugin's own
read routes live under `/api/v1/plugins/<id>/…`.

The framework mounts a plugin's actions as `POST /api/v1/plugins/<id>/actions/<name>` while
`plugins.<id>.actions.enabled` is true. Two plugins have them so far: the alertmanager plugin's
`silence.create` and `silence.expire`, and the kubernetes plugin's `restart` (a rollout restart
of a Deployment, StatefulSet or DaemonSet), all for admins and all answering 202. Set
`plugins.alertmanager.actions.enabled` (`DASH_PLUGINS_ALERTMANAGER_ACTIONS_ENABLED=true`) or
`plugins.kubernetes.actions.enabled` (`DASH_PLUGINS_KUBERNETES_ACTIONS_ENABLED=true`) to turn
them on; changing either restarts that plugin, and until it is set the page shows no such button.
`plugins.kubernetes.actions.denyNamespaces` lists the namespaces where `restart` answers 403
(audited). `dash config validate` rejects `plugins.<id>.actions.*` on a plugin without them, and
the Flux reconcile action stays on the SPA's internal route until the flux plugin moves. Every action
request runs one chain: the principal's role, a rate limit of 10 actions a minute per user, the
body checks (JSON only, unknown fields refused, `required` and `enum` honoured), the namespace
deny list where the plugin has one, the audit intent, the action, then the audit outcome. A
refusal is audited and the action does not run.

Every error answer is `{"error": "<text for a person>", "code": "<reason>", "requestId": "<id>"}`.
Clients branch on `code`. These are sent today: `unauthenticated`, `forbidden`, `csrf_failed`,
`not_found`, `invalid_input`, `conflict`, `too_many_streams`, `upstream_unavailable`,
`audit_unavailable`, `unknown_host`. `rate_limited` and `internal` come from plugin action
routes, so they appear only once a plugin enables actions. `insufficient_scope`, `invalid_token`,
`ambiguous_credentials`, `locked_field`, `readonly_settings`, `approval_required`,
`approval_denied`, `approval_expired` and `precondition_failed` are reserved for the client,
settings and approval work that follows; nothing sends them in 0.5.0, and the OpenAPI document
lists them all.

### Kubernetes permissions (RBAC)

The kubernetes plugin reads the cluster through informers and writes in two places only. Without
a kubeconfig or an in-cluster ServiceAccount (`plugins.kubernetes.mode: auto`) it is `unavailable`:
its pages are hidden, the other plugins keep running, and `/readyz` still answers `ok`. Discovery
and the version check use the default `system:discovery` and `system:public-info-viewer` bindings.
The permissions the plugin uses, as a `ClusterRole` for dash's ServiceAccount:

```yaml
rules:
  - apiGroups: [""]
    resources: [nodes, namespaces, pods, persistentvolumeclaims, events]
    verbs: [get, list, watch]
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets, replicasets]
    verbs: [get, list, watch]
  - apiGroups: [batch]
    resources: [jobs, cronjobs]
    verbs: [get, list, watch]
  # The restart action (plugins.kubernetes.actions.enabled): the pod template's restartedAt annotation.
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets]
    verbs: [patch]
  # The CRDs the plugin reads when the cluster has them (a missing one is a note on its row).
  - apiGroups: [cert-manager.io]
    resources: [certificates]
    verbs: [get, list, watch]
  - apiGroups: [external-secrets.io]
    resources: [externalsecrets]
    verbs: [get, list, watch]
  - apiGroups: [gateway.networking.k8s.io]
    resources: [httproutes]
    verbs: [get, list, watch]
  - apiGroups: [postgresql.cnpg.io]
    resources: [clusters]
    verbs: [get, list, watch]
  # Flux: read, and the reconcile request (an annotation).
  - apiGroups: [kustomize.toolkit.fluxcd.io]
    resources: [kustomizations]
    verbs: [get, list, watch, patch]
  - apiGroups: [helm.toolkit.fluxcd.io]
    resources: [helmreleases]
    verbs: [get, list, watch, patch]
  - apiGroups: [source.toolkit.fluxcd.io]
    resources: [gitrepositories, ocirepositories, helmrepositories]
    verbs: [get, list, watch, patch]
```

`plugins.kubernetes.upstreamViaAPIServer` (development) also needs `get` and `create` on
`services/proxy` (the API server authorizes a proxied GET as `get` and a proxied POST as `create`).
`plugins.kubernetes.apps.ingress: true` also needs `get`, `list` and `watch` on `ingresses` in
`networking.k8s.io`; that rule is not part of the ones above, so grant it only where you turn the
key on. A resource the API server serves but the ServiceAccount may not list (Ingresses here, or
the certificates, ExternalSecrets, HTTPRoutes or CNPG clusters when their permission is missing)
is published without its objects after 60 seconds, and the row is `degraded` with
`cannot list: <name> (<category>)`, where `<name>` is `ingress`, `cert-manager`,
`external-secrets`, `gateway-api` or `cnpg`, for example `cannot list: ingress (auth)`; the
topic's source note carries the same category.
A cluster that lacks a CRD group is fine: mark the kinds in `plugins.kubernetes.optional.*` to
keep the plugin out of `degraded` for it. A mark also covers a group whose discovery fails (an
aggregated API that is down) and a resource that cannot be listed.

### Secrets from Kubernetes (`k8s:`)

Any secret field takes `k8s:<namespace>/<name>#<key>`: the value of one key of a Kubernetes
Secret. The kubernetes plugin reads it, so the plugin must run (a cluster, and `plugins.kubernetes`
not disabled) and dash's ServiceAccount needs the permission below. The plugin watches each
Secret it is asked for with `fieldSelector=metadata.name=<name>` and nothing else; it never lists
Secrets, keeps no Secret object and copies only the keys the configuration names. dash can read
nothing else of the namespace.

```yaml
plugins:
  proxmox:
    url: https://pve.example.com:8006
    tokenID: dash@pve!dash
    tokenSecret: k8s:dash/proxmox#tokenSecret
```

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: dash-secrets, namespace: dash}
rules:
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: [proxmox]        # one entry per Secret a k8s: reference names in this namespace
    verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: dash-secrets, namespace: dash}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: dash-secrets}
subjects:
  - {kind: ServiceAccount, name: dash, namespace: dash}
```

`list` and `watch` are needed because a watch with a `metadata.name` selector is a `list` and a
`watch` request; `resourceNames` is what limits them to the named Secrets. Repeat the `Role` and
the `RoleBinding` in each namespace a reference names.

A plugin does not start while any `k8s:` reference in its section is unread, an optional
credential included (`auth.bearerToken` and `auth.basic.password` of the Prometheus,
Alertmanager, Gatus and VictoriaLogs sources too): it would otherwise query without the
credential. Its row says `waiting for a Kubernetes Secret (see plugins.<id>.<field>)` (once for
each such field), after `not configured: set plugins.<id>.<setting>` when something else is
missing too. Its state is `unconfigured` when its other settings are complete, otherwise
`disabled` (or `unconfigured` when `enabled: true`). The row never names the namespace, the
Secret, the key or the reason; dash's log does, once per change (`not_found`, `key_missing`,
`forbidden`, `unreachable`, `kubernetes_unavailable`). The plugin starts by itself when the
values arrive, and a rotated Secret applies to the next request without a restart. A waiting
plugin counts as settled for `/readyz`: a Secret that cannot be read never keeps dash unready.
The value is trimmed of surrounding whitespace, as an `env:` or `file:` value is (a Secret made
with `kubectl create secret --from-file` usually ends in a newline), and a blank value counts as
missing (`key_missing`). It is held in memory only: never written to disk, to the settings store,
to `core.plugins`, the stream or the log, and scrubbed like every other secret.

When the Secret is deleted, or the kubernetes plugin stops (a reload, a lost cluster), the last
value stays in use; only the reason the log shows changes, and the next successful read replaces
it. A reload that adds a `k8s:` reference to a plugin that is running stops that plugin until the
value is read, and is listed as needing a restart as for a literal added after startup.
`dash config validate` checks the syntax of the reference and reads no Secret.

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
| only routes of the gateway `gateway/home` were apps | `plugins.kubernetes.apps.gateways` (`[{namespace: gateway, name: home}]`); empty now accepts every HTTPRoute |
| the `dash` namespace was never an app | `plugins.kubernetes.apps.excludeNamespaces` (`[dash]`); empty now skips nothing |
| app icons from a CDN | `ui.iconBaseURL` (`https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png`); empty now shows letter tiles |

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
| `internal/plugins` | the plugins dash ships, one package per plugin ID (G3 moves each integration here): `proxmox`, `hardware`, `kubernetes`, `prometheus`, `alertmanager`, `gatus`, `victorialogs`, `grafana`, `headlamp` and `launcher` so far; `internal/plugins/<id>/model` holds the payload types only that plugin publishes |
| `internal/svc` | the interfaces plugins export to each other and to the core: `Querier`, `EndpointHealth`, `NodeName` (proxmox), `NodeIndex` and `Cluster` (the kubernetes plugin) |
| `internal/pluginconf` | the configuration structs of the plugins, shared by `internal/config` and the plugins (which never import `internal/config`) |
| `internal/secret` | secret values (`[redacted]` when printed), the `env:` and `file:` references, the scrub registry and `K8sRefs`, which hands the kubernetes plugin's `k8s:` references one holder each |
| `internal/auth` | who a request acts as: role and principal |
| `internal/kube` | the kubernetes plugin's client: informers, discovery and the patches |
| `internal/upstream` | VictoriaMetrics, Alertmanager, VictoriaLogs, Gatus, Proxmox clients |
| `internal/topics` | the flux shim and the overview's composer: the builders, health and attention rules of what is not a plugin yet (flux) |
| `internal/hub`, `internal/api` | SSE hub, REST, middleware |
| `internal/obs` | dash's own Prometheus metrics (`server.metrics`), in a private registry |
| `internal/hygiene` | repository-wide guards that belong to no package (`TestNoHomelabLiterals`: no lab identifier in non-test code) |
| `internal/demo` | fake data for `DASH_DEMO=true`; `internal/demo/world` is the fake homelab the plugins' demo twins publish from |
| `web/` | React 19, Vite, Tailwind v4, shadcn/ui and Magic UI |
| `web/src/plugins` | the SPA's plugin modules, registry, slots and generic panels; a module's directory name is its plugin ID |

## Develop

Tools are pinned in `mise.toml` (`mise install`). The homelab checkout must sit next to this one:
dev mode uses its kubeconfig and its 1Password broker (`../homelab/scripts/hl-op`). Copy
`dev/site.env.example` to `dev/site.env` and fill in your lab's URLs; `make dev-api` loads it.

```sh
make dev-api   # Go API on 127.0.0.1:8080 (DASH_DEV=true)
make dev-web   # Vite on http://localhost:5173, proxying /api to the Go API
```

`DASH_DEV=true` signs every request in as `dev` without authentication, so dash refuses to
start unless `server.listen` (`DASH_SERVER_LISTEN`) is a loopback address (`127.0.0.1:8080`,
`[::1]:8080`, `localhost:8080`). Since v0.9.0 it no longer sends the plugins' upstream URLs
through the API server. To reach in-cluster services such as
`http://prometheus.monitoring.svc:9090` from a laptop, set
`DASH_PLUGINS_KUBERNETES_UPSTREAM_VIA_API_SERVER=true` (`plugins.kubernetes.upstreamViaAPIServer`;
`dev/site.env.example` sets it, and the service proxy needs `get` and `create` on `services/proxy`).

Open http://localhost:5173. `make check` runs everything CI runs: it regenerates the generated
files and fails if they differ from what is committed (`web/src/api/types.ts`,
`docs/configuration.md` and `docs/api/openapi.json`), lints the OpenAPI document with vacuum,
then runs `go vet`, `go test`, `tsc`, Biome, the web tests and the production build.

Web tests run on Vitest with jsdom and Testing Library (`cd web && npm test`; `npx vitest` watches).
Tests sit next to the code as `*.test.ts(x)`. `web/src/test/harness.tsx` has the shared helpers:
`installEventSource()` (a fake EventSource you drive with `open()`, `emit(name, data)` and
`fail()`), `stubFetch(routes)` (answers `"GET /api/v1/x"` from a table and records calls),
`renderApp(ui, { path })` (QueryClient, router and tooltips, animations off) and the
`pluginInfo()` / `pluginsEnvelope()` builders. `setup.ts` stubs what jsdom lacks and removes the
fakes after each test, so a test never reaches the network.

Three files are generated; never edit them by hand. `make generate` rewrites all three and you
commit them with the change:

| File | Generated from | Rewrite after changing |
|---|---|---|
| `web/src/api/types.ts` | `internal/model` (tygo) | a struct in `internal/model` |
| `docs/configuration.md` | the config schema (`dash config env --markdown`) | a setting, its `doc` tag or its default |
| `docs/api/openapi.json` | the route table and the types it names (`dash api openapi`) | an `/api/v1` route, a topic envelope, a plugin manifest or an error code |

A changed config schema also needs `go test ./internal/config -run TestConfigSchemaGolden -update`
for `internal/config/testdata/config.schema.json`.

A plugin that owns payload types has its own `model` package (`internal/plugins/<id>/model`: proxmox and hardware have one), its own entry in `tygo.yaml` and a generated `web/src/plugins/modules/<id>/model.gen.ts`, which `make check` also diffs. Such a package is an import leaf: it imports only the standard library (`TestPluginModelPackagesAreImportLeaves`), so a payload that needs a core type such as `Link` or `Health` stays in `internal/model`; `tygo.yaml` has the recipe for the other case.

### Pre-push hook (opt-in)

CI runs only on `main` and tags, so `.githooks/pre-push` is the pre-merge gate. It diffs the
pushed commits against the remote (a new branch against its merge-base with `origin/main`) and
checks only what changed, usually in a few seconds:

- Go files: `go vet` and `go test` on the changed packages (`./...` if `go.mod`/`go.sum` changed
  or a package was deleted). Packages that import a changed package are not re-tested.
- `internal/model`, `tygo.yaml` or `web/src/api/types.ts`: the `make generate` drift check.
- `docs/api/`, `tools/`, `cmd/dash/openapi.go`, `cmd/dash/plugins.go` or the Go files in
  `internal/api`, `internal/plugin`, `internal/model`, `internal/jsonschema` or
  `internal/topics`: `docs/api/openapi.json` must equal `dash api openapi`, and vacuum must lint
  it. `docs/configuration.md` has no step of its own: a Go change in `internal/config` runs its
  currency test.
- `web/`: `tsc --noEmit`, Biome on the changed files (all of `web/` if its config, package or lock
  files changed) and `vitest run` whenever TypeScript or web config changed. Skipped with a hint
  if `web/node_modules` is missing (`npm ci`).
- Anything else (other docs, CI, Dockerfile): nothing; the hook exits at once.

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
