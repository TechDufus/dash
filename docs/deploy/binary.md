# Run dash as a binary with systemd

Every release from v0.17.0 on carries dash as a single binary for Linux and macOS, on amd64 and
arm64, next to the container image: `dash_<version>_<os>_<arch>.tar.gz` (the binary, `LICENSE`
and `THIRD_PARTY_NOTICES.md`) and `dash_<version>_checksums.txt` on the
[GitHub release](https://github.com/TechDufus/dash/releases). The binary embeds the web UI and
needs nothing else. On a Linux server with systemd, one command installs it as a service.

## The one-line install

```sh
curl -fsSL https://dash.techdufus.com/install.sh | sudo sh
```

The installer is [`site/install.sh`](../../site/install.sh), published with the landing page.
It prints the link to open within an hour to choose the administrator's password (12 to 256
bytes). The token is single-use; a restart prints a new one while no account exists:

```sh
journalctl -u dash -o cat | grep 'setup token' | tail -n 1
```

Options go after `sh -s --`:

```sh
curl -fsSL https://dash.techdufus.com/install.sh | sudo sh -s -- --public-url https://dash.example.com
```

| Option | What it does |
|---|---|
| `--version X.Y.Z` | Install that release instead of the latest (also `DASH_VERSION`). |
| `--public-url URL` | The URL people open dash at, written to `/etc/dash/dash.env` on the first install only. Default: `http://<this machine's name>:8080`. |
| `--no-start` | Install and enable the service without starting it, to edit `/etc/dash/dash.env` first. |
| `--uninstall` | Stop and remove the service and the binary (see [Uninstall](#uninstall)). |
| `--help` | Print all of this. |

It needs root, Linux on amd64 or arm64, systemd 248 or newer, `curl` or `wget`, `tar`, `gzip`,
`sha256sum` and `base64`; on any other system it stops with a pointer to this page. The whole
script runs only once it has downloaded completely, so a broken connection runs nothing.

## What the installer does and creates

1. It resolves the version (the latest release unless you name one), downloads the archive for
   this machine and the release's checksums file from GitHub, and refuses to go on unless the
   archive's SHA-256 matches. A release older than the binaries has no archive, and it says so.
2. It installs `/usr/local/bin/dash`, replacing an older one atomically.
3. It writes `/etc/dash/dash.env` and `/etc/dash/secret_key` only when they are missing.
4. It writes `/etc/systemd/system/dash.service`, enables and starts it (or restarts it on an
   upgrade), waits until `/readyz` answers and prints the address to open.

| Path | What |
|---|---|
| `/usr/local/bin/dash` | The binary. |
| `/etc/dash/dash.env` | Root's, mode 0640: `DASH_SERVER_PUBLIC_URL` and `DASH_DATA_DIR`, read by systemd (`EnvironmentFile=`). Add any other variable of [docs/configuration.md](../configuration.md) here. |
| `/etc/dash/secret_key` | Root's, mode 0600: 32 random bytes, base64, from `/dev/urandom`. The installer makes it; dash never generates a key. |
| `/etc/systemd/system/dash.service` | The unit, rewritten on every run of the installer; put your changes in a drop-in (`systemctl edit dash`). |
| `/var/lib/dash` | The data directory (`DASH_DATA_DIR`): `ui-settings.yaml`, `credentials.yaml`, `audit.jsonl`. systemd keeps it in `/var/lib/private/dash`. |

dash listens on `0.0.0.0:8080` (the web UI and the API) and `0.0.0.0:8081` (`/healthz` and
`/readyz`), and requires a sign-in (`auth.mode=local` by default). Port 8080 answers only the
host of `DASH_SERVER_PUBLIC_URL` (and `server.allowedHosts`).

The unit runs dash as a dynamic user that exists only while dash runs, with no capabilities, a
read-only system (`ProtectSystem=strict`) in which only its state directory is writable, no access
to home directories, devices, kernel settings or other users' processes, IPv4, IPv6 and Unix
sockets only, and the `@system-service` system calls without the privileged ones. The secret key
reaches dash as a credential (`LoadCredential=`): systemd copies it where only the service can
read it and passes its path as `DASH_SECRET_KEY_FILE`, so do not also set `DASH_SECRET_KEY` in
`dash.env`. `systemd-analyze security dash.service` rates the unit 1.1 ("OK"); the full unit is in
[Install by hand](#install-by-hand).

## Install by hand

For people who will not pipe a script into a shell, these are the installer's steps. Pick the
version and the architecture (`amd64` or `arm64`):

```sh
version=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/TechDufus/dash/releases/latest | sed 's#.*/tag/v##')
arch=amd64
base=https://github.com/TechDufus/dash/releases/download/v$version
curl -fsSLO "$base/dash_${version}_linux_${arch}.tar.gz"
curl -fsSLO "$base/dash_${version}_checksums.txt"
sha256sum --check --ignore-missing "dash_${version}_checksums.txt"   # must say OK
tar -xzf "dash_${version}_linux_${arch}.tar.gz" dash
sudo install -m 0755 dash /usr/local/bin/dash

sudo install -d -m 0755 /etc/dash
head -c 32 /dev/urandom | base64 | sudo install -m 0600 /dev/stdin /etc/dash/secret_key
printf 'DASH_SERVER_PUBLIC_URL=%s\nDASH_DATA_DIR=/var/lib/dash\n' "http://$(hostname -f):8080" |
  sudo install -m 0640 /dev/stdin /etc/dash/dash.env
```

Then write `/etc/systemd/system/dash.service`, the unit the installer writes:

```ini
[Unit]
Description=dash, a dashboard for a homelab
Documentation=https://github.com/TechDufus/dash/blob/main/docs/deploy/binary.md
Wants=network-online.target
After=network-online.target

[Service]
Type=exec
ExecStart=/usr/local/bin/dash
EnvironmentFile=/etc/dash/dash.env
LoadCredential=secret_key:/etc/dash/secret_key
Environment=DASH_SECRET_KEY_FILE=%d/secret_key
Restart=on-failure
RestartSec=5s
DynamicUser=yes
StateDirectory=dash
StateDirectoryMode=0700
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
PrivateUsers=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
ProcSubset=pid
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RemoveIPC=yes
KeyringMode=private
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now dash
journalctl -u dash -o cat | grep 'setup token' | tail -n 1
```

On macOS, or without systemd, run the binary yourself with the same variables, for example
`DASH_SERVER_PUBLIC_URL=http://localhost:8080 DASH_DATA_DIR="$HOME/.local/share/dash" DASH_SECRET_KEY_FILE="$HOME/.config/dash/secret_key" ./dash`
(create the key as above). `DASH_DEMO=true ./dash` runs the demo on
http://127.0.0.1:8080 with nothing else.

## Verify the download

The installer checks the archive against the release's checksums file. To check by hand, and to
check where the files were built (each archive and the checksums file carry a GitHub
build-provenance attestation, signed by the release workflow of this repository):

```sh
sha256sum --check --ignore-missing "dash_${version}_checksums.txt"
gh attestation verify "dash_${version}_linux_${arch}.tar.gz" --owner TechDufus
```

The archives are reproducible: `scripts/release/build-binaries.sh <version> <dir>` (or
`make dist`, at the chart's version) on the tag's checkout with the Go of `mise.toml` builds the
same bytes.

## Configure

Edit `/etc/dash/dash.env` and run `sudo systemctl restart dash`, or set what the settings page
offers there. A config file works too: put it at `/etc/dash/config.yaml` (mode 0644: the dynamic
user must read it, so keep secrets out of it) and add `DASH_CONFIG=/etc/dash/config.yaml`.
Secret files reach dash the way the key does, through a drop-in (`sudo systemctl edit dash`):

```ini
[Service]
LoadCredential=kubeconfig:/etc/dash/kubeconfig
Environment=DASH_PLUGINS_KUBERNETES_KUBECONFIG=%d/kubeconfig
```

dash cannot read home directories (`ProtectHome=yes`), so copy a kubeconfig to `/etc/dash`
first. A credential is a copy made at start: after changing the file, restart dash.

## Behind a reverse proxy

Serve other machines through a TLS reverse proxy rather than plain HTTP. Point dash at the address
people type and keep its port on the machine itself, in `/etc/dash/dash.env`:

```sh
DASH_SERVER_PUBLIC_URL=https://dash.example.com
DASH_SERVER_LISTEN=127.0.0.1:8080
```

Then `sudo systemctl restart dash`, and proxy `https://dash.example.com` to
`http://127.0.0.1:8080` with the `Host` header passed on (dash answers only its public URL's
host) and responses unbuffered (the UI is a server-sent event stream). With the proxy on another
machine, keep the listener on `0.0.0.0:8080` and let a firewall admit only the proxy.

## Upgrade and roll back

Run the installer again: it installs the latest release (or `--version X.Y.Z`, which also rolls
back), restarts dash and keeps `/etc/dash` and the data. Read the release notes first: a
breaking change says what to set. By hand, replace `/usr/local/bin/dash` and run
`sudo systemctl restart dash`.

## Back up

The data is in `/var/lib/private/dash`; the key that encrypts the secrets saved from the settings
page is `/etc/dash/secret_key`. **Keep a copy of the key:** once secrets are stored, dash refuses
to start without the key that encrypted them, and a backup of the data is useless without it.
It also seals the key pair dash signs in to [SSH hosts](../plugins/ssh.md) with: lose it, and
every host must be given a new public key.

```sh
sudo systemctl stop dash
sudo tar czf "dash-data-$(date +%F).tar.gz" -C /var/lib/private/dash .
sudo systemctl start dash
```

## Uninstall

```sh
curl -fsSL https://dash.techdufus.com/install.sh | sudo sh -s -- --uninstall
```

It stops and disables the service and removes the unit and the binary. It leaves `/etc/dash`
(the settings and the key) and `/var/lib/private/dash` (the data), so a later install picks up
where this one stopped; `sudo rm -rf /etc/dash /var/lib/private/dash` removes them for good.

## How it is tested

`scripts/ci/install-smoke.sh` runs in CI's smoke job on a GitHub-hosted runner: it builds two
versions for the runner, serves them on loopback, and checks that the installer starts a healthy
service and prints the setup link, that a second run upgrades and restarts dash without touching
the key, that a checksum mismatch and a release without the platform's archive are refused, and
that `--uninstall` leaves no unit. `scripts/ci/install-smoke.sh --docker` runs the same test in a
throwaway Debian container with systemd, never on your machine; it needs Docker, Go and the
checkout.
