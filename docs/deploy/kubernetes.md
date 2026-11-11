# Run dash on Kubernetes

Two paths install the same objects:

- **Helm:** the chart in [`charts/dash`](../../charts/dash), configured through its values
  ([`values.yaml`](../../charts/dash/values.yaml) documents each one; `values.schema.json`
  checks them).
- **kustomize:** [`examples/kubernetes`](../../examples/kubernetes), a base rendered from the
  chart with `helm template` and the values in
  [`examples/kubernetes/values.yaml`](../../examples/kubernetes/values.yaml). `make generate`
  writes it and `make check` fails when it drifts from the chart.

The chart needs Kubernetes 1.30 or later (the guardrail is an `admissionregistration.k8s.io/v1`
ValidatingAdmissionPolicy). The pod meets the `restricted` Pod Security Standard (user 65532, no
privilege escalation, every capability dropped, the RuntimeDefault seccomp profile) and also runs
with a read-only root filesystem.

The image is `ghcr.io/techdufus/dash:<version>` and the chart is
`oci://ghcr.io/techdufus/charts/dash`; the chart's version is the dash version it runs. To run
code that is not released yet, build the image from a checkout and push it to a registry your
cluster pulls from, then set `image.repository` and `image.tag` (Helm) or an `images:` entry
(kustomize).

## Before either path: the namespace and the secret key

```sh
kubectl create namespace dash
kubectl label namespace dash pod-security.kubernetes.io/enforce=restricted
openssl rand -base64 32 |
  kubectl -n dash create secret generic dash-secret-key --from-file=secretKey=/dev/stdin
```

The secret key encrypts the secrets typed into the settings page and keeps sign-ins across
restarts. The chart requires it: `secretKey.existingSecret` names the Secret (`secretKey.key`
the key in it, default `secretKey`), mounted into the pod and passed as `DASH_SECRET_KEY_FILE`.
**Keep a copy:** once secrets are stored, dash refuses to start without the key that encrypted
them. The key goes to kubectl on stdin, so it never shows in a process list or the shell's
history; read it back with
`kubectl -n dash get secret dash-secret-key -o jsonpath='{.data.secretKey}' | base64 -d`.

## Helm

```sh
helm install dash oci://ghcr.io/techdufus/charts/dash --version 0.16.1 --namespace dash \
  --set secretKey.existingSecret=dash-secret-key \
  --set config.server.publicURL=https://dash.example.com
kubectl -n dash rollout status deploy/dash
kubectl -n dash logs deploy/dash -c dash | grep -m1 'setup token'
```

Open the `url` of that line within an hour and choose the administrator's password. Reach dash
through an Ingress or an HTTPRoute (below), or `kubectl -n dash port-forward svc/dash 8080`
with a `publicURL` of `http://localhost:8080`. `helm upgrade` with new values restarts the pod
when the config changes (the pod carries a checksum of it).

`config` is dash's config file (`dash config schema` prints its JSON Schema;
[docs/configuration.md](../configuration.md) lists every setting). The chart renders it into a
ConfigMap mounted at `/etc/dash/config.yaml` and passes `DASH_CONFIG`. `env` adds plain
variables (rendering refuses a literal value for a secret setting's variable, and any variable
the chart sets itself). A value from the file or the environment is locked on the settings page.

## kustomize

```sh
kubectl apply -k examples/kubernetes
```

The base deploys into the namespace `dash` with the chart's defaults: settings editable in the
UI on a 256Mi ReadWriteOnce volume, the read-only ClusterRole, no write action. Set your public
URL, and optionally the image (a mirror registry), in an overlay:

```yaml
# kustomization.yaml of your overlay
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/TechDufus/dash//examples/kubernetes?ref=v0.16.1
images:
  - name: ghcr.io/techdufus/dash
    newName: registry.example.com/dash
    newTag: "0.16.1"
patches:
  - target: {kind: Deployment, name: dash}
    patch: |
      - op: add
        path: /spec/template/spec/containers/0/env/-
        value: {name: DASH_SERVER_PUBLIC_URL, value: https://dash.example.com}
```

