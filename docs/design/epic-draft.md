# Epic draft: plugin architecture, layered config and open-source readiness

> **Draft. Not filed.** These issues get filed only after the design in [`plugins-and-config.md`](plugins-and-config.md) is approved and the open decisions in its section J are answered.
>
> **Filing order:** the epic first, then the seven group issues (linked as sub-issues of the epic), then the leaves (linked as sub-issues of their group). When filing, replace every `P<phase>.<n>` reference with the real issue number.
>
> **Conventions:**
> - Every leaf is one PR, and `make check` passes on every PR.
> - `DASH_DEMO=true` must keep rendering every page that still exists after the PR.
> - `file:line` references are to commit `e2b7f93` (branch `claude/project-thread-8gve8b`), which P0.1 lands on `main`.
>
> **Suggested labels:** `epic`, `group`, `phase/0`..`phase/6`, `area/config`, `area/plugins`, `area/web`, `area/auth`, `area/deploy`, `area/ci`, `breaking`.

---

## Epic

### feat: plugin architecture, layered config and open-source readiness

**Body**

dash is currently wired to one homelab. Its upstream defaults are VictoriaMetrics-operator `.svc` names. It contains literals such as `flux-system`, `mac-mini`, `rpi-control`, Talos VMIDs and `gateway/home`. It refuses to start without a Kubernetes cluster, trusts identity headers from oauth2-proxy, and releases from a self-hosted runner to a private registry.

This epic turns dash into a modular, self-hostable app:

- integrations become plugins with a schema, a status, topics and actions;
- configuration is layered: defaults < UI settings < config file < env, with locked fields;
- secrets are handled safely (refs, encryption at rest, never sent to the browser);
- auth is part of dash (local, proxy, OIDC);
- dash ships as one multi-arch image for Docker, Compose, PaaS and Kubernetes (Helm);
- the repo meets open-source prerequisites, including GitHub-hosted CI and GHCR releases.

Design: `docs/design/plugins-and-config.md`.

**Groups**

- [ ] G0: docs(adr): phase 0, decisions and ADR
- [ ] G1: feat(config): phase 1, config core and secrets
- [ ] G2: feat(plugin): phase 2, plugin framework and SPA registry
- [ ] G3: feat(plugins): phase 3, migrate integrations to plugins
- [ ] G4: feat(auth): phase 4, settings UI and auth
- [ ] G5: build(deploy): phase 5, packaging and deployment targets
- [ ] G6: chore(oss): phase 6, open-source readiness and public CI

**Acceptance criteria**

- `docker run -p 127.0.0.1:8080:8080 -v dash-data:/data ghcr.io/techdufus/dash:<v>` starts with no cluster and no env other than the public URL. An admin can set up an account, enable Prometheus and Proxmox in Settings, and see their pages.
- The same image runs in Kubernetes from the Helm chart, configured only through a ConfigMap and Secrets, with the settings UI read-only.
- The homelab runs the released GHCR image in `proxy` auth mode with the same features as today.
- No real-lab identifier remains in tracked files; the denylist test enforces this.
- CI runs on GitHub-hosted runners; no workflow targets self-hosted labels.
- The repository is public, with a licence, SECURITY.md and CONTRIBUTING.md.

**Dependencies:** none (top level).

---

## G0: docs(adr): phase 0, decisions and ADR

**Body:** land the configurable-settings branch and record the decisions this epic depends on.

- [ ] P0.1 chore: land claude/project-thread-8gve8b (e2b7f93) on main
- [ ] P0.2 docs(adr): record plugin, config, auth and release decisions

**Acceptance criteria:** both leaves closed; every item in design section J is either decided in the ADR or explicitly deferred with an owner.

**Dependencies:** this design PR merged.

### P0.1 chore: land claude/project-thread-8gve8b (e2b7f93) on main

**Body:** review and merge the three commits on `claude/project-thread-8gve8b`:

- `397f4c6` demo mode
- `ba73150` README
- `e2b7f93` configurable settings

Everything below assumes the `Conventions` struct, `GET /api/config`, `DASH_LAUNCHER_FILE` and `dev/site.env.example` exist on `main`.

**Acceptance criteria**
- `main` contains `e2b7f93` (or its squash), and CI is green.
- The homelab deployment runs the resulting image with unchanged behaviour. Only the homelab knows its env, so this is verified by the owner.

**Dependencies:** none.

### P0.2 docs(adr): record plugin, config, auth and release decisions

**Body:** write `docs/adr/0001-plugins-config-auth.md` covering design section J. At minimum:

- licence
- plugin boundary
- settings store (already decided in design J.3: no database, one YAML file `ui-settings.yaml`; SQLite only on the named triggers, in a separate `dash.db`; no bbolt; the ADR records it)
- precedence and locked fields
- legacy env aliases
- default auth mode
- Helm vs manifests
- git history
- homelab image source
- plugin naming
- actions default
- roles
- multi-instance

**Acceptance criteria**
- The ADR exists, with each decision, the alternatives considered, and the consequences.
- The design doc's section J links to it.

**Dependencies:** P0.1 (so the ADR lands on the same base).

---

## G1: feat(config): phase 1, config core and secrets

**Body:** replace the flat env reader (`internal/config/config.go:80-124`) with a typed, layered config tree that records provenance. Add the secret primitives, the settings store and the audit log. No plugin framework yet: the existing `Config` fields are populated from the new tree, so the rest of the code keeps working.

- [ ] P1.1 feat(config): typed config tree with YAML file loader
- [ ] P1.2 feat(config): env layer with generated DASH_* names and provenance
- [ ] P1.3 feat(config)!: drop homelab defaults for upstream URLs and conventions
- [ ] P1.4 feat(secrets): secret value type, env and file references, redaction
- [ ] P1.5 feat(secrets): encrypt UI-entered secrets at rest with key rotation
- [ ] P1.6 feat(settings): data directory and atomic YAML settings store
- [ ] P1.7 feat(config): hot reload of the config file and referenced secret files
- [ ] P1.8 feat(audit): persistent audit log shared by actions and settings

**Acceptance criteria:** all leaves closed. `dash` starts from a config file alone, from env alone, or from both, and the effective value of every field reports its source.

**Dependencies:** G0.

### P1.1 feat(config): typed config tree with YAML file loader

**Body:** introduce `server`, `auth`, `settings`, `ui` and `plugins` sections (design B.4). Load YAML from `DASH_CONFIG`, defaulting to `/data/config.yaml` if present, with strict unknown-key errors (as `LoadLauncher` does at `config.go:166`). For now, map the tree onto today's `config.Config` fields so callers are untouched.

**Acceptance criteria**
- A table-driven test loads a sample file and gets the expected `config.Config`.
- An unknown key fails with its path, e.g. `plugins.proxmox.urll: unknown field`.
- With no file present, behaviour equals today's.

