# Upgrading

dash is 0.x: a minor release may change behaviour, and its release notes say how. This page keeps
the changes that still matter when you upgrade from an old release. The sign-in changes of v0.12
(the default `auth.mode` became `local`) are in [auth.md](auth.md).

## No site defaults

Earlier versions defaulted to one homelab's service addresses and names. They are gone. An empty
upstream URL turns that source off: dash makes no request to it and its panels show
"source not configured". If you relied on a default, set it explicitly:

| Removed default | Set |
|---|---|
| `http://vmsingle-vm.monitoring.svc:8428` | `plugins.prometheus.url` |
| `http://vmalertmanager-vm.monitoring.svc:9093` | `plugins.alertmanager.url` |
| `http://vl-server.monitoring.svc:9428` | `plugins.victorialogs.url` |
| `http://gatus.gatus.svc:8080` | `plugins.gatus.url` |
| the protected and tenant namespace labels | `plugins.kubernetes.conventions.protectedLabel`, `.tenantLabel` |
| the Flux suspend-reason annotation | `plugins.flux.suspendReasonAnnotation` |
| `app-` | `plugins.kubernetes.conventions.appsFluxPrefix` |
| the ZFS scrub metric prefix | `plugins.hardware.zfsScrubMetricPrefix` |
| one backup CronJob | `plugins.kubernetes.backupJobs` (`[{namespace: backup, cronjob: nightly-backup}]`) |
| only routes of one gateway were apps | `plugins.kubernetes.apps.gateways` (`[{namespace: gateway, name: main}]`); empty now accepts every HTTPRoute |
| the `dash` namespace was never an app | `plugins.kubernetes.apps.excludeNamespaces` (`[dash]`); empty now skips nothing |
| app icons from a CDN | `ui.iconBaseURL` (`https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png`); empty now shows letter tiles |
| the RustFS buckets and capacity were always read | `plugins.rustfs.enabled` (`true`; the plugin is now off unless set, and its series names are `plugins.rustfs.metrics.*`) |
| one Flux source was always the one that had to be signed and verified | `plugins.flux.verify.sources` (`[{kind: GitRepository, namespace: <ns>, name: <name>}]`); empty now pins none, and any source with `spec.verify` is checked (attention items, the verified tile and the GitOps page) unless `plugins.flux.verify.requireWhenSpecVerify` is `false`, which keeps only the pins in scope |
| Reconcile was always on | `plugins.flux.actions.enabled` (`true`; the action is now off unless set) |
| the Gatus group `infrastructure` was critical | `plugins.gatus.criticalGroups` (`DASH_PLUGINS_GATUS_CRITICAL_GROUPS='["infrastructure"]'`); empty now makes every failing check a warning |
| the API listened on `127.0.0.1:8080` (until v0.14.0) | `server.listen` (`DASH_SERVER_LISTEN=127.0.0.1:8080` behind a sidecar identity proxy); the default is now `0.0.0.0:8080` |

## Removed variable names

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

## Removed API paths

The API moved under `/api/v1` and the plugins' own routes; every old path answers 404:

| Removed | Now |
|---|---|
| `/api/actions/reconcile` | the flux plugin's `reconcile` action, `POST /api/v1/plugins/flux/actions/reconcile` |
| `/api/actions/restart` | the kubernetes plugin's `restart` action, `POST /api/v1/plugins/kubernetes/actions/restart` |
| `/api/pods/{ns}/{name}` | `/api/v1/plugins/kubernetes/pods/{ns}/{name}` |
| `/api/vms/{vmid}` | `/api/v1/plugins/proxmox/vms/{vmid}` |
| `/api/logs`, `/api/logs/tail` | `/api/v1/plugins/victorialogs/logs`, `.../logs/tail` |
| `/api/silences` | the alertmanager plugin's `silence.create` and `silence.expire` actions |
| `/api/me`, `/api/stream`, `/api/series` | `/api/v1/me`, `/api/v1/stream`, `/api/v1/series` |

## Behaviour changes worth knowing

