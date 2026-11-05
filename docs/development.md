# Developing dash

dash is one Go binary (Go 1.27, stdlib `net/http`, client-go informers) that serves the React SPA
embedded at build time. Tools are pinned in [`mise.toml`](../mise.toml) (`mise install`). Dev mode
needs nothing outside this checkout.

## Demo data (the default for contributors)

No cluster, no credentials:

```sh
DASH_DEMO=true make dev-api   # Go API on 127.0.0.1:8080, the demo twins' fake data
make dev-web                  # Vite on http://localhost:5173, proxying /api to the Go API
```

`DASH_DEMO=true` runs dash without a cluster: every plugin runs its demo twin, which publishes a
small, obviously fake homelab on its topics (with values that drift so the UI looks live) and
answers its REST routes and actions from the same fixtures (`internal/demo/world`); the overview
is composed from what the twins contribute, as in production. Nothing contacts Kubernetes,
Proxmox or the monitoring services, the actions answer without changing anything, and every link
points at `example.com`.

`make dev-web` runs `npm ci --ignore-scripts` first when `web/node_modules` is missing or older
than the lockfile. Health answers on http://127.0.0.1:8081/healthz. Open http://localhost:5173.

### Demo mode without make

The SPA is embedded at build time, so build it before dash:

```sh
cd web && npm ci --ignore-scripts && npm run build && cd ..
DASH_DEMO=true go run ./cmd/dash   # http://127.0.0.1:8080
```

Open http://127.0.0.1:8080. `http://localhost:8080` loads the pages too, but its writes
(restart, reconcile, silences) are refused as `csrf_failed`: dash accepts writes only from its
public URL, which in demo mode without dev mode defaults to the listen address. To browse at
`localhost`, start dash with `DASH_SERVER_PUBLIC_URL=http://localhost:8080`.

### When the ports are taken

Move the API and point Vite at it. Start `make dev-web` first, with the API's new address; Vite
picks the next free port on its own and prints it. Then start `dev-api` with
`DASH_SERVER_PUBLIC_URL` on that port, since dash only accepts writes from its public URL:

```sh
DASH_API=http://127.0.0.1:18080 make dev-web   # prints the port it picked
DASH_SERVER_LISTEN=127.0.0.1:18080 DASH_SERVER_HEALTH_LISTEN=127.0.0.1:18081 \
  DASH_SERVER_PUBLIC_URL=http://localhost:<that port> DASH_DEMO=true make dev-api
```

## A real cluster (optional)

`make dev-api` without `DASH_DEMO` reads what you point it at, and every piece is optional:

- the cluster: `KUBECONFIG` from your environment (pick a context with
  `plugins.kubernetes.context`);
- the config file: copy `dev/config.example.yaml` to `dev/config.yaml` (git-ignored), which
  `make dev-api` passes as `DASH_CONFIG` unless you set `DASH_CONFIG` yourself;
- variables: `dev/site.env` (git-ignored, see `dev/site.env.example`), loaded by `make dev-api`
  and winning over the file;
- secrets: `env:`/`file:` references or plain variables, or `make dev-api-op`, which runs
  `make dev-api` through a secret broker with the interface of 1Password's `op run`. Set `HL_OP`
  to the broker (`HL_OP=op make dev-api-op`; there is no default) and copy
  `dev/secrets.env.example` to `dev/secrets.env` (git-ignored) with your secret references.

`DASH_DEV=true` signs every request in as `dev` without authentication, so dash refuses to
start unless `server.listen` (`DASH_SERVER_LISTEN`) is a loopback address (`127.0.0.1:8080`,
`[::1]:8080`, `localhost:8080`). Since v0.9.0 it no longer sends the plugins' upstream URLs
through the API server. To reach in-cluster services such as
`http://prometheus.monitoring.svc:9090` from a laptop, set
`DASH_PLUGINS_KUBERNETES_UPSTREAM_VIA_API_SERVER=true` (`plugins.kubernetes.upstreamViaAPIServer`;
both dev examples set it, and the service proxy needs `get` and `create` on `services/proxy`).

## Checks

`make check` runs what CI's `check` job runs: it regenerates the generated files and fails if they
differ from what is committed (the table below), lints the OpenAPI document with vacuum, checks
the relative links of the docs and their anchors (lychee, offline), checks the Go and npm licences
against the policy, then runs `go vet`, `go test`, `tsc`, Biome, the web tests and the production
build.