**Dependencies:** P0.1.

### P1.2 feat(config): env layer with generated DASH_* names and provenance

**Body:** implement the env layer with these rules:

- Env overrides the file.
- Names are generated from the config schema: camelCase becomes UPPER_SNAKE (design B.3).
- `DASH_*_FILE` suffix support, resolved in the Go binary (the image is distroless, so there is no shell entrypoint). Setting both `DASH_X` and `DASH_X_FILE` is a startup error.
- A legacy alias table for today's variables (design B.5), with one deprecation warning per variable. Whether the aliases ship at all is decided in P0.2.

Every resolved field carries `source` (`default|ui|file|env`) and an origin (env var name or file path).

**Acceptance criteria**
- For every row of design B.5, a test sets the legacy variable and asserts the new path and value.
- A collision test fails if two schema paths generate the same env name.
- An unknown `DASH_PLUGINS_*` variable fails startup; other unknown `DASH_*` variables warn, except `DASH_API`.
- Setting both `DASH_X` and `DASH_X_FILE` fails startup, naming both variables.
- `dash config env` prints the generated name table.

**Dependencies:** P1.1.

### P1.3 feat(config)!: drop homelab defaults for upstream URLs and conventions

**Body:** remove the defaults at `config.go:100-103` (`.svc` URLs) and `config.go:114-120` (`homelab.io/*`, `app-`, `homelab_zfs_scrub`, `rustfs`/`nas-backup`). An empty value means the source or feature is off: the existing `ErrDisabled` path and empty panels. Validation of the ZFS prefix (`config.go:153-155`) applies only when it is set. Update the README configuration tables.

**Acceptance criteria**
- With no env, no Prometheus, Alertmanager, VictoriaLogs or Gatus request is made, and the source chips read "source not configured".
- Restart typed-confirmation is skipped when no protected label is configured.
- Release notes are marked breaking.

**Dependencies:** P1.2. **External:** the homelab manifest sets these values explicitly *before* this ships.

### P1.4 feat(secrets): secret value type, env and file references, redaction

**Body:** add an `internal/secret` package:

- `Value` with `String/GoString` returning `[redacted]`, `LogValue`, and metadata-only `MarshalJSON`.
- Resolvers for literal, `env:NAME`, `file:/path` and `<FIELD>_FILE`.
- A redacting `slog.Handler`.
- A scrubber applied to `SourceStatus.Error` (`internal/topics/manager.go:189-199`) and to API error messages built from upstream errors.

Move `ProxmoxToken` onto `secret.Value`.

**Acceptance criteria**
- `fmt`, `slog` and `json.Marshal` of a config never contain a secret (test with a canary value).
- An upstream error that contains the token is published with `[redacted]`.
- A missing `file:` ref fails startup with the field path, never the value.

**Dependencies:** P1.2.

### P1.5 feat(secrets): encrypt UI-entered secrets at rest with key rotation

**Body:** XChaCha20-Poly1305 with the field path as AAD, in the format `enc:v1:<kid>:...` (design C.4).

- Key sources: `DASH_SECRET_KEY` or `DASH_SECRET_KEY_FILE` only. There is no default or auto-generated key. Without a key, UI-entered secret fields are disabled ("set via env/file only") and the rest of dash runs, including env, file and k8s refs.
- `DASH_SECRET_KEY_PREVIOUS` triggers a re-encrypt on startup.

**Acceptance criteria**
- Round-trip tests.
- A ciphertext moved to another field fails to decrypt.
- Rotation rewrites every value and audits `secrets.rekey`.
- With no key, storing a secret returns 409 naming the missing key, and nothing else is affected.
- A stored `enc:v1` value with a missing or wrong key fails startup, listing the field paths. The value is never dropped, blanked or regenerated.

**Dependencies:** P1.4, P1.6.

### P1.6 feat(settings): data directory and atomic YAML settings store

