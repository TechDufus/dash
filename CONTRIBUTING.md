# Contributing to dash

dash is maintained by one person on a best-effort basis. Issues and pull requests are welcome;
this page says how a change gets in. [docs/development.md](docs/development.md) is the reference
for everything it only names.

## Before you start

- **A bug or an idea:** open an issue with the form that fits. For anything larger than a fix,
  open the issue first, so the design is agreed before the code is written.
- **A vulnerability:** never in an issue or a pull request. [SECURITY.md](SECURITY.md) says how
  to report it privately.
- **A decision that changes what dash connects to, stores or exposes** is recorded in
  [docs/adr](docs/adr/README.md), in the same pull request as the code.

## Set up

Tools are pinned in `mise.toml`: `mise install` gets Go, Node, Helm and the rest. Nothing else
is needed, not even a cluster:

```sh
DASH_DEMO=true make dev-api   # the Go API on 127.0.0.1:8080, on demo data
make dev-web                  # Vite on http://localhost:5173
```

## Make the change

- **Reuse what is there.** Read the neighbours of the code you touch and follow them. A second
  way of doing something needs a reason.
- **Generated files** (the TypeScript types, the configuration reference, the OpenAPI document,
  the plugin pages, the chart's schema, the notices) are never edited by hand: `make generate`
  rewrites them, and you commit the result with the change.
- **Neutral names only.** Fixtures, docs, examples and commit messages use `example.com`,
  `192.0.2.0/24`, alice and bob. `make check` fails on an identifier of a real lab.
- **Messages never repeat a value.** An error shown to a user says which setting is wrong, not
  what it holds, and never passes on text an upstream sent.
- **Tests cover behaviour:** boundaries, failures, what a consumer can see. Do not weaken an
  assertion to make a change pass, and do not pin wording or implementation details.
- **Docs change with behaviour.** A setting, a route, a plugin's page or an action that changes
  has its documentation changed in the same pull request. If a page the README shows looks
  different afterwards, run `make screenshots` and commit the images.

## Check

```sh
make check   # what CI's check job runs: generated files, licences, vet, tests, the web build
make hooks   # optional: a pre-push hook that checks only what you changed
```

`make scan` (secrets, vulnerabilities, workflows, links) and `make demo` (the static demo the
landing page hosts) are what CI's `scan` and `demo` jobs run.

## Commits and pull requests

- [Conventional commits](https://www.conventionalcommits.org/): `type(scope): summary`, with a
  body that says why the change is needed and what it does. Look at `git log` for the tone.
- Commits are signed.
- One coherent change per pull request, with its tests and docs. Fill in the template.
- CI must be green. The maintainer merges by fast-forward, so keep the branch rebased on `main`.
- Release notes: add what a user will notice under `## Unreleased` in
  [CHANGELOG.md](CHANGELOG.md). Versions and tags are the maintainer's.

## Adding a plugin

A plugin is a Go package under `internal/plugins/<id>` and, when it has pages of its own, a
module under `web/src/plugins/modules/<id>`. The contract is `internal/plugin/plugin.go`;
`internal/plugins/grafana` is the smallest example and `internal/plugins/gatus` the smallest
that polls a service. In order:

1. **Settings:** a struct in `internal/pluginconf/<id>.go` that embeds `PluginBase`, with `doc`
   tags, defaults and `Validate`, and its field in `internal/config/tree.go`. A secret is a
   `secret.Value` tagged `dash:"secret"`.
2. **Payloads:** the types the plugin publishes. Types only this plugin uses go in
   `internal/plugins/<id>/model` (standard library imports only) with an entry in `tygo.yaml`;
   every object in a payload carries a `ref` (`<id>:<Kind>/<name>`). Add the topic constant and
   its type to `internal/model`.
3. **The plugin:** its manifest, `Start`, `Stop` and `Status`, and whichever optional interfaces
   it needs (`Configured`, `Validator`, `Router`, `Actor`, `AttentionSource`, `Objects`).
   Pollers run through `Host.Go`; upstreams are reached through `Host.HTTPClient` or
   `Host.Dial`, which carry the dial guard.
4. **A demo twin** (`Demo()`), publishing made-up data from `internal/demo/world` as a pure
   function of the clock. A plugin without one does not exist in demo mode, in the screenshots
   or in the live demo.
5. **Registration:** `cmd/dash/plugins.go`, and the module in `web/src/plugins/registry.ts`.
   Several tests pin the list of plugins on purpose; they tell you what else to update.
6. **Docs:** the prose in `cmd/dash/plugindocs/<id>.md`; `make generate` builds the plugin's
   page, the configuration reference and the OpenAPI document from the code.
7. **Actions,** if any, are off by default, administrators only and audited: read
   [the security model](docs/security.md#actions-and-guardrails) first. A plugin that writes
   somewhere new needs an ADR.