`make scan` runs CI's `scan` job: gitleaks and trufflehog over every commit of every ref,
govulncheck, actionlint and zizmor on the workflows, and the web links of the docs (lychee, with
retries). Its binaries are pinned by sha256 in `scripts/ci/tools.sh`; made-up values (test
fixtures, doc examples) are allowlisted in `.gitleaks.toml` and `scripts/ci/trufflehog-allow.txt`,
and links never checked in `scripts/ci/lychee.toml`, each with a reason. Besides what the docs
link, the link check needs github.com (its first run downloads lychee).

Web tests run on Vitest with jsdom and Testing Library (`cd web && npm test`; `npx vitest` watches).
Tests sit next to the code as `*.test.ts(x)`. `web/src/test/harness.tsx` has the shared helpers:
`installEventSource()` (a fake EventSource you drive with `open()`, `emit(name, data)` and
`fail()`), `stubFetch(routes)` (answers `"GET /api/v1/x"` from a table and records calls),
`renderApp(ui, { path })` (QueryClient, router and tooltips, animations off) and the
`pluginInfo()` / `pluginsEnvelope()` builders. `setup.ts` stubs what jsdom lacks and removes the
fakes after each test, so a test never reaches the network.

## Generated files

These files are generated; never edit them by hand. `make generate` rewrites all of them and you
commit them with the change:

| File | Generated from | Rewrite after changing |
|---|---|---|
| `web/src/api/types.ts` | `internal/model` (tygo) | a struct in `internal/model` |
| `docs/configuration.md` | the config schema and `internal/config/configuration.intro.md` (`dash config env --markdown`) | a setting, its `doc` tag or its default, or the intro |
| `docs/api/openapi.json` | the route table and the types it names (`dash api openapi`) | an `/api/v1` route, a topic envelope, a plugin manifest or an error code |
| `docs/plugins/*.md` | each plugin's manifest and settings, the panel and RBAC tables in `cmd/dash/docs.go` and the prose in `cmd/dash/plugindocs/` (`dash docs plugins`) | a plugin's manifest, settings, panels, RBAC or prose |
| `charts/dash/files/config.schema.json` | the config schema (`dash config schema`) | a setting, its `doc` tag or its default |
| `charts/dash/files/secret-env.txt` | the secret settings (`dash config env`) | a secret setting |
| `examples/kubernetes/dash.yaml` | `helm template` of `charts/dash` with `examples/kubernetes/values.yaml` | the chart or those values |
| `THIRD_PARTY_NOTICES.md` | `tools/notices/header.md`, go-licenses and `web/scripts/licenses.mjs` | a Go or npm runtime dependency, or a vendored component |

A changed config schema also needs `go test ./internal/config -run TestConfigSchemaGolden -update`
for `internal/config/testdata/config.schema.json`.

A plugin that owns payload types has its own `model` package (`internal/plugins/<id>/model`: proxmox and hardware have one), its own entry in `tygo.yaml` and a generated `web/src/plugins/modules/<id>/model.gen.ts`, which `make check` also diffs. Such a package is an import leaf: it imports only the standard library (`TestPluginModelPackagesAreImportLeaves`), so a payload that needs a core type such as `Link` or `Health` stays in `internal/model`; `tygo.yaml` has the recipe for the other case.

A new plugin starts from the contract in `internal/plugin/plugin.go`; `internal/plugins/grafana`
is the small example. Fixtures, docs and commits use neutral names (`example.com`, `192.0.2.0/24`,
alice and bob): `make check` fails on a lab identifier through the denylist in `internal/hygiene`.

### npm overrides

`web/package.json` overrides `glob` (`^14.0.3`) and `minimatch` (`^11.0.4`) under
`license-checker-rseidelsohn` and `shadcn` only, for GHSA-3g3m-2fjr-6pw4 and GHSA-g3hv-75wg-m249.
Both are dev tools; nothing of them ships in `web/dist`. Their releases still pin older majors
(through `@npmcli/arborist`, `@npmcli/package-json` and `ts-morph`), so `npm update` cannot reach
the fixes. Remove each override once a release of its parent allows the patched major, then run
`npm install` so the lockfile follows.