**Body:** add `DASH_DATA_DIR` (default `/data`) and `settings.Store` backed by `$DASH_DATA_DIR/ui-settings.yaml` (not the operator's `config.yaml`):

- a top-level `schemaVersion`, with ordered migrations run on load;
- atomic write: temp file in the same directory, fsync of the file, rename, then fsync of the directory;
- the last 5 backups kept as `ui-settings.yaml.bak.1`..`.bak.5` (`.bak.1` newest), rotated on each write;
- a revision counter used as the ETag;
- `flock` on a separate, never-replaced `.lock` to refuse a second instance (a lock on the store file would follow the old inode after the rename);
- sparse store: it holds only the keys an admin explicitly saved, never a snapshot of the effective config. A form save persists only changed, unlocked fields, and "reset to default" deletes the key;
- read-time merge: the effective config is resolved from defaults, the UI store, the file and env on every load, and file and env values are never copied into the store;
- read-only mode when the directory is unwritable or `settings.ui: readonly` is set.

The UI layer sits below file and env (design B.2).

**Acceptance criteria**
- Concurrent writers: the second process exits with a clear message.
- A write interrupted between the temp write and the rename leaves the old file intact (test via a fault-injected filesystem helper).
- A read-only data dir, or `settings.ui: readonly`, yields `readonly=true`, and writes return 409.
- A file at an older `schemaVersion` is migrated on load.
- The sixth write drops the oldest backup.
- Saving one field writes only that key. After a UI save, a later env or file change still takes effect (no snapshot).
- Removing a file value reveals the stored UI value on the next reload.

**Dependencies:** P1.1.

### P1.7 feat(config): hot reload of the config file and referenced secret files

**Body:** poll the config file and every `file:` ref every 10s (mtime, size, SHA-256). On change, validate the whole tree:

- invalid: keep the running config and surface the error;
- valid: emit a change event per affected section.

Env changes still need a restart. Fields marked `restart` only raise a "restart required" flag.

**Acceptance criteria**
- Replacing the file through a symlink swap (ConfigMap style) is detected.
- An invalid edit does not change the running config and is logged once.
- `/readyz` is unaffected throughout.

**Dependencies:** P1.2, P1.4.

### P1.8 feat(audit): persistent audit log shared by actions and settings

**Body:** move `audit()` (`internal/api/actions.go:30-38`) into `internal/audit`. Write both the slog record and `$DASH_DATA_DIR/audit.jsonl`, rotated at 10 MiB with 5 files kept. A record holds user, action, target, the field paths changed, the source and the result, and never values.

**Acceptance criteria**
- Restart, reconcile and silence actions produce identical slog output to today, plus a JSONL line.
- Rotation is tested.
- No-op when the data dir is unwritable (slog only).

**Dependencies:** P1.6.

---

## G2: feat(plugin): phase 2, plugin framework and SPA registry

**Body:** add the plugin contract (design A) and make the SPA render from a registry. Existing integrations keep running inside `topics.Manager`, which consumes the registry, until G3 moves them.

- [ ] P2.1 feat(plugin): plugin contract, registry and lifecycle supervisor
- [ ] P2.2 feat(plugin): JSON Schema from config structs and dash config commands
- [ ] P2.3 feat(api): /api/plugins, plugin-scoped routes and action framework
- [ ] P2.4 feat(hub)!: namespaced topics and core.plugins status topic
- [ ] P2.5 feat(core): compose the overview from plugin contributions
- [ ] P2.6 feat(api)!: readiness independent of plugin health
- [ ] P2.7 feat(web): plugin module registry for routes, nav and palette
- [ ] P2.8 feat(web): panel slots for composite pages and generic panels
- [ ] P2.9 build(web): generate TS types per plugin model package

**Acceptance criteria:** all leaves closed. A test plugin registered in a test build publishes a topic, shows up in `/api/plugins` and the nav, and disappears when disabled, all without a reload.

**Dependencies:** G1.

### P2.1 feat(plugin): plugin contract, registry and lifecycle supervisor

**Body:** add `internal/plugin` with `ID`, `Manifest`, `Plugin`, the optional interfaces and `Host` (design A.2, A.3). The explicit list lives in `cmd/dash/plugins.go`, along with:

- instance config keyed by instance name, with a `type` default;
- dependency ordering by `Requires`/`Uses`;
- panic recovery;
- the states `disabled|unavailable|unconfigured|blocked|starting|healthy|degraded|error` (design A.6);
- reconfigure as Stop then Start on a config change event from P1.7.

**Acceptance criteria** (unit tests with fake plugins)
- Dependency order is respected; a missing hard dependency gives `blocked` with a message.
- A panic in `Start` or a poller gives `error`, and the process survives.
- Reconfigure stops the old instance within 10s.
- A second instance of the same type is rejected in v1.

**Dependencies:** P1.2, P1.7.

### P2.2 feat(plugin): JSON Schema from config structs and dash config commands

**Body:** reflect JSON Schema 2020-12 from config structs:

- `dash` tags for `required`, `secret` (emitting `writeOnly`), `restart` and `format`;
- `doc` tags become descriptions.

Validation runs schema first, then `Validator`. Add the CLI subcommands `dash config schema`, `dash config env` and `dash config validate [file]`. Choose the reflector library (in-house or `invopop/jsonschema`) in the PR.

**Acceptance criteria**
- Golden-file schema tests.
- `dash config validate` exits non-zero with path-qualified errors.
- `docs/configuration.md` is generated from `dash config env` and checked for drift in `make check`.

**Dependencies:** P2.1.

### P2.3 feat(api): /api/plugins, plugin-scoped routes and action framework

**Body:**

- `GET /api/plugins` returns `{id, name, version, state, message, topics, actions, nav, configurable}`.
- `Router` plugins mount GET routes under `/api/plugins/{id}/`.
- `POST /api/plugins/{id}/actions/{name}` runs auth, then `MinRole`, then the existing CSRF check (`internal/api/middleware.go:18-29`), then the body cap and input schema validation, then `Act`, then audit.
- Actions are mounted only when `actions.enabled` is true.
- Typed-name confirmation is enforced server-side.

**Acceptance criteria**
- Tests for a fake action: 403 without the role; 403 on CSRF failure; 400 on invalid input; 404 when actions are disabled; an audit line on every outcome.
- `/api/plugins` lists the states correctly.

**Dependencies:** P2.1, P1.8.

### P2.4 feat(hub)!: namespaced topics and core.plugins status topic

**Body:**

- `Host.Publish` namespaces topics as `<instance>.<topic>`, and `hub.Subscribe` takes its order from the registry (replacing the fixed list at `internal/hub/hub.go:113`).
- Add `core.plugins`, pushed on every state change.
- Disabling a plugin removes its latest events.
- Legacy topics keep their names until their plugin migrates in G3.

**Acceptance criteria**
- An SSE test sees `core.plugins` on connect.
- After disabling, a new subscriber gets no events for that plugin.
- Hash-dedup behaviour is unchanged (`hub.go:48`).

**Dependencies:** P2.1.

### P2.5 feat(core): compose the overview from plugin contributions

**Body:** replace the all-topics gate (`internal/topics/manager.go:357-365`) with composition:

- KPIs from `KPISource`, attention items from `AttentionSource` (each item carries `plugin`), and topology fragments.
- Publish `core.overview` whenever any contribution changes.
- Legacy builders are adapted as contributors until G3.

**Acceptance criteria**
- With only one contributor enabled, the overview publishes within one rebuild tick.
- Attention ordering is unchanged (existing `attention_test.go` passes after the rename).

**Dependencies:** P2.4.

### P2.6 feat(api)!: readiness independent of plugin health

**Body:**

- `/readyz` returns ok once config is loaded, HTTP is serving, and every enabled plugin has returned from `Start`.
- `/readyz?verbose=1` lists plugin states.
- Optional `server.readyRequires` plus `server.readyTimeout` restores waiting on specific plugins.

**Acceptance criteria**
- Ready with a plugin in `error`.
- With `readyRequires: [kubernetes]` and a never-syncing fake, not ready until the timeout, then ready with a warning.

**Dependencies:** P2.1.

### P2.7 feat(web): plugin module registry for routes, nav and palette

**Body:** add `web/src/plugins/registry.ts` (explicit list) and `PluginModule` (design A.9). The registry replaces:

- the hard-coded routes (`web/src/App.tsx:35-64`);
- `NAV` (`web/src/shell/nav.ts:21-31`);
- the palette "Pages" group;
- the fixed `TOPICS` listeners (`web/src/api/stream.ts:49-60`).

Visibility follows `/api/plugins` and `core.plugins`. A disabled plugin's route renders a "plugin disabled" page. Pages are lazy-loaded per module.

**Acceptance criteria**
- With a plugin disabled, its nav item, route and palette entries disappear live.
- A deep link to it shows the disabled page.
- `npm run check` and the build pass.
- Bundle split: each module's page is its own chunk.

**Dependencies:** P2.3, P2.4.

### P2.8 feat(web): panel slots for composite pages and generic panels

**Body:**

- Overview (KPIs, side panels), Storage and Apps render slot contributions in registry order.
- Plugins without a module can declare `UI.Generic` views (table, stat or list) rendered with the existing `Panel` and table components.
- The closed unions widen: `LinkKind` (`model.go:28-37`, `web/src/components/links.tsx:6-12`) becomes a provider ID or `app`/`external`.

**Acceptance criteria**
- The Storage page renders only the slots of enabled plugins.
- A generic test plugin renders a table from its topic.
- Unknown link kinds fall back to a generic icon.

**Dependencies:** P2.7.

### P2.9 build(web): generate TS types per plugin model package

**Body:** extend `tygo.yaml` to one entry per plugin `model` package. Make `TopicData` (`web/src/api/topics.ts:16-26`) registry-driven while keeping a compile-time check per module.

**Acceptance criteria**
- `make generate` is deterministic.
- `make check` fails if the generated types drift.
- Adding a topic to a plugin model without regenerating fails the check.

**Dependencies:** P2.1.

---

## G3: feat(plugins): phase 3, migrate integrations to plugins

**Body:** move every integration into a plugin, in the order of design F.0. Each leaf:

- deletes the migrated code from `topics.Manager`, `internal/api` and `internal/links`;
- renames its topic to `<plugin>.<topic>` in Go and TS together;
- adds the plugin's `Demo()` fixtures;
- updates the README configuration table.

No aliases survive the phase.

- [ ] P3.1 feat(plugins): prometheus plugin with Querier service and series registry
- [ ] P3.2 feat(plugins): link providers for grafana, headlamp and launcher
- [ ] P3.3 feat(plugins): alertmanager plugin with silence actions
- [ ] P3.4 feat(plugins): gatus plugin with configurable critical groups
- [ ] P3.5 feat(plugins): victorialogs plugin with configurable field mapping
- [ ] P3.6 feat(plugins): proxmox plugin with TLS options and critical VMs
- [ ] P3.7 feat(plugins): hardware plugin for host metrics and machines
- [ ] P3.8 feat(plugins): kubernetes plugin with optional cluster and discovery-gated informers
- [ ] P3.9 feat(plugins): configurable apps discovery in the kubernetes plugin
- [ ] P3.10 feat(plugins): k8s secret references via the kubernetes plugin
- [ ] P3.11 feat(plugins): flux plugin with spec.verify-based source checks
- [ ] P3.12 feat(plugins): cnpg and rustfs storage plugins and a composite Storage page
- [ ] P3.13 refactor!: remove the legacy topics manager, routes and model fields

**Acceptance criteria**
- All leaves closed.
- `internal/topics` no longer exists, or holds only core composition.
- `rg -n 'flux-system|mac-mini|rpi-control|gateway/home|2020 && vmid' internal web/src` finds only test fixtures that deliberately use neutral names, or nothing.
- dash starts without a cluster.

**Dependencies:** G2.

### P3.1 feat(plugins): prometheus plugin with Querier service and series registry

**Body:**

- Wrap `internal/upstream/prom.go` as the `prometheus` plugin. Config: `url`, `timeout`, `auth.basic`/`auth.bearerToken` (secret), `headers`, `tls`.
- Expose the `Querier` service.
- `/api/series` dispatches to the `SeriesSpec`s registered by plugins; the existing names are registered by the legacy code until it migrates.
- The legacy `DASH_VM_URL` maps here.

**Acceptance criteria**
- Series API tests pass unchanged.
- Basic and bearer auth are sent as headers.
- A custom CA file is honoured.
- Disabled plugin: series return 404 `unknown series` and legacy pollers report "source not configured".

**Dependencies:** P2.3.

### P3.2 feat(plugins): link providers for grafana, headlamp and launcher

**Body:**

- Replace `links.Builder` (`internal/links/links.go`) with a `links.Registry`. Providers: `grafana` (`url`), `headlamp` (`url`, `cluster`) and `proxmox` (registered later in P3.6; until then the builder's Proxmox methods stay).
- The `launcher` plugin takes `file` or inline `links`, with the validation from `config.go:170-174`.
- The launcher's links travel in the `/api/plugins` payload, and `GET /api/config` is removed.

**Acceptance criteria**
- The `links_test.go` cases pass against the providers.
- The Apps and Overview launchers render the inline and file lists.
- No links when a provider is disabled.

**Dependencies:** P2.3.

### P3.3 feat(plugins): alertmanager plugin with silence actions

**Body:** move the alerts poller (`internal/topics/feeds.go:21-44`), the attention rules (`internal/topics/attention.go:111-124`) and the silence handlers (`internal/api/silences.go`) into `alertmanager`.

- Config: `url`, `auth`, `tls`, `ignoreAlerts`, `severityMap`, `silenceDurations`, `actions.enabled`.
- Topic: `alertmanager.alerts`.
- Actions: `silence.create`, `silence.expire`.
- The SPA's Alerts page moves into a module.

**Acceptance criteria**
- `silences_test.go` cases pass through the action framework.
- Actions are hidden and return 404 when disabled.
- The rail badge reads the alertmanager KPI.

**Dependencies:** P3.1, P3.2, P2.5.

### P3.4 feat(plugins): gatus plugin with configurable critical groups

**Body:** move `internal/upstream/gatus.go`, `feeds.go:46-73` and the status attention rules into `gatus`.

- Config: `url`, `auth`, `tls`, `criticalGroups` (replaces `"infrastructure"` at `internal/topics/health.go:105` and the ordering at `web/src/pages/status.tsx:31-35`), `pageSize`.
- Service: `EndpointHealth`.

**Acceptance criteria**
- With `criticalGroups: []`, every failing check warns.
- With `[infrastructure]`, behaviour matches today's.
- Apps health dots still work through the service.

**Dependencies:** P2.5.

### P3.5 feat(plugins): victorialogs plugin with configurable field mapping

**Body:** move `internal/upstream/vlogs.go` and the `/api/logs` and `/api/logs/tail` handlers under `/api/plugins/victorialogs/`.

- Config: `url`, `auth`, `tls`, `fields` (defaults `kubernetes.pod_namespace`, `kubernetes.pod_name`, `kubernetes.container_name`; today hard-coded at `vlogs.go:60-67`), `limit`.
- Service: `Logs`.

**Acceptance criteria**
- `vlogs_test.go` passes with the default mapping.
- A custom mapping changes the query.
- The pod sheet's log tab hides when the plugin is disabled.

**Dependencies:** P2.3.

### P3.6 feat(plugins): proxmox plugin with TLS options and critical VMs

**Body:** move `internal/upstream/proxmox.go`, `internal/topics/vms.go` (minus the machines), the Proxmox facts from `host.go:22-30`, the VM attention rules, and the console, shell and UI links into `proxmox`.

- Config: `url`, `node`, `tokenID`, `tokenSecret` (secret), `tls {caFile, insecureSkipVerify}`, `criticalVMs {ids, tags}` (replaces `IsTalosVM` at `health.go:75-76`), `storage.source`.
- Topics: `proxmox.vms`, `proxmox.node`, `proxmox.storage`.
- Route: `GET vms/{vmid}`.
- Series: `vm.*`.
- Uses kubernetes (VM-to-node) and prometheus.

**Acceptance criteria**
- `clients_test.go` Proxmox cases pass, with neutral fixtures (see P6.1).
- A self-signed server works with `caFile`, and fails without it.
- A stopped VM in `criticalVMs` is critical; any other stopped VM warns.
- The legacy `DASH_PROXMOX_TOKEN` alias (if kept) is split correctly.

**Dependencies:** P3.1, P3.2, P1.4.

### P3.7 feat(plugins): hardware plugin for host metrics and machines

**Body:** move `internal/topics/host.go` metrics, the `Host*` queries (`internal/queries/queries.go:57-118`), the ZFS, SMART and temperature attention rules, and the machines (`vms.go:42-47,96-103`) into `hardware`.

- Config: `hosts[] {name, nodename, model, bays, fanMaxRPM, sensorAliases}`, `zfsScrubMetricPrefix`, `selectors`, `machines[] {name, nodeExporterNodename | gatusEndpoint}`, `tempThresholds`.
- The Hardware page reads `bays` and `fanMaxRPM` instead of `BAY_COUNT` and `FAN_SCALE_MIN` (`web/src/pages/hardware.tsx:46-47`).

**Acceptance criteria**
- No `mac-mini`, `rpi-control` or `16` bay literal remains.
- Host series names are unchanged.
- With no machines configured, the machines panel is hidden.

**Dependencies:** P3.1, P3.4, P3.6.

### P3.8 feat(plugins): kubernetes plugin with optional cluster and discovery-gated informers

**Body:** move `internal/kube`, `internal/topics/cluster.go`, PVC and etcd storage, backup-job freshness (generalising `NASBackupAge`), pod detail and the restart action into `kubernetes`.

- `mode: auto|inCluster|kubeconfig`. With no config, the state is `unavailable`, and `cmd/dash/main.go:59-62` no longer exits.
- Dynamic informers only for API resources present per discovery, re-checked every 5 minutes.
- Conventions default to empty.
- `upstreamViaAPIServer` (opt-in) hosts today's dev transport (`internal/upstream/transport.go`).
- Series: `node.*`, `pod.*`.

**Acceptance criteria**
- dash starts and serves Proxmox pages with no kubeconfig.
- Against a fake cluster without Flux or CNPG CRDs, the cluster topic publishes and the state is `degraded` (optional CRDs missing), not stuck.
- Restart action tests pass through the framework.
- `actions_test.go` uses neutral admission strings.

**Dependencies:** P2.6, P3.1, P3.2, P3.5.

### P3.9 feat(plugins): configurable apps discovery in the kubernetes plugin

**Body:** replace these literals:

- `routeAttachesToHome` (`gateway/home`, `internal/topics/feeds.go:87-96`)
- the `dash` namespace skip (`feeds.go:117`)
- the fixed `gethomepage.dev/` prefix (`feeds.go:123-135`)

with `apps {gateways, excludeNamespaces, annotationPrefix, ingress}`. With no `gateways`, all HTTPRoutes are accepted. Ingress support is optional, behind `ingress: true`.

**Acceptance criteria**
- Table tests for gateway filtering, namespace exclusion, prefix change, and Ingress on and off.
- The homelab config reproduces today's app list (verified by the owner).

**Dependencies:** P3.8.

### P3.10 feat(plugins): k8s secret references via the kubernetes plugin

**Body:** add a `k8s:namespace/name#key` resolver backed by the kubernetes plugin. It watches only the referenced Secrets, and a change reconfigures the consuming plugins. The chart grants `get` with `resourceNames`.

**Acceptance criteria**
- A fake clientset test resolves a ref and picks up an update.
- With the kubernetes plugin unavailable, there is a clear load error naming the field.
- No list or watch on all Secrets.

**Dependencies:** P3.8, P1.7.

### P3.11 feat(plugins): flux plugin with spec.verify-based source checks

**Body:** move the Flux informers, `topics/flux.go`, the reconcile action (`internal/api/actions.go:22-28,109-134`) and the Flux attention rules into `flux` (requires kubernetes).

- Replace the `flux-system/flux-system` literals (`attention.go:134-137`, `overview.go:44-48`, `web/src/pages/gitops.tsx:144-184,315`, `web/src/shell/command-palette.tsx:81-88`) with:
  - a critical item for any source that has `spec.verify` but no `SourceVerified=True`;
  - plus `verify.sources` pins.
- Move `suspendReasonAnnotation` here.

**Acceptance criteria**
- `attention_test.go` Flux cases are rewritten for the new rule and pass.
- The GitOps header and the palette list the configured or verified sources.
- The plugin is `blocked` without kubernetes and `unavailable` without Flux CRDs.

**Dependencies:** P3.8.

### P3.12 feat(plugins): cnpg and rustfs storage plugins and a composite Storage page

**Body:**

- `cnpg` (requires kubernetes, uses prometheus): clusters plus backup age.
- `rustfs` (requires prometheus): bucket usage and quota, plus cluster capacity.
- Remove `RustFS`, `CNPG`, `NASBackups` and `Etcd` from `model.StorageData` (`model.go:521-529`). The Storage page renders slots from kubernetes (PVCs, etcd, backup jobs), proxmox (storages), cnpg and rustfs.

**Acceptance criteria**
- Each panel appears only when its plugin is healthy, degraded or starting.
- No "RustFS" or "CNPG" text remains outside those plugin modules.

**Dependencies:** P3.6, P3.8, P2.8.

### P3.13 refactor!: remove the legacy topics manager, routes and model fields

**Body:** delete what remains of `topics.Manager`, the legacy `model.Topic` constants, `/api/vms`, `/api/pods`, `/api/logs`, `/api/actions/*`, `/api/silences`, `/api/config`, and the legacy demo source. `internal/demo` now composes the plugins' `Demo()` twins.

**Acceptance criteria**
- `go vet` and the tests pass.
- Grep finds no references to the removed routes in `web/src`.
- `DASH_DEMO=true` renders every page, with the demo test's identifier denylist still enforced.

**Dependencies:** P3.1 to P3.12.

---

## G4: feat(auth): phase 4, settings UI and auth

**Body:** make auth part of dash (design E) and build the settings experience (design B, C).

- [ ] P4.1 feat(auth): auth modes and proxy mode with trusted proxy CIDRs
- [ ] P4.2 feat(auth): viewer and admin roles enforced on actions
- [ ] P4.3 feat(auth): local admin with bootstrap token and sessions
- [ ] P4.4 feat(auth): built-in OIDC login
- [ ] P4.5 feat(auth): explicit no-auth mode with warnings
- [ ] P4.6 feat(api): security headers and content security policy
- [ ] P4.7 feat(api): settings API with provenance, locking and secret endpoints
- [ ] P4.8 feat(web): settings plugin list and status page
- [ ] P4.9 feat(web): schema-driven plugin forms with locked and write-only fields
- [ ] P4.10 feat(web): audit log view and restart-required notice

**Acceptance criteria:** all leaves closed. An admin can enable and configure a plugin from the UI; a viewer cannot reach settings or actions; secrets are never returned by any endpoint.

**Dependencies:** G1, G2. P4.2 also needs P2.3.

### P4.1 feat(auth): auth modes and proxy mode with trusted proxy CIDRs

**Body:** replace `auth` (`internal/api/middleware.go:32-51`) with a mode switch. `proxy` mode takes `userHeaders` (default: today's two), `groupsHeader` and `trustedProxies` (default loopback). Headers from untrusted peers are ignored. Dev and demo behaviour are kept.

**Acceptance criteria**
- A request with `X-Forwarded-User` from a non-trusted remote address gets 401.
- From 127.0.0.1 it is accepted.
- The homelab sidecar flow works unchanged (owner-verified).

**Dependencies:** P1.2.

### P4.2 feat(auth): viewer and admin roles enforced on actions

**Body:** `auth.User{Name, Role}`.

- Proxy role mapping: `adminUsers`/`adminGroups`. If both are empty, every authenticated user is admin, with a warning.
- The action framework enforces `MinRole`. The SPA hides actions for viewers, using `/api/me`, which gains `role`.

**Acceptance criteria**
- A viewer gets 403 on every action and on `/api/settings/*`.
- The UI shows no action buttons to viewers.
- Existing behaviour holds when the lists are empty.

**Dependencies:** P4.1, P2.3.

### P4.3 feat(auth): local admin with bootstrap token and sessions

**Body:** local mode with an argon2id hash, stored in settings or supplied via `DASH_ADMIN_PASSWORD_HASH`.

- Bootstrap: a one-time setup token is logged once (1h, single use); `/setup?token=` sets the password.
- `dash hash-password` prints a hash.
- `/login` and `/auth/logout`.
- AEAD session cookie keyed via HKDF from the secret key; 12h idle, 7d absolute; epoch-based "log out everywhere". Without `DASH_SECRET_KEY` the session key is random per process, so logins end on restart.
- Optional `anonymousViewer`.
- The SPA's 401 handling becomes mode-aware (`web/src/api/client.ts:24-32`, `web/src/api/stream.ts:40-47`).

**Acceptance criteria**
- The setup token cannot be reused or used after expiry.
- A wrong password is rate-limited and audited.
- A cookie is invalid after an epoch bump.
- Login POSTs require a valid Origin.

**Dependencies:** P4.2, P1.5, P1.8.

### P4.4 feat(auth): built-in OIDC login

**Body:** authorization code with PKCE, using `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`.

- Config: `issuer`, `clientID`, `clientSecret` (secret), `scopes`, `allowedUsers`/`allowedGroups`, `adminGroups`, `roleClaim`.
- Sessions as in P4.3.

**Acceptance criteria**
- Integration test against a local mock OIDC provider: login, role mapping and denial of disallowed users.
- State and nonce are validated.

**Dependencies:** P4.3.

### P4.5 feat(auth): explicit no-auth mode with warnings

**Body:** `DASH_AUTH_MODE=none`: everyone is admin. It is never the default. A WARN on every start, and a persistent UI banner.

**Acceptance criteria**
- The mode is impossible without the explicit setting.
- The banner is visible on every page.
- The docs recommend binding to `127.0.0.1`.

**Dependencies:** P4.2.

### P4.6 feat(api): security headers and content security policy

**Body:** on SPA and API responses (`internal/api/static.go`):

- `Content-Security-Policy: default-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data: <icon origin>; frame-ancestors 'none'`
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: same-origin`

**Acceptance criteria**
- The built SPA loads with no CSP violations, checked in a browser smoke run.
- An injected inline script is blocked.

**Dependencies:** P2.7.

### P4.7 feat(api): settings API with provenance, locking and secret endpoints

**Body:** a `/api/settings/**` route group: auth, then admin, then CSRF, then audit (design E.5).

- `GET` returns the effective tree with `source` and `locked` per field and secret metadata only.
- `GET schema`.
- `PUT plugins/{id}`: merge patch with `If-Match`.
- `POST plugins/{id}/test` runs `Checker` against a candidate config.
- `PUT|DELETE plugins/{id}/secrets/{field}`.
- `DELETE plugins/{id}/stored/{field}`: remove a stored UI value.
- `GET export`: a `config.yaml` snippet of the admin-saved UI keys.
- `GET audit`.

**Acceptance criteria**
- Every write (PUT, PATCH, DELETE, secret Replace/Clear) to a file- or env-locked path returns 409 with `{path, source, ref}`, tested per verb for both file- and env-locked fields.
- Remove-stored-value is the only write accepted on a locked path.
- With `settings.ui: readonly`, every write returns 409.
- A stale `If-Match` returns 412.
- No response body ever contains a secret (canary test across all endpoints).
- The export contains only admin-saved keys and no plaintext secret (canary test).
- Every change is audited, with field paths only.

**Dependencies:** P4.2, P1.6, P2.2.

### P4.8 feat(web): settings plugin list and status page

**Body:** a `/settings` route (admin only) listing plugins with:

- state chip, message and dependency info;
- an enable toggle (disabled when locked);
- a link to configure.

The general section shows read-only server and auth info, a banner when settings are read-only, and an "Export as config.yaml snippet" button.

**Acceptance criteria**
- The list updates live from `core.plugins`.
- Locked toggles show their source on hover.
- Viewers cannot navigate to `/settings`.
- The export button downloads or copies the snippet from `GET /api/settings/export`.

**Dependencies:** P4.7, P2.7.

### P4.9 feat(web): schema-driven plugin forms with locked and write-only fields

**Body:** render a form from the JSON Schema:

- locked fields are read-only and show their source;
- secret fields are write-only (Replace or Clear), never prefilled;
- a "Test connection" button;
- per-field errors from the server;
- an optional custom `settings` component per module.

**Acceptance criteria**
- A Proxmox config can be entered, tested and saved, and the plugin becomes healthy without a reload.
- Secret inputs never appear in the DOM after save.
- Keyboard-accessible.

**Dependencies:** P4.8.

### P4.10 feat(web): audit log view and restart-required notice

**Body:** settings → audit list (latest N), and a global notice when a saved field needs a restart.

**Acceptance criteria**
- The audit view shows settings and action events.
- Changing `server.listen` in a file shows "restart required" until restart.

**Dependencies:** P4.7, P1.7.

---

## G5: build(deploy): phase 5, packaging and deployment targets

**Body:** ship one image that works with Docker, Compose, PaaS and Kubernetes (design D).

- [ ] P5.1 build(docker): data volume, exposed ports, healthcheck command and OCI labels
- [ ] P5.2 feat(config)!: listen on 0.0.0.0:8080 by default
- [ ] P5.3 docs(deploy): docker run and compose examples with a CI smoke test
- [ ] P5.4 feat(deploy): Helm chart with RBAC modes and optional guardrail policy
- [ ] P5.5 docs(deploy): plain Kubernetes manifests rendered from the chart
- [ ] P5.6 docs(deploy): PaaS guide and read-only settings without a volume

**External dependency (private repo, not filed here):** the homelab migrates to the chart, or adds a ConfigMap and `DASH_AUTH_MODE=proxy`, and pulls from GHCR.

**Acceptance criteria:** all leaves closed. The examples in the docs are exercised in CI.

**Dependencies:** G3, P4.1, P4.3.

### P5.1 build(docker): data volume, exposed ports, healthcheck command and OCI labels

**Body:**

- Add `EXPOSE 8080 8081`, plus a `/data` directory owned by 65532 (created in the build stage, copied with `--chown`), `VOLUME /data` and `ENV DASH_DATA_DIR=/data`.
- Add a `dash healthcheck` subcommand and `HEALTHCHECK CMD ["/dash","healthcheck"]`.
- Add OCI labels.
- Add `dev/` and `.github/` to `.dockerignore`.
- Keep the distroless nonroot base and the digest pins.

**Acceptance criteria**
- `docker run` reports `healthy` within 30s.
- `docker buildx build --platform linux/amd64,linux/arm64 .` succeeds on an amd64 host without QEMU.
- `docker inspect` shows the labels and the volume.

**Dependencies:** P2.6.

### P5.2 feat(config)!: listen on 0.0.0.0:8080 by default

**Body:** change the `server.listen` default from `127.0.0.1:8080` (`internal/config/config.go:97`) to `0.0.0.0:8080`. The sidecar chart option sets loopback explicitly. Release notes are marked breaking.

**Acceptance criteria**
- With the default `local` mode, unauthenticated requests get 401 or a login redirect.
- In `proxy` mode, spoofed headers from a non-loopback peer are rejected (covered by the P4.1 tests).

**Dependencies:** P4.1, P4.3, P5.1.

### P5.3 docs(deploy): docker run and compose examples with a CI smoke test

**Body:**

- `docs/deploy/docker.md`.
- `examples/compose/compose.yaml`: dash, plus optional `monitoring` profile services Prometheus and Alertmanager, with a `secrets:` example that includes a `dash_secret_key` secret wired to `DASH_SECRET_KEY_FILE`.
- A CI job runs `docker compose up`, waits for healthy, logs in through the bootstrap flow, and enables Prometheus via the settings API.

**Acceptance criteria**
- The CI job passes on PRs that touch `examples/`, `Dockerfile` or `internal/`.
- The docs commands are copy-paste runnable.

**Dependencies:** P5.2, P4.7.

### P5.4 feat(deploy): Helm chart with RBAC modes and optional guardrail policy

**Body:** `charts/dash` (design D.4):

- the config file as a ConfigMap, `secretEnv`;
- required `secretKey.existingSecret` (no default; `helm template` fails without it);
- render fails if any `x-dash-secret` field in `.Values.config` is a literal instead of an `env:`/`file:`/`k8s:` ref (secrets come from `existingSecret`, `secretEnv` or refs);
- persistence (PVC, or emptyDir with read-only settings), with `persistence.accessModes` offering `[ReadWriteOncePod]` (where the CSI driver supports it) besides the default `[ReadWriteOnce]`;
- RBAC `read`, `actions.restart`, `actions.fluxReconcile` and `secretRefs` (`resourceNames`);
- an optional oauth2-proxy sidecar;
- optional Ingress or HTTPRoute;
- an optional generic ValidatingAdmissionPolicy fencing the ServiceAccount to the enabled actions, written fresh;
- `strategy: Recreate` always;
- a replica guard: render fails if `replicaCount > 1` unless `settings.ui: readonly` and `persistence.enabled: false`.

Publish the chart as OCI to GHCR from the release workflow (P6.7).

**Acceptance criteria**
- `helm lint` and `helm template` succeed for the read-only, actions and sidecar value sets.
- A kind-based CI job installs the chart and reaches ready.
- With actions off, the RBAC has no `patch` verbs.
- `helm template` fails without `secretKey.existingSecret`, with a literal secret in `config`, and with `replicaCount: 2` while settings are editable or persistence is on.
- The rendered Deployment uses `Recreate`.

**Dependencies:** P3.8, P3.10, P5.2.

### P5.5 docs(deploy): plain Kubernetes manifests rendered from the chart

**Body:** `examples/kubernetes/` is a kustomize base generated by `helm template` and checked for drift in CI. `docs/deploy/kubernetes.md` covers both paths and the three Secret options.

**Acceptance criteria**
- The drift check passes.
- `kubectl apply -k` on kind reaches ready.

**Dependencies:** P5.4.

### P5.6 docs(deploy): PaaS guide and read-only settings without a volume

**Body:** `docs/deploy/paas.md` covering Fly.io, Render, Cloud Run and ECS. It explains env and mounted-file config, the stateless caveats (`DASH_SETTINGS_UI=readonly`, `DASH_ADMIN_PASSWORD_HASH`, `DASH_SECRET_KEY`) and single-replica guidance. Verify the unwritable-data-dir banner.

**Acceptance criteria**
- Running with `--read-only` and no volume starts, shows the read-only banner, and accepts env and file config.

**Dependencies:** P1.6, P4.3.

---

## G6: chore(oss): phase 6, open-source readiness and public CI

**Body:** clear every blocker in design G and move CI and release to public infrastructure (design H). P6.1, P6.2, P6.4, P6.5 and P6.6 can start right after G0.

- [ ] P6.1 test: replace real-lab identifiers in fixtures and add a denylist test
- [ ] P6.2 chore(dev): decouple dev tooling from ../homelab
- [ ] P6.3 docs: rewrite the README and add generated configuration docs
- [ ] P6.4 docs: add SECURITY, CONTRIBUTING, CODE_OF_CONDUCT and templates
- [ ] P6.5 chore: add LICENSE and third-party notices
- [ ] P6.6 ci: move CI to GitHub-hosted runners with secret and workflow scans
- [ ] P6.7 ci(release): publish multi-arch images and the chart to GHCR with attestations
- [ ] P6.8 docs: automate screenshots from demo mode
- [ ] P6.9 chore(repo): pre-publication repository settings
- [ ] P6.10 chore(repo): execute the history decision, run final scans and make the repo public

**Acceptance criteria:** all leaves closed; the repository is public.

**Dependencies:** P6.10 depends on G1 to G5 and on P6.1 to P6.9.

### P6.1 test: replace real-lab identifiers in fixtures and add a denylist test

**Body:** rewrite the fixtures in:

- `internal/upstream/clients_test.go`, `transport_test.go`
- `internal/queries/queries_test.go`
- `internal/api/api_test.go`, `silences_test.go`, `actions_test.go`
- `internal/topics/attention_test.go`
- `internal/links/links_test.go`

Use `example.com`, `192.0.2.0/24`, neutral node, VM and tenant names, a zero-filled token fixture, and `admission denied:` strings. Fix the comments at `cmd/dash/main.go:1`, `internal/demo/demo.go:2` and `internal/api/actions.go:41`. Add a repo-wide denylist test (generalising `internal/demo/demo_test.go:44`) with an explicit allowlist (module path, GHCR image, LICENSE).

**Acceptance criteria**
- The denylist test fails on any of the identifiers listed in design G.2, and passes on the tree.
- No test assertion loses coverage: same cases, new names.

**Dependencies:** none (can run after G0; rebase over G3 moves).

### P6.2 chore(dev): decouple dev tooling from ../homelab

**Body:**

- Make `HL_OP` and `KUBECONFIG` in `Makefile:1-2` optional.
- `make dev-api` runs with `DASH_CONFIG=dev/config.yaml` (git-ignored; an example is committed) and any `KUBECONFIG`.
- Keep an opt-in target for the 1Password broker, or move it to the homelab repo.
- Remove `dev/secrets.env`, or replace it with an example.
- Document `DASH_DEMO=true` as the default contributor path.

**Acceptance criteria**
- On a clean checkout without `../homelab`, `make dev-api` with `DASH_DEMO=true`, and `make dev-web`, both work.
- No tracked file references `../homelab`.

**Dependencies:** none (P1.1 for `DASH_CONFIG`, otherwise use env).

### P6.3 docs: rewrite the README and add generated configuration docs

**Body:**

- A new README: generic intro, feature list per plugin, quick start (docker run, compose, Helm), screenshots from demo mode, and links to docs.
- Remove the homelab-specific text (`README.md:23-28,51-53,150-151,166-174`).
- `docs/configuration.md` and `docs/plugins/<id>.md` generated from the schemas, plus `docs/auth.md` and `docs/security.md`.

**Acceptance criteria**
- The link check passes (lychee or equivalent, in CI).
- The denylist test passes.
- The generated docs drift check is in `make check`.

**Dependencies:** P2.2, G3, G4, G5 (content).

### P6.4 docs: add SECURITY, CONTRIBUTING, CODE_OF_CONDUCT and templates

**Body:**

- `SECURITY.md`: private vulnerability reporting, supported versions, scope.
- `CONTRIBUTING.md`: mise, `make check`, `make generate`, demo mode, conventional commits, how to add a plugin.
- `CODE_OF_CONDUCT.md`: Contributor Covenant 2.1.
- Issue and PR templates, `CODEOWNERS`.

**Acceptance criteria**
- The files exist, and GitHub's community profile shows them as complete.

**Dependencies:** P0.2 (licence and conduct choices).

### P6.5 chore: add LICENSE and third-party notices

**Body:**

- Add `LICENSE` per the P0.2 decision.
- Generate `THIRD_PARTY_NOTICES` from `go-licenses report` and an npm licence report.
- Attribute dashboard-icons (Apache-2.0) and the vendored shadcn/ui and Magic UI components.
- A CI check fails on disallowed licences.

**Acceptance criteria**
- The notices are generated reproducibly.
- The CI licence check passes.
- The Magic UI licence is confirmed at its source.

**Dependencies:** P0.2.

### P6.6 ci: move CI to GitHub-hosted runners with secret and workflow scans

**Body:**

- Replace `runs-on: [self-hosted, linux, x64, homelab, dash]` (`.github/workflows/ci.yml:13,22`) with `ubuntu-24.04`.
- Add a `pull_request` trigger, `persist-credentials: false`, and least-privilege `permissions`.
- Add a `scan` job: gitleaks and trufflehog over full history, govulncheck, and actionlint/zizmor failing on `pull_request_target`, `workflow_run`, `issue_comment` and unpinned actions.
- Add Dependabot for `github-actions`, `gomod`, `npm` and `docker`.
- Keep the existing private release job until P6.7.

**Acceptance criteria**
- No workflow references self-hosted labels.
- The scans pass on the current history.
- A PR from a branch runs `check` and `scan` on GitHub-hosted runners.

**Dependencies:** none.

### P6.7 ci(release): publish multi-arch images and the chart to GHCR with attestations

**Body:** a release job on tags `vX.Y.Z` (design H.2):

- tag validation and a create-only check;
- `docker/metadata-action` tags `X.Y.Z`, `X.Y`, `X` (not for 0.x) and `latest`;
- `build-push-action` for `linux/amd64,linux/arm64` with provenance and SBOM;
- `attest-build-provenance`, plus `attest-sbom` with an SPDX SBOM from `anchore/sbom-action`;
- optional cosign;
- `helm push` of the chart to `oci://ghcr.io/techdufus/charts`;
- the digest in the job summary.

Remove the Zot push (`ci.yml:31`). README release steps are updated.

**Acceptance criteria**
- A pre-release tag on a fork or test repo publishes both architectures.
- `gh attestation verify oci://ghcr.io/techdufus/dash:<v> --owner TechDufus` succeeds.
- Re-pushing an existing version fails.

**Dependencies:** P6.6, P5.1, P5.4. **External:** the homelab switches its image to GHCR (design H.3 option a), or a private mirror job in the homelab repo.

### P6.8 docs: automate screenshots from demo mode

**Body:** `make screenshots` starts `DASH_DEMO=true` and captures every page with Playwright into `docs/images/`, replacing the hand-made images after the plugin UI changes.

**Acceptance criteria**
- The script is deterministic (fixed viewport, fixed demo clock).
- The images are regenerated and committed.

**Dependencies:** G3, G4 (final UI).

### P6.9 chore(repo): pre-publication repository settings

**Body:** a checklist issue, with no code.

- Deregister the self-hosted runner from this repo, or remove the repo from the org runner group and disable "Allow public repositories" there.
- Actions: require approval for fork PR workflows from external contributors; default token read-only; Actions may not create or approve PRs.
- Branch rulesets on `main`: required checks, no force-push, signed commits if desired.
- Secret scanning with push protection, Dependabot alerts, private vulnerability reporting.
- GHCR package linked to the repo.

**Acceptance criteria**
- Each setting is verified and ticked by the owner, with a screenshot or `gh api` output attached (no secrets).

**Dependencies:** P6.6.

### P6.10 chore(repo): execute the history decision, run final scans and make the repo public

**Body:**

- Apply the P0.2 history decision: keep, or a fresh squash into a new repo or orphan branch. Do not use `filter-repo`.
- Run gitleaks and trufflehog over all refs one last time.
- Confirm the denylist test is green.
- Make GHCR packages public.
- Flip the repository visibility.

**Acceptance criteria**
- Scan reports are clean (attached as summaries with no findings).
- The repo and packages are public.
- `docker pull ghcr.io/techdufus/dash:<v>` works anonymously.
- The first public release notes are published.

**Dependencies:** everything else in this epic.