The rendered objects name the namespace `dash` in two places kustomize does not rewrite: the
ClusterRoleBinding's subject and, with an action on, the guardrail's ServiceAccount. For another
namespace, or for any option below, render your own base:
`helm template dash charts/dash --namespace <ns> -f your-values.yaml > dash.yaml`.

## Secrets

Secrets never go into values or the ConfigMap. A secret field of `config` (`x-dash-secret` in the
schema: `plugins.proxmox.tokenSecret`, `auth.oidc.clientSecret`, every `auth.bearerToken` and
`auth.basic.password`) takes only a reference, and rendering fails on a literal. Three ways in:

1. **`existingSecret`:** dash's own secret key, above.
2. **`secretEnv`:** variables from Secrets, for `env:` references or `DASH_*` variables:

   ```yaml
   secretEnv:
     - name: PROXMOX_TOKEN_SECRET
       secretKeyRef: {name: proxmox, key: tokenSecret}
   config:
     plugins:
       proxmox:
         url: https://pve.example.com:8006
         tokenID: dash@pve!dash
         tokenSecret: env:PROXMOX_TOKEN_SECRET
   ```

   Or mount a Secret with `extraVolumes` and `extraVolumeMounts` and use
   `file:/run/secrets/proxmox/tokenSecret`; a rotated file applies without a restart.
3. **`k8s:` references**, read by the kubernetes plugin through the API, with `rbac.secretRefs`
   naming each Secret dash may read:

   ```yaml
   config:
     plugins:
       alertmanager:
         url: http://alertmanager.monitoring.svc:9093
         auth:
           bearerToken: k8s:monitoring/alertmanager#token
   rbac:
     secretRefs:
       - namespace: monitoring
         names: [alertmanager]
   ```

   Each entry renders a Role in that namespace with `get`, `list` and `watch` on those Secret
   names only (`resourceNames`), and its RoleBinding. dash watches each Secret by name and can
   read nothing else of the namespace. The kubernetes plugin must run for these references.

## Settings, storage and replicas

- **`settings.ui: editable`** (the default): settings can be changed on the settings page and are
  saved on the data volume. **`readonly`**: the page shows every setting and the server refuses
  every write; use it when Git holds the configuration.
- **`persistence.enabled: true`** (the default): a PersistentVolumeClaim for `/data` (UI
  settings, the account, the audit log, backups), `persistence.size` 256Mi.
  `persistence.accessModes` defaults to `[ReadWriteOnce]`; `[ReadWriteOncePod]` keeps any second
  pod in the cluster off the volume where the CSI driver supports it.
- **`persistence.enabled: false`**: an emptyDir, lost with the pod. Set `settings.ui: readonly`
  then: settings saved in the UI and every sign-in would end at the next restart.
- The Deployment always uses **`strategy: Recreate`**, so a rollout never runs two pods on one
  volume.
- **`replicaCount` above 1** renders only with `settings.ui: readonly` and
  `persistence.enabled: false`, and works only with `auth.mode: proxy` (the sidecar, or an
  identity proxy in front): in `local` and `oidc` mode each replica keeps its own session salt
  (and, without `DASH_ADMIN_PASSWORD_HASH`, its own setup token), so a request the Service sends to
  another replica is signed out. Rendering refuses two or more replicas in any other mode. With
  an identity proxy in front instead of the sidecar, `config.auth.proxy.trustedProxies` must
  cover only the proxy's pods, or be paired with `networkPolicy.from` admitting only the proxy:
  dash believes the user name any trusted peer sends, so a pod CIDR without a NetworkPolicy lets
  any pod in it sign in as anyone.

## RBAC modes

| Value | Grants | dash setting it serves |
|---|---|---|
| `rbac.read: true` (default) | ClusterRole: `get`, `list`, `watch` on nodes, namespaces, pods, PVCs, events, Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs, CronJobs, and the cert-manager, External Secrets, Gateway API (HTTPRoutes), CloudNativePG and Flux kinds | the kubernetes, flux and cnpg plugins' views; Ingresses too when `config.plugins.kubernetes.apps.ingress` is true |
| `rbac.actions.restart` | `patch` on Deployments, StatefulSets, DaemonSets; `get` on namespaces | `plugins.kubernetes.actions.enabled` (the restart button) |
| `rbac.actions.fluxReconcile` | `patch` on Kustomizations, HelmReleases, GitRepositories, OCIRepositories, HelmRepositories | `plugins.flux.actions.enabled` (the reconcile button) |
| `rbac.secretRefs` | a Role per namespace: `get`, `list`, `watch` on the named Secrets | `k8s:` references |