## Layout

| Path | What |
|---|---|
| `cmd/dash` | entry point, listeners, shutdown |
| `internal/model` | the API contract (Go structs → `web/src/api/types.ts` via `make generate`) |
| `internal/config` | layered configuration: defaults, config file, environment (`DASH_*`) |
| `internal/jsonschema` | JSON Schema from Go structs, for `dash config schema` and plugin config |
| `internal/plugin` | the plugin contract, registry and supervisor; `plugintest` has the fakes |
| `internal/plugins` | the plugins dash ships, one package per plugin ID: `proxmox`, `hardware`, `kubernetes`, `flux`, `cnpg`, `rustfs`, `prometheus`, `alertmanager`, `gatus`, `victorialogs`, `grafana`, `headlamp` and `launcher`; `internal/plugins/<id>/model` holds the payload types only that plugin publishes |
| `internal/svc` | the interfaces plugins export to each other and to the core: `Querier`, `EndpointHealth`, `NodeName` (proxmox), `NodeIndex` and `Cluster` (the kubernetes plugin) |
| `internal/pluginconf` | the configuration structs of the plugins, shared by `internal/config` and the plugins (which never import `internal/config`) |
| `internal/secret` | secret values (`[redacted]` when printed), the `env:` and `file:` references, the scrub registry and `K8sRefs`, which hands the kubernetes plugin's `k8s:` references one holder each |
| `internal/auth` | who a request acts as: role and principal |
| `internal/kube` | the kubernetes plugin's client: informers, discovery and the patches |
| `internal/upstream` | VictoriaMetrics, Alertmanager, VictoriaLogs, Gatus, Proxmox clients |
| `internal/overview` | the overview's composer: `core.overview` is built from what the running plugins contribute (attention items, KPIs, topology), in live and demo mode alike |
| `internal/hub`, `internal/api` | SSE hub, REST, middleware |
| `internal/obs` | dash's own Prometheus metrics (`server.metrics`), in a private registry |
| `internal/hygiene` | repository-wide guards that belong to no package (`TestNoLabIdentifiers`: no lab identifier in any tracked file) |
| `internal/demo` | demo mode: `internal/demo/world` is the fake homelab the plugins' demo twins publish from (the demo's user is the fixed principal `auth.Fixed` builds in `cmd/dash`) |
| `web/` | React 19, Vite, Tailwind v4, shadcn/ui and Magic UI |
| `web/src/plugins` | the SPA's plugin modules, registry, slots and generic panels; a module's directory name is its plugin ID |
| `web/sw` | the service worker, hand-written; `web/vite-plugins/service-worker.ts` emits it as `dist/sw.js` with the precache list and the build hash |

## Screenshots

The images in [`docs/images`](images) come from demo mode: `make screenshots` builds the SPA
and dash, starts dash with `DASH_DEMO=true` and a fixed clock (`DASH_DEMO_CLOCK`, the Makefile's
`SCREENSHOT_CLOCK`) on free loopback ports, and captures every page the README shows, plus the
banner, with Playwright (`web/scripts/screenshots.mjs`; the banner's layout is
`web/scripts/screenshots/banner.html`). The browser's clock, viewport, timezone and locale are
fixed, motion is reduced, no request leaves dash, and a page drawn with any font the SPA does not
bundle fails the run, so two runs on one machine write the same bytes. Playwright's Chromium is
a one-time download; install `web/node_modules` first, or `npx` fetches the latest Playwright
instead of the pinned one:

```sh
cd web && npm ci --ignore-scripts && npx playwright install chromium && cd ..
make screenshots    # rewrites docs/images/*.png
```

Run it after a UI change those pages show, and commit the images with the change.

The PWA icons in `web/public/icons` (192 and 512 px, a maskable 512 and the 180 px
apple-touch-icon) are rendered the same way from the favicon glyph in `web/index.html`, so the
home-screen icon and the tab icon stay one design: `make icons` (`web/scripts/icons.mjs`) rewrites
them. Run it after changing the favicon, and commit the PNGs with it.

## PWA smoke

