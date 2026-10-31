# ADR 0001: Plugins, layered config, auth and release

- Status: Accepted
- Date: 2026-10-31
- Deciders: TechDufus

## Context

dash today reads a flat set of `DASH_*` variables, requires a Kubernetes cluster, hard-codes five upstream integrations, and trusts proxy headers from any peer. The goal is a project that other people can run: configurable through environment variables, a config file and a settings UI; integrations as plugins; deployable as a plain container, Compose, PaaS or Kubernetes; and ready to be published as open source.

The full design and its analysis live in [../design/plugins-and-config.md](../design/plugins-and-config.md) (being added in PR #1). This ADR records only the outcomes. The 19 decisions are numbered as in section J of that document, and each cites the design section that holds the detail.

## Decisions

### 1. Licence

**Decision.** MIT, with the copyright line "Matthew DeGarmo (TechDufus)" (design G.6).

**Alternatives considered.**
- Apache-2.0 (the earlier lean): patent grant and a NOTICE mechanism, but heavier for a self-hosted dashboard.
- MPL-2.0: file-level copyleft, less familiar and weaker than AGPL against hosted forks.
- AGPL-3.0: protects against closed SaaS forks, but many companies ban it and it is overkill here.
- BSL 1.1 / FSL: not OSI open source, which contradicts the aim of open-sourcing.

**Consequences.** MIT has no patent grant and no copyleft; that is accepted. `LICENSE` is added in phase 6 (P6.5), not in this change. Third-party notices (`NOTICE` / `THIRD_PARTY_NOTICES`) are still needed for bundled assets and dependencies (design G.5, G.7).

### 2. Plugin boundary

**Decision.** Plugins are in-process Go packages in an explicit, compile-time list (`cmd/dash/plugins.go`). The package stays `internal/plugin` until the API is stable at 1.0, then is promoted to `pkg/plugin` (design A.1).

**Alternatives considered.**
- Out-of-process plugins (HTTP/gRPC sidecar): any language and isolation, but needs a versioned wire protocol, inter-process auth and more deployment pieces, and a stable data contract first.
- hashicorp/go-plugin (subprocess, gRPC): many binaries in the image; awkward with distroless and read-only filesystems.
- WASM (for example wazero): sandboxed, but network, TLS and client-go inside WASM are immature and the effort is large.
- `init()` self-registration: no gain over an explicit list, and ordering is hidden.

Out-of-process options are rejected for v1 only.

**Consequences.** One static binary and one test suite; no IPC. A new plugin means a new build, so third parties fork or build their own image until `pkg/plugin` exists. The interface stays transport-agnostic (JSON-serialisable topic payloads, no Go handles across the boundary except through `Host.Service`) so a remote-plugin adapter can be added later without changing the core. The SPA has a matching compiled-in module per plugin ID and never loads remote JS.

### 3. Settings store

**Decision.** No database. One YAML store, `$DASH_DATA_DIR/ui-settings.yaml`, with atomic writes, plus `audit.jsonl` (design B.7).

**Alternatives considered.**
- SQLite (`modernc.org/sqlite`, separate `dash.db`): real transactions and queryable tables, but a new dependency and no human-readable, diffable backups. To be reconsidered only if dash needs multiple users with roles, server-side sessions or a searchable audit log.
- bbolt: no migrations or query language, single-process like SQLite, so nothing over SQLite except a smaller binary.

**Consequences.** The store is sparse (only keys an admin saved), versioned with `schemaVersion` and ordered migrations, written via temp file, fsync and rename (plus a directory fsync), and keeps 5 rotated backups. A single writer under a mutex and an `flock` on `/data/.lock` refuse a second instance. Sessions are stateless signed cookies. If SQLite is added later, it goes in a separate `dash.db`, never mixed into the YAML store. The store sits behind an interface so that change stays local.

### 4. Precedence

**Decision.** Layers from lowest to highest: built-in defaults, UI settings, config file, environment (`defaults < UI < file < env`). A field set by the file or by env is locked: the UI shows it read-only with its source, and the server rejects writes to it with `409 Conflict`. Secret references are a value form usable in any layer, not a layer. The Helm chart defaults to `settings.ui: editable`; `readonly` is the opt-in for GitOps installs, and the homelab can opt in (design B.2, D.4).

Two follow-on defaults are part of this decision:
- Removing a stored value on a locked field stays allowed. It deletes a shadowed UI key and cannot change the effective value. With `settings.ui: readonly`, every settings write is refused, including this one.
- The Helm replica guard stays strict: `replicaCount > 1` is allowed only with `settings.ui: readonly` and persistence off, and the template fails otherwise. Multi-replica therefore requires opting into readonly.

**Alternatives considered.**
- UI overrides everything: breaks GitOps, lets the running state differ from the reviewed state, and lets two operators fight over a value unnoticed. Vaultwarden and Grafana SSO settings show the failure mode of UI-wins.
- A readonly chart default: safe for GitOps, but a fresh install could not be configured in the UI. Rejected as the default.

**Consequences.** File and env values are overlaid at read time and never copied into the UI store, so removing them reveals the stored UI value or the default. Lists lock as a whole; `plugins.<id>.managed: true` locks a whole plugin. Shadowed UI values are shown as unused, with a "remove stored value" action. An export-as-snippet button is the path to GitOps; dash never writes to the operator's config file. The chart needs a template guard for the replica rule. The homelab's own switch to `readonly` is a change in its private repo.

### 5. Legacy `DASH_*` names

**Decision.** A one-release alias layer maps the legacy `DASH_*` names to the new config paths and logs one deprecation warning per variable at startup. It is then removed (design B.5).

**Alternatives considered.**
- Direct cutover without an alias layer: simpler, but breaks the one existing deployment on upgrade.

**Consequences.** The alias table (including parsing the old `DASH_PROXMOX_TOKEN` form) must be built and later deleted. Defaults specific to the original deployment (service URLs, label conventions) are removed and become empty, so the deployment must set those values explicitly before the leaf that removes the defaults lands.

### 6. Default auth mode

**Decision.** `auth.mode=local`: one admin account with an argon2id hash, bootstrapped with a one-time setup token logged at first start. `proxy` (with `trustedProxies` CIDRs) and `oidc` are first-class options. The anonymous read-only viewer for wall displays (`auth.local.anonymousViewer`) is opt-in and off by default (design E.2, E.6).

**Alternatives considered.**
- `proxy` as the default: needs extra infrastructure, and unsafe if the listener is reachable by untrusted peers.
- `oidc` as the default: needs an identity provider before first use.

**Consequences.** `local` is the only mode that is safe on `0.0.0.0` with no extra infrastructure. In-app auth (sessions, CSRF and Origin checks, setup flow, `dash hash-password`) must be built before the `0.0.0.0` container default. `none` stays available only through an explicit setting, with a warning and a UI banner. The existing deployment sets `proxy` in the release that introduces auth modes.

### 7. Kubernetes packaging

**Decision.** A Helm chart in this repository (`charts/dash`), published as an OCI chart to GHCR from the same release workflow and at the same version (design D.4).

**Alternatives considered.**
- A separate charts repository: another repo and release process to keep in step with the image.
- Manifests or kustomize only: less configurable. Plain manifests are still offered as `examples/kubernetes/`, rendered from the chart with `helm template`.

**Consequences.** The chart owns the Deployment (`Recreate` strategy), persistence, RBAC (read and per-action), optional admission-policy guardrail and template guards (required secret key, no literal secrets in values, replica rule from decision 4). CI must render and check the example manifests. Chart and image are released together.

### 8. Git history

**Decision.** Keep the history; no squash. Right before the repository goes public, scrub the homelab identifiers from history with `git filter-repo --replace-text`. That scrub is a separate, later step: it is a history rewrite plus a force-push, and it needs TechDufus's explicit go-ahead at that time (design G.9, G.10).

**Alternatives considered.**
- Fresh squash or new repository: a clean slate and trivial with so few commits, but loses history and signatures and orphans existing clones.
- Keeping history unscrubbed: honest provenance at no cost, but internal hostnames, addresses and the private repo layout would stay readable in public.

**Consequences.** The scrub rewrites every SHA, so signatures are lost and existing clones and refs are orphaned. It must be verified with the denylist test and a re-run of gitleaks and trufflehog over all refs. Branch protection is enabled only after the force-push. Nothing in this ADR authorises the rewrite; it is not done until the go-ahead is given.

### 9. Homelab image source

**Decision.** The homelab pulls the image from GHCR directly, pinned by version and digest. An optional pull-through cache on its private Zot registry can sit in front (design H.3).

**Alternatives considered.**
- A private mirror job in the homelab repository that copies the attested digest into Zot: workable, but a second artefact and pipeline. If the cluster must never reach GHCR, that job must never run in or be triggered by the public repo.
- Continuing to push to the private registry from this repository's CI: needs homelab credentials and a reachable runner in a public repo's workflow.

**Consequences.** There is one artefact, publicly attested, and no homelab secret in a public workflow. The push to the private registry is removed from this repo's CI before it goes public. The "bump the manifest" step lives in the homelab repo's docs, not here.

### 10. Plugin naming

**Decision.** The plugin `prometheus` is displayed as "Prometheus-compatible" (it covers VictoriaMetrics, Thanos and Mimir). `victorialogs` and `alertmanager` keep their names. `hardware`, `rustfs` and `cnpg` are separate plugins (design F.1, F.7, F.10).

**Alternatives considered.**
- The name `victoriametrics`: the code speaks the generic `/api/v1/query` API, so the name would be too narrow.
- Packs inside `prometheus` (host hardware, storage backends): would tie them to the generic client, and they could not be enabled, shown or configured on their own.

**Consequences.** `prometheus` stays a generic query client that exposes a `Querier` service and hosts series. `hardware` requires it; `cnpg` requires `kubernetes` and uses it; `rustfs` requires it. Plugin IDs match `[a-z][a-z0-9]{1,31}` so env names parse unambiguously. A generic S3-usage plugin may follow if another backend is requested.

### 11. Actions default

**Decision.** Actions are off per plugin: `actions.enabled: false` (design A.8).

**Alternatives considered.**
- On by default: convenient, but a fresh install would expose write operations (restarts, reconciles, silences) before an operator chose to.

**Consequences.** With actions disabled the action route is not mounted and the SPA hides its buttons. Each plugin that declares actions needs the `actions.enabled` field. The existing deployment sets it to `true` explicitly. The admission policy remains the real fence for Kubernetes writes.

### 12. Roles

**Decision.** Two roles in v1: `viewer` and `admin` (design E.4).

**Alternatives considered.**
- An `operator` role (actions without settings): rejected for v1 to keep the model and the route groups simple.

**Consequences.** Viewers read every enabled page; admins also run actions and use `/api/settings/*` and the audit log. Each mode needs a role mapping (`local` account is admin; `proxy` via `adminUsers`/`adminGroups`; `oidc` via `adminGroups`/`roleClaim`). Anyone who needs actions must also be able to change settings, until an `operator` role is added in a later version.

### 13. Scope gaps

**Decision.** A standalone logs page (VictoriaLogs without the `kubernetes` plugin) and backup freshness without cluster access are deferred to post-v1 (design D.6, F.5).

**Alternatives considered.**
- Building either in v1: extra scope for features that depend on cluster objects today (the log entry point is the pod sheet; backup jobs live in `kubernetes.backupJobs`).

**Consequences.** Without a cluster, `victorialogs` is healthy but has no page, and backup-age panels are hidden. Both are documented limits, tracked as post-v1 work.

### 14. Branding

**Decision.** Keep the name `dash` and the tagline "Homelab mission control". The name collision with the Debian `dash` shell in search results is accepted.

**Alternatives considered.**
- Renaming: avoids search collisions, but costs the existing identity, image and repo names.

**Consequences.** Search discoverability is weaker than a unique name; the repository description, README and docs need to disambiguate. The existing copy stays (design 1.1, G.1).

### 15. Demo mode

**Decision.** Demo mode stays opt-in through a config flag (`DASH_DEMO` or the config file). There is no separate `:demo` image tag. A public demo instance comes later, post-v1 (design D.6, H.2).

**Alternatives considered.**
- A `:demo` image tag: a second tag to build, scan and document.

**Consequences.** Demo mode ships in the normal image and is built from each plugin's `Demo()`, so every plugin must provide one. Demo mode is also the source for documentation screenshots.

### 16. Multiple instances per plugin

**Decision.** One instance per plugin type in v1. The schema is already shaped for more: `plugins.<instance>.type`, where the type defaults to the instance name (design A.2).

**Alternatives considered.**
- Full multi-instance support in v1: more UI, topic and routing work before there is demand.

**Consequences.** The core enforces one instance per type. Topics, routes and env names already use the instance name, so lifting the limit later needs no schema or env-naming change.

### 17. Icon source

**Decision.** Icons are bundled or self-hosted by default. The jsDelivr CDN is opt-in through `ui.iconBaseURL`, which can also point at another mirror (design G.5).

**Alternatives considered.**
- The CDN as the default: no bundling work, but the browser would make third-party requests by default, which is a privacy cost.

**Consequences.** Icons must be bundled or served by dash, with attribution in NOTICE (dashboard-icons is Apache-2.0; the logos are their owners' trademarks). The runtime CDN references in the server and SPA are replaced.

### 18. Supported upstream versions to test against

**Decision.** Proxmox VE 8 and 9, the last 3 Kubernetes minor versions, current Flux v2, and Gateway API v1 (design H.2).

**Alternatives considered.**
- Testing only the latest upstream versions: cheaper, but leaves users on supported older releases untested.

**Consequences.** CI runs the `kubernetes`, `flux` and `cnpg` plugin tests against the last 3 Kubernetes minors with current Flux v2 and Gateway API v1 CRDs. The `proxmox` plugin is tested against recorded API fixtures for VE 8 and 9. The Kubernetes matrix moves as new minors are released.

### 19. Release cadence and versioning

**Decision.** Stay on 0.x until the plugin API is stable. Releases follow semver and are cut from git tags `vX.Y.Z`. `pkg/plugin` does not exist before 1.0 (decision 2), so there is no compatibility promise for it until then (design H.2).

**Alternatives considered.**
- A 1.0 release before the plugin API is stable: would imply compatibility guarantees that dash cannot yet keep.

**Consequences.** Breaking changes are allowed in 0.x and should be called out in release notes. Image and chart are versioned together from the tag. The `X` major tag is not published for `v0.*`. The 1.0 gate is the stable plugin API and the move to `pkg/plugin`.

## Consequences overall

- The decisions are implemented by the phased plan in the design document (section I). Phases 1 to 6 each land through their own pull requests.
- Phase 0 lands the configurable-settings branch (P0.1) and this ADR; the existing deployment must keep running the resulting image with unchanged behaviour.
- Two steps need the owner's explicit approval when they come up: the history scrub and force-push (decision 8) and the visibility flip.

## References

- [../design/plugins-and-config.md](../design/plugins-and-config.md), section J (decisions) and the sections cited above.
- Epic leaf P0.2 (record plugin, config, auth and release decisions).