With both actions off (the default) no rule has the `patch` verb. An action needs both its RBAC
flag and its `config.plugins.<id>.actions.enabled`. With every flag off the pod mounts no
ServiceAccount token, so the kubernetes plugin cannot connect (`error`, an ERROR at every start;
flux and cnpg wait in `starting`). Set `config.plugins.kubernetes.enabled: false` too: it is then
`disabled`, flux and cnpg `blocked`, their pages hidden. `/readyz` answers either way.
`rbac.create: false` renders no RBAC, for clusters where you grant it yourself; the flags still
decide whether the token is mounted. `serviceAccount.create: false` needs `serviceAccount.name`,
the existing ServiceAccount dash runs as.

## The guardrail

RBAC's `patch` lets dash change anything of a Deployment or a Flux object. The guardrail, a
ValidatingAdmissionPolicy and its binding, holds dash's ServiceAccount to what the actions do:

- on Deployments, StatefulSets and DaemonSets, only the pod template's
  `kubectl.kubernetes.io/restartedAt` annotation, and only with `rbac.actions.restart`;
- on Kustomizations, HelmReleases, GitRepositories, OCIRepositories and HelmRepositories, only
  the `reconcile.fluxcd.io/requestedAt` annotation, and only with `rbac.actions.fluxReconcile`;
- no create or delete of any of them, and no write to a subresource (`scale`, `status`).

Any other change by that ServiceAccount (the spec, a label, another annotation, an owner, a
finalizer) is denied (`failurePolicy: Fail`), whatever RBAC grants it; nobody else is affected.
`guardrails.enabled: auto` (the default) renders it whenever an `rbac.actions` flag is on; `true`
renders it always, `false` never. The policy is cluster-scoped and named after the namespace and
the release.

## The listen address and the oauth2-proxy sidecar

The chart sets `server.listen` explicitly, with `server.healthListen` (`0.0.0.0:8081`, where the
probes look) and `settings.ui`, through the environment, and keeps `dataDir` at the `/data`
volume. Rendering fails when `config` sets any of them, or `env` or `secretEnv` sets a variable
the chart sets (`DASH_CONFIG`, `DASH_SERVER_LISTEN`, `DASH_SERVER_HEALTH_LISTEN`,
`DASH_SETTINGS_UI`, `DASH_SECRET_KEY`, `DASH_DATA_DIR`, and their `_FILE` forms), so each has
one source.

- **Without the sidecar** dash listens on `0.0.0.0:8080` and the Service leads to it. Keep a
  sign-in on (`auth.mode` `local`, the default, or `oidc`). Rendering refuses `auth.mode: none`
  here unless `config.auth.none.allowRemote` is true: dash listens on the pod IP, where `none`
  refuses to start without it, and with it every pod that reaches dash is an administrator.
- **`auth.oauth2ProxySidecar.enabled: true`** adds an oauth2-proxy container: dash listens on the
  pod's loopback (`127.0.0.1:8080`), the proxy on `auth.oauth2ProxySidecar.port` (4180) forwards
  to it, and the Service leads to the proxy. `config.auth.mode` defaults to `proxy` and
  `config.auth.proxy.trustedProxies` to the loopback, so dash believes the proxy's user headers
  and nothing else reaches dash's port. Add your provider, client, allowed users or groups and
  `--email-domain` (oauth2-proxy refuses to start without it; `--email-domain=*` when
  `--github-user` or an org already limits who signs in) to `args`, and the client and cookie
  secrets through `envFrom` (or `valueFrom` in `env`; rendering refuses a literal
  `OAUTH2_PROXY_CLIENT_SECRET` or `OAUTH2_PROXY_COOKIE_SECRET`, and `--client-secret` or
  `--cookie-secret` in `args`):

  ```yaml
  config:
    server:
      publicURL: https://dash.example.com
    auth:
      proxy:
        adminUsers: [alice]   # the name the proxy forwards, exactly
  auth:
    oauth2ProxySidecar:
      enabled: true
      args:
        - --provider=github
        - --github-user=alice
        - --email-domain=*
        - --redirect-url=https://dash.example.com/oauth2/callback
        - --cookie-secure=true
      envFrom:
        - secretRef: {name: oauth2-proxy}   # OAUTH2_PROXY_CLIENT_ID, _CLIENT_SECRET, _COOKIE_SECRET
  ```

  [docs/auth.md](../auth.md) describes the proxy mode. `routes.direct` (below) adds a second host
  for bearer clients; dash then listens on `0.0.0.0:8080`.

