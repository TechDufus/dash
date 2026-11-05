The kubernetes plugin reads the cluster through informers: nodes, namespaces, workloads, pods,
events, certificates and External Secrets on the Cluster page (a pod's logs come from the
victorialogs plugin and its metrics from the prometheus plugin); the volumes, their fill and
growth with days-to-full, the etcd size and the age of the backup CronJobs you name on the
Storage page; and the apps behind HTTP routes on the Apps page. Without
a kubeconfig or an in-cluster ServiceAccount it is `unavailable`: its pages are hidden and dash
serves everything else. The flux and cnpg plugins read the cluster through it.

## Actions

- `restart` is a rollout restart of a Deployment, StatefulSet or DaemonSet: it sets the pod
  template's `kubectl.kubernetes.io/restartedAt` annotation. `kind` is `Deployment`,
  `StatefulSet` or `DaemonSet`; `namespace` and `name` are DNS names.
- In a namespace that `plugins.kubernetes.conventions.protectedLabel` marks, the caller also
  types the workload's name (the `confirm` field).

## Demo twin

The restart action answers 202 and changes nothing.

## Settings

- A failed discovery or a refused list of a group marked in `plugins.kubernetes.optional.*`
  still adds a note to its topic.
- `plugins.kubernetes.apps.gateways` in the environment:
  `DASH_PLUGINS_KUBERNETES_APPS_GATEWAYS='[{"namespace":"gateways","name":"main"}]'`.
- An app's `/icon` annotation is a dashboard-icons name, a same-origin path or a URL under
  `ui.iconBaseURL`.
- `plugins.kubernetes.apps.ingress` reads `networking.k8s.io/v1` Ingresses, one app per rule host
  (`https://<host>`), with the same annotations and exclusions as the routes.
- A `plugins.kubernetes.backupJobs` row shows the age of the last success of the CronJob, read
  from kube-state-metrics' `kube_cronjob_status_last_successful_time` through the prometheus
  plugin. With a `label` the entry is one row of that name, aged by its freshest CronJob.

## Kubernetes RBAC

The kubernetes plugin's restart and the flux plugin's reconcile are dash's only writes to the
cluster (apart from the requests `upstreamViaAPIServer` proxies, a development setting), the
reconcile through the kubernetes plugin's client. Discovery and the version check use the
default `system:discovery` and `system:public-info-viewer` bindings.

A resource the API server serves but the ServiceAccount may not list (Ingresses, or the
certificates, ExternalSecrets or HTTPRoutes when their permission is missing) is published without
its objects after 60 seconds, and the row is `degraded` with `cannot list: <name> (<category>)`,
where `<name>` is `ingress`, `cert-manager`, `external-secrets` or `gateway-api`, for example
`cannot list: ingress (auth)`; the topic's source note carries the same category.

A mark in `plugins.kubernetes.optional.*` also keeps the row healthy when the discovery of that
group fails (an aggregated API that is down) or its resource cannot be listed; the topic's source
note still says so.

## Secrets from Kubernetes (k8s:)

Any secret field takes `k8s:<namespace>/<name>#<key>`: the value of one key of a Kubernetes
Secret. The kubernetes plugin reads it, so the plugin must run (a cluster, and
`plugins.kubernetes` not disabled). It watches each Secret it is asked for with
`fieldSelector=metadata.name=<name>` and nothing else; it never lists Secrets, keeps no Secret
object and copies only the keys the configuration names.

```yaml
plugins:
  proxmox:
    url: https://pve.example.com:8006
    tokenID: dash@pve!dash
    tokenSecret: k8s:dash/proxmox#tokenSecret
```

`list` and `watch` are needed because a watch with a `metadata.name` selector is a `list` and a
`watch` request; `resourceNames` is what limits them to the named Secrets. With
`rbac.create: false`, create a Role and a RoleBinding for dash's ServiceAccount in each namespace
a reference names:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: dash-secrets, namespace: dash}
rules:
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: [proxmox]   # one entry per Secret a k8s: reference names here
    verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: dash-secrets, namespace: dash}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: dash-secrets}
subjects:
  - {kind: ServiceAccount, name: dash, namespace: dash}
```

A plugin does not start while any `k8s:` reference in its section is unread, an optional
credential included (`auth.bearerToken` and `auth.basic.password` of the Prometheus,
Alertmanager, Gatus and VictoriaLogs sources too): it would otherwise query without the
credential. Its row says `waiting for a Kubernetes Secret (see plugins.<id>.<field>)` (once for
each such field), after `not configured: set plugins.<id>.<setting>` when something else is
missing too. Its state is `unconfigured` when its other settings are complete, otherwise
`disabled` (or `unconfigured` when `enabled: true`). The row never names the namespace, the
Secret, the key or the reason; dash's log does, once per change (`not_found`, `key_missing`,
`forbidden`, `unreachable`, `kubernetes_unavailable`).

The plugin starts by itself when the values arrive, and a rotated Secret applies to the next
request without a restart. A waiting plugin counts as settled for `/readyz`: a Secret that cannot
be read never keeps dash unready.

The value is trimmed of surrounding whitespace, as an `env:` or `file:` value is (a Secret made
with `kubectl create secret --from-file` usually ends in a newline), and a blank value counts as
missing (`key_missing`). It is held in memory only: never written to disk, to the settings store,
to `core.plugins`, the stream or the log.

When the Secret is deleted, or the kubernetes plugin stops (a reload, a lost cluster), the last
value stays in use; only the reason the log shows changes, and the next successful read replaces
it. A reload that adds a `k8s:` reference to a running plugin restarts that plugin in-process: it
waits until the value is read, and dash itself needs no restart.

`dash config validate` checks the syntax of the reference and reads no Secret.
