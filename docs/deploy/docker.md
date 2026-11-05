# Run dash with Docker

One image runs everywhere: `ghcr.io/techdufus/dash:<version>` (distroless, user 65532; for
linux/amd64 and linux/arm64 once published). It listens on `0.0.0.0:8080` for the web UI and the
API and on `0.0.0.0:8081` for `/healthz` and `/readyz`, and its `HEALTHCHECK` runs
`dash healthcheck`.

> Until the image is published on GHCR, build it from a checkout under the same name (for this
> machine's architecture only):
> `docker build --build-arg VERSION=0.14.4 -t ghcr.io/techdufus/dash:0.14.4 .`

## docker run

```sh
docker volume create dash-data
mkdir -p -m 0700 secrets   # only you can reach the key inside
openssl rand -base64 32 > secrets/dash_secret_key
chmod 0644 secrets/dash_secret_key   # the container's user (65532) reads it through the bind mount

docker run -d --name dash --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v dash-data:/data \
  -v "$PWD/secrets/dash_secret_key:/run/secrets/dash_secret_key:ro" \
  -e DASH_SECRET_KEY_FILE=/run/secrets/dash_secret_key \
  -e DASH_SERVER_PUBLIC_URL=http://localhost:8080 \
  ghcr.io/techdufus/dash:0.14.4

docker logs dash 2>&1 | grep -m1 'setup token'
```

Open the `url` of that line within an hour and choose the administrator's password (12 to 256
bytes). The token is single-use; a restart prints a new one while no account exists. The account
lives in the volume, so the line appears only once. `docker ps` shows the container `healthy`
within about 10 seconds.

- **`DASH_SERVER_PUBLIC_URL`** is required: the URL people type, with scheme and host. Port 8080
  (the web UI and the API) answers only that host (and `server.allowedHosts`), so
  `http://127.0.0.1:8080` gets `unknown_host` when the public URL says `localhost`. Behind a TLS
  reverse proxy set it to the proxy's `https://` URL and publish the port to the proxy only. The
  health port 8081 has no Host check.
- **`-p 127.0.0.1:8080:8080`** keeps dash on the machine itself. dash listens on every address in
  the container and requires a sign-in (`auth.mode=local` by default), but serve other machines
  through a TLS proxy, not plain HTTP.
- **`auth.mode=none`** (no sign-in at all) refuses to start on the container's `0.0.0.0:8080`, and
  a loopback listener inside the container cannot be reached through `-p`. On Linux, run it on the
  host's network instead: `--network host -e DASH_AUTH_MODE=none
  -e DASH_SERVER_LISTEN=127.0.0.1:8080 -e DASH_SERVER_HEALTH_LISTEN=127.0.0.1:8081` (no `-p`); only
  the host's own processes reach it, and the settings page still saves. Otherwise add
  `-e DASH_AUTH_MODE=none -e DASH_AUTH_NONE_ALLOW_REMOTE=true`, publish the port to `127.0.0.1`
  only and give dash a Docker network of its own (`docker network create dash-none`, then
  `--network dash-none`): every process on the host and every container on dash's network reaches
  its 8080 whatever `-p` says, and each is an administrator; dash refuses settings changes and
  connection tests there (see [docs/auth.md](../auth.md)).
- **The secret key** encrypts the secrets typed into the settings page and keeps sign-ins across
  restarts (with the volume). Without it dash runs, but secrets can only be given as `env:` or
  `file:` references and every restart ends the sessions. **Keep a copy:** once secrets are
  stored, dash refuses to start without the key that encrypted them.
- **The volume** (`/data`) holds `ui-settings.yaml`, `credentials.yaml` and `audit.jsonl`. Run one
  container per volume. Without a volume `/data` is the container's own layer: settings and the
  account are lost with the container.

Configure plugins on the settings page, or with variables (`-e DASH_PLUGINS_PROMETHEUS_URL=…`)
or a config file (`-v "$PWD/config.yaml:/data/config.yaml:ro"` inside the volume path, or
`-v "$PWD/config.yaml:/etc/dash/config.yaml:ro" -e DASH_CONFIG=/etc/dash/config.yaml`). A value
from the environment or the file wins and is locked on the page.
[docs/configuration.md](../configuration.md) lists every setting and variable. To reach a
cluster from Docker, mount a copy of the kubeconfig the container's user can read (`~/.kube/config`
is usually 0600): `mkdir -p -m 0700 kube && install -m 0644 ~/.kube/config kube/config`, then
`-v "$PWD/kube/config:/kube/config:ro" -e DASH_PLUGINS_KUBERNETES_KUBECONFIG=/kube/config`. Its
`server:` must be reachable from the container.

## Docker Compose

[`examples/compose`](../../examples/compose) runs dash on `http://localhost:8080` with the same
volume and the secret key as a Compose secret, plus Prometheus and Alertmanager under the
`monitoring` profile. Build the image first (above) until it is published.

```sh
cd examples/compose
mkdir -p -m 0700 secrets && openssl rand -base64 32 > secrets/dash_secret_key && chmod 0644 secrets/dash_secret_key   # dash (uid 65532) reads it through a bind mount
docker compose --profile monitoring up -d
docker compose logs dash 2>&1 | grep -m1 'setup token'
```

After setup, open Settings → Prometheus and set the URL to `http://prometheus:9090` (and
Alertmanager to `http://alertmanager:9093`). `secrets/` is git-ignored. Without the profile
(`docker compose up -d`) only dash starts.

`scripts/ci/compose-smoke.sh` runs this example in CI with an image built from the checkout, a
generated key and a free port: it reads the setup token from the logs, sets the password through
`POST /setup`, sets the Prometheus URL through the settings API, waits for the prometheus plugin
to be `healthy` and then requires dash's connection test against the compose Prometheus to pass.
It removes the project, its volume and the image afterwards; run it locally with Docker, `curl`,
`jq` and `openssl`.

## Upgrade and roll back

```sh
docker pull ghcr.io/techdufus/dash:<new>
docker rm -f dash
docker run -d --name dash … ghcr.io/techdufus/dash:<new>   # the same flags as above
```

With Compose, change the tag in `compose.yaml` and run `docker compose up -d`. Read the release
notes first: a breaking change says what to set. To roll back, run the previous tag on the same
volume; the volume is read by both. Rolling back to v0.13.x also needs
`-e DASH_SERVER_LISTEN=0.0.0.0:8080` (in Compose, `DASH_SERVER_LISTEN: 0.0.0.0:8080`): before
v0.14.0 dash listened on 127.0.0.1:8080, which the published port can't reach, while the health
check still says `healthy`.

## Back up

Stop dash so nothing writes, copy the volume, start it again:

```sh
docker stop dash
docker run --rm -v dash-data:/data:ro -v "$PWD:/backup" busybox:1.37 \
  tar czf /backup/dash-data-$(date +%F).tar.gz -C /data .
docker start dash
```

Restore into an empty volume with `tar xzf … -C /data` the same way and `chown -R 65532:65532
/data` inside that helper container. Back up `secrets/dash_secret_key` separately: the archive's stored
secrets are useless without it.