## A direct host for bearer clients (`routes.direct`)

With the sidecar every request goes through oauth2-proxy, and a client holding a dash token
(a personal access token, a static token, an OAuth access token), an MCP client or the OAuth
token and discovery endpoints has no proxy session: the proxy turns it away. Its `/api/v1` paths
are the browser's too, so no path rule can let it past. `routes.direct` gives those clients a
second host that skips the proxy:

```yaml
routes:
  direct:
    enabled: true
    host: dash-direct.example.com
```

- **What renders:** dash listens on `0.0.0.0:8080` with a container port named `direct`, and
  `DASH_SERVER_DIRECT_URL` is `https://<host>` (`server.directURL`: the OAuth issuer and the
  origin of the token, revoke and discovery endpoints and of `/mcp`). The Service gets a port
  `direct` (8082) to that container port beside `http` (the proxy). With `ingress.enabled` the
  Ingress gets a rule sending the whole host to `direct`; with `httpRoute.enabled` a second
  HTTPRoute, `<release>-direct`, does the same on the same `parentRefs`. With
  `networkPolicy.enabled` the policy admits `direct` from the same `networkPolicy.from` as
  `http`.
- **DNS and TLS:** the host needs its own DNS record pointing at your ingress controller or
  Gateway, and a certificate: list it in `ingress.tls` (the same Secret can hold both names), or
  on a listener of the Gateway that `httpRoute.parentRefs` names. Bearer clients and the OAuth
  flow use `https://<host>`; browsers keep `config.server.publicURL`, and the consent page stays
  behind the proxy there.
- **Why it is safe:** dash still believes identity headers from the loopback only
  (`config.auth.proxy.trustedProxies` stays `[127.0.0.1/32, ::1/128]`), where only the sidecar
  connects from. A request on the direct host arrives from the ingress controller's pod IP, so
  any `X-Forwarded-User` it carries is refused with 401; dash also refuses identity headers on
  a request addressed to the direct host whatever its peer, the loopback included. That host
  authenticates bearer tokens and nothing else, and the browser pages there sign nobody in.
  Rendering refuses `config.auth.proxy.trustedProxies` and `allowAnyPeer`,
  `config.server.directURL`, and their variables (`DASH_AUTH_PROXY_TRUSTED_PROXIES`,
  `DASH_AUTH_PROXY_ALLOW_ANY_PEER`, `DASH_SERVER_DIRECT_URL`, and their `_FILE` forms) with
  `routes.direct`, and `auth.mode: none` (every client of the host would be an administrator).
- **One replica:** tokens and OAuth grants live in each pod's `credentials.yaml`, so a token
  created through one replica would be unknown to the others. Rendering refuses `routes.direct`
  with `replicaCount` above 1.
- It needs `auth.oauth2ProxySidecar.enabled` (without the sidecar the Service already leads
  straight to dash) and `routes.direct.host`, a DNS name, and `service.port` other than 8082.
  The host must be its own: rendering refuses one equal to an `ingress.hosts` host, an
  `httpRoute.hostnames` entry or the host of `config.server.publicURL` (case, port and a
  trailing dot ignored), since dash tells the two routes apart by the Host alone.
  OAuth itself is off until `config.auth.oauth.enabled` is true.

## Ingress, HTTPRoute and NetworkPolicy

- `ingress.enabled` with `ingress.hosts` (and `className`, `tls`, `annotations`) renders an
  Ingress to the Service, plus the `routes.direct` host's rule.