- **`DASH_DEV` no longer sends the plugins' upstream URLs through the API server** (since
  v0.9.0). To reach in-cluster services from a laptop set
  `DASH_PLUGINS_KUBERNETES_UPSTREAM_VIA_API_SERVER=true` (`plugins.kubernetes.upstreamViaAPIServer`).
- **`/readyz` no longer waits for the Kubernetes caches to fill.** List `kubernetes` in
  `server.readyRequires` to hold it until the new pod has data.
- **The Hardware plugin reads the host's node from the running Proxmox plugin.** v0.7.0 used
  `plugins.proxmox.node` as the host's node even without a Proxmox token; set
  `plugins.hardware.hosts[0].nodename` to run Hardware without one.
- **Proxmox clusters (v0.15.0).** The proxmox plugin reads the whole cluster through its one
  `url`: every node, every node's guests and storages, and the tasks of all of them.
  - The topic `proxmox.node` is gone; `proxmox.cluster` carries the cluster's name and quorum,
    every node with its usage and facts, and the totals of the online nodes. Each node has a
    `status`: `online`, `offline`, or `unknown` (a member whose status daemon has not reported for
    a while; no usage, not counted online, no item). `quorumKnown` is false while dash cannot read
    the quorum (the token lacks `Sys.Audit` on `/`, or `/cluster/status` has not answered yet);
    `quorate` means nothing then.
  - VMs and tasks carry `node`. Storage refs carry the node (`proxmox:Storage/<node>/<name>`), and
    a shared storage is listed once as `proxmox:Storage/<name>`; `proxmox.storage` gains its
    totals. With `storage.source: prometheus`, an exporter without `pve_storage_shared` lists a
    shared storage once per node.
  - `core.overview`'s topology has `hosts` (a list, one per node) instead of `host`.
  - New critical items: `pve-node:<node>` for an offline node and `pve-cluster:quorum` for lost
    quorum. A guest of an offline node shows health `unknown` and raises no item of its own.
  - `plugins.proxmox.node` no longer selects what is read, and a cluster of several nodes no
    longer needs it for the plugin to run (node discovery and its "set plugins.proxmox.node" error
    are gone). It now names the node that is the Hardware page's host; a value that is no node of
    the cluster leaves the row degraded.
  - The Hardware page in a cluster of several nodes needs `plugins.proxmox.node` (or
    `plugins.hardware.hosts[0].nodename`): until one is set its row names the nodes and says what
    to set.
  - The cluster's name and quorum come from `/cluster/status`, which needs `Sys.Audit` on `/`
    (`PVEAuditor` on `/` has it). Without it the row is degraded with `cluster status: auth` and
    everything else is read.
  - Rolling back to v0.14.11 needs no settings change, but a cluster of several nodes then needs
    `plugins.proxmox.node` again for the plugin to run.
