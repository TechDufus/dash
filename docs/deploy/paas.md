# Run dash on a PaaS (Fly.io, Render, Cloud Run, ECS)

The image is the one of [docker.md](docker.md): `ghcr.io/techdufus/dash:<version>`, port `8080`
for the web UI and the API, port `8081` for `/healthz` and `/readyz`. Every platform below runs
it unchanged; what differs is whether it gives dash a disk.

Every platform below pulls the public GHCR image directly.

## Configuration: environment and mounted files

Everything is a `DASH_*` variable ([docs/configuration.md](../configuration.md)), or a YAML file
that `DASH_CONFIG` points to. Use the platform's secret store for credentials and pass them as
variables, or mount them as files and refer to them:

| Need | Variable |
|---|---|
| The URL people open (required) | `DASH_SERVER_PUBLIC_URL=https://dash.example.com` |
| A config file mounted from a secret or config store | `DASH_CONFIG=/etc/dash/config.yaml` |
| A plugin | `DASH_PLUGINS_PROMETHEUS_URL=https://prometheus.example.com` |
| A secret as a file | `plugins.proxmox.tokenSecret: file:/run/secrets/pve` in the file, or `DASH_PLUGINS_PROXMOX_TOKEN_SECRET=…` |
| The key for secrets typed into the UI | `DASH_SECRET_KEY` (32 bytes, base64: `openssl rand -base64 32`) or `DASH_SECRET_KEY_FILE` |
| The administrator's password | `DASH_ADMIN_PASSWORD_HASH` or `DASH_ADMIN_PASSWORD_HASH_FILE` |
| The settings page saves nothing | `DASH_SETTINGS_UI=readonly` |

Make the password hash with the image itself (it asks for the password twice, without echo):

```sh
docker run --rm -it ghcr.io/techdufus/dash:0.15.0 hash-password
```

A value from the environment or the file wins over the settings page, which shows it locked
with its source (`env` and the variable, or `file` and the path).

### The Host check