`make pwa-smoke` checks the installable app ([docs/app.md](app.md)) in a real browser: it builds
the SPA and dash, starts dash with `DASH_DEMO=true` on free loopback ports behind a loopback
proxy that counts the requests reaching dash, and drives Playwright's full Chromium
(`channel: "chromium"`, a temporary profile: the headless shell and an incognito context cannot
install) through `web/scripts/pwa-smoke.mjs`. It fails unless CDP `Page.getInstallabilityErrors`
is empty, the worker controls the page, an `/auth/callback?code=…` navigation reaches dash exactly
once and not through the worker, Cache Storage holds exactly the worker's precache list and no
`/api`, `/oauth` or `/mcp` URL after a session (the overview with its stream, a plugin page, the
alerts), an offline reload shows the offline page from the worker with its CSS applied, and no
document or worker reports a CSP violation. A run ends after 120 s at most. It needs the same
one-time setup as the screenshots and is not part of `make check`: run it after a change to
`web/sw`, `web/public`, the worker's plugin or the headers of `internal/api/static.go`.

## Context smoke

`make context-smoke` checks "Copy context for AI" in a real browser: it builds the SPA and dash,
starts dash with `DASH_DEMO=true` on free loopback ports, and drives Playwright's headless Chromium
with the clipboard permissions granted through `web/scripts/context-smoke.mjs`. It opens the first
pod's sheet, waits until the page is quiet and clicks the button. It fails unless the clipboard
holds the pod's context bundle as Markdown, its lab data in a closed fence after the untrusted-data
sentence, `GET /api/v1/context` is the only request the click made, and the toast names the pod.
Then, on a page with `navigator.clipboard` and `ClipboardItem` removed, as on a page served over
plain http, it fails unless the click opens a dialog holding the bundle, read-only and selected,
and the dialog's Copy button puts it on the clipboard.
A run ends after 60 s at most. It needs the same one-time setup as the screenshots and is not part
of `make check`: run it after a change to the button, `web/src/lib/clipboard.ts` or the context
route.

## Pre-push hook (opt-in)

CI runs on pushes to `main` and tags, and on pull requests once the repository is public; until
then `.githooks/pre-push` is the pre-merge gate. It diffs the pushed commits against the remote (a
new branch against its merge-base with `origin/main`) and checks only what changed, usually in a
few seconds:

- Go files: `go vet` and `go test` on the changed packages (`./...` if `go.mod`/`go.sum` changed
  or a package was deleted). Packages that import a changed package are not re-tested.
- `internal/model`, `tygo.yaml` or `web/src/api/types.ts`: the `make generate` drift check.
- `docs/api/`, `tools/`, `cmd/dash/openapi.go`, `cmd/dash/plugins.go` or the Go files in
  `internal/api`, `internal/plugin`, `internal/model`, `internal/jsonschema` or
  `internal/overview`: `docs/api/openapi.json` must equal `dash api openapi`, and vacuum must lint
  it.
- `docs/configuration.md` and `docs/plugins` have no step of their own. `go test` of
  `internal/config` and `cmd/dash` compares them, so a Go change in one of those packages checks
  them; a change elsewhere that feeds them (a plugin's settings or manifest, `cmd/dash/plugindocs`,
  `internal/config/configuration.intro.md`) is caught only by `make check`.
- `web/`: `tsc --noEmit`, Biome on the changed files (all of `web/` if its config, package or lock
  files changed) and `vitest run` whenever TypeScript or web config changed. Skipped with a hint
  if `web/node_modules` is missing (`npm ci --ignore-scripts`).
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

1. On a branch off `main`: `make set-version VERSION=X.Y.Z`. It moves the version in
   `charts/dash/Chart.yaml` (`version` and `appVersion`), the version pins of the README, the
   deploy docs, `examples/compose/compose.yaml` and the bug report form, and re-renders
   `examples/kubernetes` with `make generate`; prose that names a past version stays. Commit the
   result and merge it: the `release` job refuses a tag whose version differs from `Chart.yaml`.
2. On `main` with CI green: `git tag vX.Y.Z && git push origin vX.Y.Z`.
3. CI (`release` job) pushes the image `dash:X.Y.Z` to the release registry and prints
   `image: <registry>/dash/dash:X.Y.Z@sha256:<digest>` in the log and the job summary. Tags are
   create-only: a version can never be pushed twice.
4. Deploy that tag and digest wherever dash runs.