- **The web UI registers a service worker** (since v0.14.5, which makes dash an installable app).
  Rolling back past it leaves the worker in each browser: an old worker keeps serving its
  precached files until it updates, which an older dash cannot offer. Pages and data still come
  from the server. To remove it: developer tools → *Application* → *Service workers* →
  *Unregister*, then *Storage* → *Clear site data*, which also signs you out
  ([details](app.md#rolling-back)).
- **API tokens and rolling back (v0.14.6).** `credentials.yaml` stays `schemaVersion: 1` until it
  holds a personal access token; from the first token on it is written as `schemaVersion: 2`, which
  v0.14.5 and earlier refuse to start with (rather than drop the tokens). To roll back to v0.14.5
  after creating tokens, stop dash and delete `credentials.yaml`: that ends every session and
  revokes every token, and in the local mode reopens `/setup` unless `DASH_ADMIN_PASSWORD_HASH` is
  set. Remove `auth.tokens` and `server.corsOrigins` from the config file too: v0.14.5 refuses a
  file with keys it does not know. Since v0.14.6 the proxy mode creates the file in a writable data
  dir as well, and a second dash on the same data dir refuses to start.
- **OAuth and rolling back (v0.14.7).** v0.14.7 adds the OAuth authorization server
  (`auth.oauth`, off by default) and `server.directURL`. "Log out everywhere" now revokes your
  connected apps as well, and is offered to every signed-in user; "Sign out all users" is the
  administrators' former "Log out everywhere". OAuth grants live in `credentials.yaml`. To roll
  back to v0.14.6, remove `auth.oauth` and `server.directURL` from the config file (and their
  variables): v0.14.6 refuses a file with keys it does not know. v0.14.6 does not know grants
  either: it ignores them, drops them at its next write of `credentials.yaml`, and a later
  upgrade could find the ones it did not drop. So the file must hold no grants: before rolling
  back, as an administrator, open Settings → Connected apps, choose "All users" and use "Revoke
  every user's connected apps" (it works in every auth mode, the proxy mode too); or stop dash
  and delete `credentials.yaml`, which ends every session and revokes every token and
  grant. With the chart, remove `routes` and `config.auth.oauth` and `config.server.directURL`
  from your values (a `helm rollback` to the previous revision does this).
- **The MCP server and context bundles (v0.14.8).** The new `mcp.enabled` (default `false`)
  serves the read-only [MCP server](mcp.md) at `/mcp`; nothing changes until you switch it on.
  `GET /api/v1/context` and "Copy context for AI" need no setting. The audit log gains three
  fields on MCP records (`argsSha256`, `client`, `durationMs`), which older releases read and
  ignore. To roll back to v0.14.7, remove `mcp.enabled` from the config file first: an older
  release refuses a file with keys it does not know, while a value saved from the settings page or
  set in the environment only logs a warning there.
- **Push and rolling back (v0.14.9).** v0.14.9 adds [push notifications](push.md). Push data (the
  VAPID key and the devices) keeps `credentials.yaml` at `schemaVersion: 2`, which v0.14.8 reads
  and starts with: it keeps the personal access tokens and the OAuth grants, ignores the `push:`
  section it does not know, and drops that section at its next write of the file (a token created
  or used, a grant refreshed, a "Log out everywhere"). Upgrading again then makes a new VAPID key,
  and every device, the dash app's included, must subscribe again in Settings → Devices. To roll
  back to v0.14.8, remove `push:` from the config file first: v0.14.8 refuses a file with keys it
  does not know, while a value saved from the settings page or set in the environment only logs a
  warning there. With the chart, remove `config.push` from your values.
- **Agent actions and rolling back (v0.14.10).** v0.14.10 gives the [MCP server](mcp.md) the
  plugins' action tools, for administrators' `mcp` tokens holding `actions:<plugin>`, and the
  new `plugins.<id>.actions.agents` (`deny`, `approve` or `allow`; default `approve`) on
  kubernetes, flux and alertmanager. With MCP on, an existing `mcp` token of an administrator that
  holds `actions:<plugin>` sees those tools after the upgrade, and its calls create approval
  requests: under the default an agent's action waits for its owner's approval at
  `/approve/<id>`, so nothing runs without a person. `/api/v1/me` gains
  `capabilities.approvals`, and the audit log gains `approval` and `approvedBy` on approval
  records, which older releases read and ignore. Approval requests live in memory only: nothing is
  written to `credentials.yaml` or any other file, and a restart (the rollback included) forgets
  the pending ones. To roll back to v0.14.9, first remove every `plugins.<id>.actions.agents` key
  from the config file **and** unset every `DASH_PLUGINS_<ID>_ACTIONS_AGENTS` variable: v0.14.9
  refuses to start with either, since it knows neither the key nor the variable (an unknown
  `DASH_PLUGINS_*` variable is an error, not a warning). Only a value saved from the settings page
  is safe to leave: v0.14.9 logs a warning and ignores it. With the chart, remove
  `actions.agents` from `config.plugins.*` in your values (the chart writes `config` into the
  config file) and any `DASH_PLUGINS_*_ACTIONS_AGENTS` entry from `env`.