Port 8080 (the web UI and the API) answers only the host of `DASH_SERVER_PUBLIC_URL`, and the
hosts in `DASH_SERVER_ALLOWED_HOSTS` (a JSON list, `["dash-abc.fly.dev","dash.example.com"]`).
Set the public URL to the address people use; when the platform's own name (`*.fly.dev`,
`*.onrender.com`, `*.run.app`, the load balancer's DNS name) should work too, list it in
`allowedHosts`. Any other host gets the error `unknown_host`. The health port has no Host check,
so probes that can target 8081 work with any name (Fly's top-level `[checks]`, ECS target groups;
Render's and Cloud Run's documented probes can't).

## With a volume, or without

**With a small volume at `/data`** (Fly volumes, Render disks, ECS with EFS) dash behaves as it
does in Docker: the settings page saves, the setup token creates the account, and with
`DASH_SECRET_KEY` sign-ins survive restarts. 256 MiB is plenty. This is the recommended setup
for `auth.mode=local`. The volume must be writable by uid 65532; check it on the first start
(the log says `"persistent":true`).

**Without a volume** `/data` is unwritable (a read-only root filesystem) or thrown away at every
restart (Cloud Run and other scale-to-zero platforms):

- **Sign-ins end at every restart.** The session state lives in the data directory, so without a
  writable one every restart, redeploy or scale-to-zero signs everybody out, **whether or not
  `DASH_SECRET_KEY` is set**. With an unwritable data dir dash logs
  `"sessionsSurviveRestart":false`; on a writable disk that is wiped at restart (Cloud Run) it
  logs `true`, yet sign-ins still end, because `credentials.yaml` and its salt are recreated.
- **`auth.mode=local` needs `DASH_ADMIN_PASSWORD_HASH`**: without a data directory there is no
  account to set up, and dash refuses to start without the hash. With a writable but ephemeral
  disk it would print a new setup token at every start instead; set the hash there too.
- **Settings.** An unwritable data directory makes the settings page read-only by itself (the page
  says "There is no writable data directory (DASH_DATA_DIR) to store settings in.", and
  `GET /api/v1/settings` reports `store.readonly: true`, `reason: data_dir`). dash cannot tell a
  writable disk that vanishes on restart from a real one, so there set
  `DASH_SETTINGS_UI=readonly`, or every save is lost at the next start.
- **The audit log** stays in memory (the settings page shows the recent records) and in the
  container log, where the platform keeps it.

So without a volume prefer **`auth.mode=oidc`** (users sign in at your identity provider again
after a restart, often without typing anything) or **`auth.mode=proxy`** behind the platform's
identity proxy, or give dash the small volume. See [docs/auth.md](../auth.md) for both modes.
In `proxy` mode set `DASH_AUTH_PROXY_TRUSTED_PROXIES` to the CIDRs the platform's proxy connects
from and the header it sets in `DASH_AUTH_PROXY_USER_HEADERS`; dash never believes the header from
any other peer.

CI runs the volume-less case on every push to `main` and on every tag:
`scripts/ci/readonly-smoke.sh` starts the image with `--read-only`, no volume, local mode with a
generated hash, one plugin URL from the environment and one setting from a mounted file, signs in
with the password and checks the read-only store and both sources.

## One replica

Run exactly one instance. dash keeps its live state in memory and its settings, accounts and
audit log in one directory guarded by file locks (one dash per data directory); two instances
without a volume each keep their own state and disagree. Disable autoscaling beyond one, and
prefer a stop-then-start deploy to a rolling one when a volume is attached. Network filesystems
(EFS) work for one instance, but file locks on them are only as good as the filesystem's.

## Platform notes

**Fly.io.** `fly launch --image ghcr.io/techdufus/dash:0.15.0 --internal-port 8080 --no-deploy`,
then in `fly.toml` keep `[http_service]` with `internal_port = 8080` and set
`auto_stop_machines = "off"` (a stopped machine is a restart). Add `[mounts]` with
`source = "dash_data"`, `destination = "/data"`, and a top-level `[checks.readyz]` with
`type = "http"`, `port = 8081`, `path = "/readyz"`, `grace_period = "75s"`. Don't use
`[[http_service.checks]]`: it can only call 8080, which answers the check's private address with
421, and Fly then stops routing to the machine. `fly secrets set DASH_SECRET_KEY=…
DASH_ADMIN_PASSWORD_HASH='…'`; `DASH_SERVER_PUBLIC_URL=https://<app>.fly.dev` under `[env]`. A
volume attaches to one machine: keep one (`fly scale count 1`).

**Render.** A Web Service from the image on a paid instance type (free services can't have
disks), with the environment variable `PORT=8080` (Render routes public traffic to the port in
`PORT`; dash ignores it). Add a persistent disk at `/data`; a disk limits the service to one
instance and turns off zero-downtime deploys. Put the variables under Environment. Render's health
check has a path but no port, and dash serves `/healthz` and `/readyz` only on 8081, so leave the
path empty: Render then checks the port with TCP and restarts an instance that fails for 60
seconds. Image-backed services don't redeploy when a tag moves: deploy by hand.

**Cloud Run.** No volume dash can use (Cloud Run's NFS and Cloud Storage volumes have no file
locking; its file system is in memory): use `oidc` or `proxy`, or local mode with the hash and
the sign-out caveat above.

```sh
gcloud run deploy dash --image ghcr.io/techdufus/dash:0.15.0 --region REGION --allow-unauthenticated \
  --port 8080 --min-instances 1 --max-instances 1 --no-cpu-throttling \
  --set-env-vars DASH_SERVER_PUBLIC_URL=https://…,DASH_SETTINGS_UI=readonly,DASH_CONFIG=/etc/dash/config.yaml \
  --set-secrets DASH_ADMIN_PASSWORD_HASH=dash-admin-hash:1,/etc/dash/config.yaml=dash-config:latest
```

Drop `DASH_CONFIG=…` and `,/etc/dash/config.yaml=dash-config:latest` if you mount no config file.
`--set-secrets` replaces every secret the service had, so the config file mount goes in the same
flag. The service account needs `roles/secretmanager.secretAccessor`. Cloud Run may briefly run a
second instance during deploys. Its probes are documented on the container port (8080), which
serves no health endpoint: keep the default TCP startup probe. List the service's second
`run.app` URL in `DASH_SERVER_ALLOWED_HOSTS`.

**ECS (Fargate).** One task (`desiredCount: 1`, `maximumPercent: 100`,
`minimumHealthyPercent: 0` so the old task stops first) with port mappings 8080 and 8081, plus
`healthCheckGracePeriodSeconds: 90`; an `ip` target group whose health check uses port 8081
`/readyz`, with the task security group open to the load balancer on 8080 and 8081;
`"healthCheck": {"command": ["CMD","/dash","healthcheck"], "startPeriod": 90}` in the container
definition (ECS ignores the image's `HEALTHCHECK`; no shell, so not `CMD-SHELL`); secrets from
Secrets Manager or SSM in the container definition's `secrets`, with a task execution role
allowed `secretsmanager:GetSecretValue` / `ssm:GetParameters`; an EFS access point with POSIX
user 65532:65532 and a root directory created with owner 65532:65532, mode 750, mounted at
`/data` with `transitEncryption: "ENABLED"`, with NFS 2049 open from the task. Without EFS set
`readonlyRootFilesystem: true` and treat it as the volume-less case.
