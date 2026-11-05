# Configuration reference

dash reads its settings from four layers; each overrides the one before it, setting by setting:
the built-in defaults, the settings saved from the UI, the config file and the environment. It
refuses to start when a required setting is missing or invalid. Nothing about a particular lab is
compiled in: an unset Headlamp, Grafana or Proxmox URL simply drops those links.

## The config file

The file is `DASH_CONFIG`, or `config.yaml` in the data directory (`DASH_DATA_DIR`, default
`/data`) when it exists. Unknown keys are an error that names the full path
(`plugins.proxmox.urll: unknown field`).

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

`dash config schema` prints the JSON Schema of the file; save it and add
`# yaml-language-server: $schema=./config.schema.json` to the file for editor completion.
`dash config validate [file]` checks a file and the environment the way startup does, without
starting dash or touching the data directory, prints every problem with its path and layer, and
exits 1 when there is one. It does not read `env:` and `file:` secret references, which often
point at files that exist only where dash runs; `--resolve-secrets` reads them. A `k8s:` reference
is only checked for its syntax, with or without that flag.

## Environment variables

Every setting has a variable: `DASH_` plus its path in UPPER_SNAKE case
(`plugins.proxmox.tokenSecret` is `DASH_PLUGINS_PROXMOX_TOKEN_SECRET`). Lists and objects are JSON
(`DASH_PLUGINS_HARDWARE_HOSTS='[{"nodename":"pve"}]'`). Append `_FILE` to read the value from a
file (`DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE=/run/secrets/pve`); setting both forms is an error.
A misspelled `DASH_PLUGINS_*` variable stops dash from starting, and any other unknown `DASH_*`
variable is logged. `dash config env` prints every path with its variable name. The variables
that are not settings are under [Process variables](#process-variables).

## Secrets

A secret takes a literal, `env:NAME` (the value of that variable), `file:/path` (the file's
contents, trimmed) or `k8s:<namespace>/<name>#<key>` (the value of that key of a Kubernetes Secret,
read through the kubernetes plugin: it must run and be allowed to read that Secret; see
[Secrets from Kubernetes](plugins/kubernetes.md#secrets-from-kubernetes-k8s)). `..._FILE` is
shorthand for a `file:` reference. A literal in the config file logs a warning, because such files
often live in git. An `env:` or `file:` reference dash cannot read stops it from starting and
names the field, never the value. dash scrubs every secret it has read, in its encoded forms too,
and credential-shaped tokens (`dash_pat_…`, `enc:v1:…`) from its log, from error messages and from
the pod logs the UI shows ([security model](security.md#secrets)).

## Settings saved from the UI

The data directory holds `ui-settings.yaml`, the layer of values saved from the settings page
([settings.md](settings.md)). It is a sparse layer below the file and the environment: whatever
they set wins, and removing it from them shows the saved value again. When the directory is
missing or not writable, or `settings.ui` is `readonly`, the store is read-only: dash logs one
INFO line and runs on the file and the environment alone.

## Reloading

Every 10 seconds dash checks the config file and every file a secret points at (`file:` and
`..._FILE`, symlinks followed, so a Kubernetes ConfigMap or Secret update is seen) and reads the
layers again. An edit that does not validate is logged once as an ERROR and changes nothing; dash
keeps running on the configuration it has. Environment variables only change with a restart.

A change to a `plugins.<id>` section applies live
for prometheus, alertmanager, gatus, victorialogs, proxmox, hardware, kubernetes, flux, cnpg, rustfs, grafana, headlamp and launcher,
except for a setting marked "restart-only": that plugin restarts, and so do the plugins that use or require it
(a prometheus change restarts proxmox, hardware, kubernetes, cnpg and rustfs; a gatus change restarts hardware and
kubernetes; a proxmox change restarts hardware; a kubernetes change restarts proxmox, flux and cnpg; a grafana change
restarts alertmanager). A change to any section that is not a plugin's needs a restart, except the administrator lists (`auth.proxy.adminUsers`, `auth.proxy.adminGroups`, `auth.oidc.adminUsers`, `auth.oidc.adminGroups` and `auth.oidc.adminRoles`), which apply without one, and `mcp.enabled`, which applies to the next request.
A restarted plugin restarts its own dependents in turn, so the list is longer than it reads: a
prometheus change also restarts flux (through kubernetes), a gatus change also proxmox, flux and
cnpg (through kubernetes), and a kubernetes change also hardware (through proxmox). Any other
changed setting is logged as a WARN that it applies after a restart.

A rotated secret file reaches the running credentials without a restart: rotate a Proxmox secret
with `DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE`, for example.

Replace the config file atomically: write a new file next to it and rename it over the old one
(a ConfigMap update does this). A check that finds the file missing or empty where it had
content waits for the next one, so a writer that truncates the file in place or deletes and
recreates it does not put "no settings" into effect for an instant; a file left empty or removed
applies 10 seconds later. A half-written file that still parses cannot be told apart from an edit
and would run until the next check.

## Plugins

A plugin whose `enabled` is unset turns on when it has the settings it needs, except that rustfs is off unless `enabled: true`.
Without them it is `disabled` with a message that names the setting to make:
`not configured: set plugins.<id>.url` for most plugins, `links` or `file` for the launcher, and
`hosts` or `machines` for the hardware plugin. What each plugin shows, needs and may do is in the
[plugin reference](plugins/README.md).