- `httpRoute.enabled` with `httpRoute.parentRefs` (and `hostnames`) renders a Gateway API
  HTTPRoute to the Service, plus `<release>-direct` with `routes.direct`.
- `networkPolicy.enabled` admits traffic only to the served port (dash's, or the proxy's with
  the sidecar) and to `direct` with `routes.direct`, from `networkPolicy.from` (any source when
  empty), and to the health port 8081 from any source, for the kubelet's probes. `/metrics`, when
  `config.server.metrics` turns it on, is on that port too, so any pod that can reach the pod can
  read it. Egress stays open: dash calls the APIs it shows.

## What rendering refuses

`helm template` and `helm install` fail, naming the value, when:

- `secretKey.existingSecret` is empty;
- a secret field of `config` holds a literal instead of an `env:`, `file:` or `k8s:` reference
  (the chart finds them with dash's config schema, `charts/dash/files/config.schema.json`, which
  `make generate` copies from `dash config schema`), or a string of `config` is a URL with a user
  name or password in it (`user:password@`, `token@`);
- `env` gives a literal `value` for a secret setting's variable (the list is
  `charts/dash/files/secret-env.txt`, from `dash config env`) or any `value` for
  `DASH_SECRET_KEY_PREVIOUS` (take it from a Secret, or mount the old key and set
  `DASH_SECRET_KEY_PREVIOUS_FILE`); the sidecar's `env` gives a literal
  `OAUTH2_PROXY_CLIENT_SECRET` or `OAUTH2_PROXY_COOKIE_SECRET`, or its `args` a `--client-secret`
  or `--cookie-secret`;
- `replicaCount` is above 1 while `settings.ui` is not `readonly`, persistence is on, or the
  auth mode is not `proxy`;
- `auth.mode` is `none` without the sidecar or `config.auth.none.allowRemote` (or a literal
  `DASH_AUTH_NONE_ALLOW_REMOTE=true` in `env`);
- `DASH_AUTH_MODE` comes from `secretEnv` or a `valueFrom`, or `DASH_AUTH_MODE_FILE` is set: the
  checks above could not read the mode;
- `config` sets `server.listen`, `server.healthListen`, `settings.ui` or `dataDir`, or `env` or
  `secretEnv` sets a variable the chart sets;
- `routes.direct.enabled` is true without the sidecar or `routes.direct.host`, with
  `replicaCount` above 1, `auth.mode: none` or `service.port` 8082, with
  `config.server.directURL`, `config.auth.proxy.trustedProxies` or `allowAnyPeer` set (in
  `config`, `env` or `secretEnv`), or with `routes.direct.host` equal to an `ingress.hosts` host,
  an `httpRoute.hostnames` entry or the host of `config.server.publicURL`;
- `serviceAccount.create` is false without `serviceAccount.name`;
- an `rbac.secretRefs` entry has no namespace, no names or a name that is not a non-blank string,
  or a namespace is listed twice;
- a value breaks `values.schema.json`.

## CI

`make check` lints and renders the chart with five value sets (`charts/dash/ci`: the defaults,
read-only with two replicas behind an identity proxy, both actions, the sidecar, the sidecar with
`routes.direct`) and checks the result
(`charts/chart_test.go`), and fails when `examples/kubernetes` drifts from the chart. The `kind`
job validates the rendered objects with kubeconform, then runs `scripts/ci/kind-smoke.sh`: a
throwaway kind cluster with the image built from the checkout, `helm install` of the chart with
both actions and `kubectl apply -k` of `examples/kubernetes`, each in a `restricted` namespace,
each reaching ready and passing `dash healthcheck`. On the Helm install it checks the guardrail
with server-side dry runs as dash's ServiceAccount: the restart patch is admitted; a scale (through
the `scale` subresource, granted by a probe Role), a replicas change, a label, another template
annotation and a delete (also granted by the probe Role) are denied by the policy. It deletes the
cluster, its Docker network and the image on exit, and CI removes them by name in a last step
even when the job is cancelled; run it locally with Docker, kind, kubectl, helm and openssl
(`mise exec -- scripts/ci/kind-smoke.sh`).
