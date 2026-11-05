# Security model

dash reads a lot (the cluster, the hypervisor, monitoring) and writes very little. This page says
who may do what, how requests are checked, how secrets are kept and what is recorded. To report a
vulnerability, see [SECURITY.md](../SECURITY.md).

## Who is signed in, and what they may do

`auth.mode` decides how dash learns who makes a request: `local` (one administrator with a
password, the default), `oidc` (an OpenID Connect provider and an allow-list), `proxy` (an
identity proxy forwards the user name, believed only from `auth.proxy.trustedProxies`) or `none`
(everyone is an administrator; it refuses a non-loopback listener unless `auth.none.allowRemote`
is set). [auth.md](auth.md) describes each mode.

There are two roles. A **viewer** reads dashboards, streams and logs. An **administrator** also
runs actions and changes settings. Roles always come from dash's configuration (the admin user,
group and role lists), never from the credential, so a change to those lists applies on the next
request, except that in `oidc` a group or role value newly added to `adminGroups` or `adminRoles`
applies at the user's next login. With `proxy` and both admin lists empty, everyone is a viewer.

`DASH_DEV=true` signs every request in as `dev` and starts only on a loopback address;
`DASH_DEMO=true` serves made-up data and contacts nothing. Neither is for deployment.

## Sessions

The `local` and `oidc` modes set a session cookie: `__Host-dash_session` over https (with
`Secure`), always `HttpOnly`, `SameSite=Lax`, `Path=/`, with no `Domain`. Its content is sealed
with XChaCha20-Poly1305 and holds no role. With `DASH_SECRET_KEY` set and a writable data
directory, the key is derived from it and a salt there, so sessions survive a restart; otherwise
(no key, or no writable data directory) the key is random per process. A session lasts 12 hours
from its last renewal and at most 7 days. Logging out ends the user's sessions on every device;
an administrator can end everyone's. In the local mode failed logins back off per address and
per account, up to 15 minutes. Details: [Sessions](auth.md#sessions).

## API tokens

Programs authenticate with a bearer token: a personal access token (Settings → Tokens) or a
static token of `auth.tokens`. dash stores only SHA-256 hashes, in `credentials.yaml` (mode 0600),
and compares them in constant time. dash keeps no `.bak` copy of the file; restoring the data
volume brings back revoked tokens, so revoke them again after a restore. The token's secret is
never logged, audited or shown again. Tokens are bound to one audience (`api`
or `mcp`), narrowed by scopes (`read`, `logs`, `actions:<plugin>` for administrators; never
`approvals` or `push`) and to an expiry of at most a year, and the role is derived on every request
from the owner's recorded groups and the current admin lists. Settings and token management refuse
tokens (403). Revocation applies to the next request and ends open streams within a heartbeat.
Details: [API tokens](auth.md#api-tokens).

## OAuth

With `auth.oauth.enabled` (off by default) dash is an OAuth 2.1 authorization server for public
clients ([auth.md](auth.md#oauth)). The authorization code flow requires PKCE S256 and a
`resource`, and returns `iss` with the code; a redirect URI must match one the client registered
exactly (an http loopback one on any port), and an error is never sent to an unverified one.
Only the built-in `dash-android` client may be granted `approvals` or `push`; any other client
asking for them gets `invalid_scope`. Consent is always asked, interactive-only and CSRF-checked.
Codes live 60 seconds and work once. Access tokens are sealed (XChaCha20-Poly1305, under a key
from `DASH_SECRET_KEY` and the store's salt), live 15 minutes, and each use re-checks the grant
and derives the role again. Refresh tokens are stored as hashes and rotate; a rotated one used
again, a code used twice, or a failed PKCE check revokes the grant. Grants are capped at 20 a
user and end after 30 idle or 180 days; approving and revoking them is audited fail-closed.

A client ID metadata document is fetched by a client that dials only global addresses (checked
after name resolution, so a rebinding name is refused too), uses no proxy from the environment,
follows no redirect and reads at most 64 KiB within 5 seconds. `auth.oauth.cimdAllowPrivate` adds
private and loopback addresses, never link-local ones. `/oauth/token` and `/oauth/revoke` need no
sign-in and no CSRF header (they read no cookie), and are limited to 60 requests a minute per
grant (a refresh), else per authorization code dash holds (an exchange), else per known client
and peer, else per peer; the limit's table forgets its oldest key rather than refuse a new one.
`/oauth/authorize` keeps nothing and fetches no client
metadata document for a caller who is not signed in; a person holds at most 20 pending requests,
and 1024 in all. With `server.directURL`, `/oauth/authorize` and `/consent` are 404 on the direct
host. Behind an identity proxy,
`server.directURL` names a second origin for these endpoints; the proxy's trusted peers stay
loopback, so nothing arriving on the direct host is signed in by a header.

## Requests

- **CSRF and Origin.** Every request other than `GET` and `HEAD` needs the header
  `X-Dash-Csrf: 1` and an `Origin` equal to the origin of `server.publicURL`; anything else is
  403 `csrf_failed` (a wrong `Host` is 421 and an unauthenticated `/api` request 401 first). A
  browser page on another site can send neither. A request dash resolved to an API token is
  exempt (a browser never attaches a bearer token on its own); a `Bearer` header that resolves to
  nothing, as in `auth.mode=none`, exempts nothing.
- **CORS.** None by default. `server.corsOrigins` lets listed origins call the API with a bearer
  token, and read the OAuth metadata, token and revoke endpoints, never with cookies
  (`Access-Control-Allow-Credentials` is never sent) and never `/oauth/authorize`.
- **The Host allow-list.** The API listener answers only the hosts of `server.publicURL` and
  `server.directURL` and the names in `server.allowedHosts`; any other `Host` gets 421
  `unknown_host`. This stops DNS rebinding: a hostile page whose name resolves to dash's address
  cannot read dash's answers.
  `server.allowedHosts: ["*"]` turns the check off. Dev and demo mode also accept `localhost`,
  `127.0.0.1` and `::1`.
- **Content-Security-Policy.** The web UI's document gets `default-src 'self'`, scripts only from
  dash, styles from dash plus a per-response nonce, images from dash, `data:` and the
  `ui.iconBaseURL` path only, no framing (`frame-ancestors 'none'`), `base-uri 'none'`,
  `object-src 'none'` and `form-action 'self'`. The service worker (`/sw.js`) runs under
  `default-src 'none'; connect-src 'self'`: it may fetch dash and nothing else. The offline page
  (`/offline.html`), which the worker keeps and so cannot carry a per-response nonce, runs no
  script, allows its inline CSS by hash and loads only dash's own icon. Every other response of
  the API listener gets `default-src 'none'; frame-ancestors 'none'`. `server.csp` is `enforce`
  (the default), `report-only` or `off`, the kill switch if a browser blanks the UI; any other
  value is refused when the configuration loads. The worker's and the offline page's policies are
  enforced in every mode: they allow dash's own origin only and cannot blank the UI, and a worker
  keeps the policy it was installed with until a release changes it, so a relaxed mode would
  outlive the switch back. `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`
  are sent in every mode.
- **Caching.** The sign-in routes and every answer that sets a cookie are `Cache-Control:
  no-store`, and so is `/api/v1` except topic snapshots and streams.
- **Limits.** Open streams and log tails are capped per user and in all, a tail ends after
  `server.limits.tailMaxDuration`, and slow request bodies, slow stream writes and large headers
  are cut off (`server.limits.*` in the [configuration reference](configuration.md#serverlimits)).
- **Errors.** A failing upstream is reported to viewers by category only (`unreachable`,
  `timeout`, `tls`, `auth`, `http_<code>`, `invalid_response`, `not_configured`); URLs and
  details stay in dash's log.

The health listener (`/healthz`, `/readyz`, and `/metrics` when `server.metrics` is on) is
unauthenticated. `/readyz?verbose=1` lists plugin states only to a loopback peer, but `/metrics`
names plugins and their states to anyone who reaches it: keep that listener cluster-internal.

## Upstream access

Each HTTP plugin (prometheus, alertmanager, victorialogs, gatus, proxmox) reaches its service
through a client dash builds for it: TLS is verified against the system roots plus an optional
`tls.caFile` (`tls.insecureSkipVerify` turns verification off), redirects stay on the same host
and port (and never go from https to http), and the environment's proxy is not used. A dial
guard on that client refuses link-local addresses (169.254.0.0/16, fe80::/10, fd00:ec2::254),
which include cloud metadata services, whatever the configuration; a URL saved from the settings
page may not reach loopback either. The kubernetes client (also used by flux and cnpg) is
client-go's: it trusts the kubeconfig's or service account's CA, honours `HTTPS_PROXY`/`NO_PROXY`,
and has no dial guard. The `oidc` mode's client for the provider (discovery, token exchange,
signing keys) also uses the environment's proxy and has no dial guard.

## Secrets

- **References.** A secret setting takes a literal, `env:NAME`, `file:/path` or
  `k8s:<namespace>/<name>#<key>` (a key of a Kubernetes Secret, read by the kubernetes plugin with
  RBAC limited to the named Secrets; see the [kubernetes plugin](plugins/kubernetes.md#kubernetes-rbac)).
  A literal in the config file logs a warning, because such files often live in git. A rotated
  `file:` secret applies without a restart.
- **The secret key.** Secrets saved from the settings page are stored encrypted in the data
  directory (XChaCha20-Poly1305, bound to the field they belong to) under `DASH_SECRET_KEY`, which
  comes from the environment only; dash never generates one, and without it the settings page
  cannot save secrets. `DASH_SECRET_KEY_PREVIOUS` rotates it. A stored value dash cannot decrypt
  stops it from starting; it never drops or regenerates one.
- **Redaction.** dash scrubs every secret it has read (and, for secrets of 8 bytes or more, their
  base64 and URL-encoded forms) and credential-shaped tokens from its log, from error messages
  and from the pod logs the UI shows.
  The settings API shows whether a secret is set and from where, never its value, and its errors
  never repeat a value ([settings.md](settings.md)).

## Audit

Every action, settings change, silence change and stored-secret change is recorded as an `audit`
log record, and appended to `audit.jsonl` in the data directory when it is writable: who, what,
the target and the outcome, never a secret or a changed value. A token's request names the token
(`actor.tokenID`); creating or revoking a token is audited like any change. Refused requests (CSRF,
unauthenticated, an invalid token, a missing scope, stream caps, a denied namespace) are recorded
too, at most once per user (or, before sign-in, per peer) and reason a minute.
Each line carries the hash of the one before, and `dash audit verify` checks the chain. The
chain proves the history was not rewritten, not that it is complete: dropping the first line,
or editing or dropping a last line that is not an intent, still verifies.
A change is refused when its record cannot be written first (503 `audit_unavailable`).

## Actions and guardrails

dash writes in three places only: a rollout restart of a workload (the
[kubernetes plugin](plugins/kubernetes.md#actions)), a Flux reconcile request
([flux](plugins/flux.md#actions)) and Alertmanager silences that expire
([alertmanager](plugins/alertmanager.md#actions)). Each is off until its
`plugins.<id>.actions.enabled` is set (demo mode mounts them against its fake data), and every
run is limited the same way:

- administrators only;
- 10 actions a minute per user, across every plugin;
- a strict JSON body;
- `actions.denyNamespaces` for the restart and reconcile actions, and
  `plugins.alertmanager.protectedAlerts` for silences;
- a typed name for a restart in a namespace the protected label marks;
- an audit record before and after.

On Kubernetes, the [Helm chart](deploy/kubernetes.md#rbac-modes) grants `patch` only for an action
you turn on, and its [guardrail](deploy/kubernetes.md#the-guardrail), a ValidatingAdmissionPolicy,
holds dash's ServiceAccount to exactly the annotation each action sets, whatever RBAC allows.

## No lab identifiers

dash grew up in one homelab, and nothing of it ships. `make check` runs a repository-wide
denylist (`internal/hygiene`) that fails on any identifier of that lab in a tracked file; each
exception names its term and reason, and its file unless it applies everywhere (such as the
project's own repository URLs). Fixtures, docs and examples use neutral names
(`example.com`, `192.0.2.0/24`, alice and bob). `make scan` searches every commit for secrets
(gitleaks, trufflehog), checks the Go code for known vulnerabilities (govulncheck) and the
workflows for unsafe patterns (actionlint, zizmor).

## MCP server

The read-only [MCP server](mcp.md) is off until `mcp.enabled` is set, answers 404 while it is off,
and never runs in `auth.mode: none`. It accepts only a bearer token of the `mcp` audience, which in
turn is refused at `/api/v1`; a present `Origin` must be the public URL's or a listed CORS origin.
`tools/list` shows only what the token's scopes allow, `dash_logs` and log lines in bundles need
`logs` and keep `plugins.victorialogs.denyNamespaces`, and no tool returns a Secret or ConfigMap.
Lab-sourced text sits in a fenced block, longer than any backtick run inside it, after the
untrusted-data sentence; every string is scrubbed before it is encoded; results are capped at
64 KiB. Each token has its rate and concurrency limits, and every call is audited with surface
`mcp` and the SHA-256 of its arguments, never the arguments.

## Not built yet

These features are designed and planned, and **none of them exists in any release**: human
approval of agent actions (agents can only read today) and push notifications. Each will be
documented here when it ships. [SECURITY.md](../SECURITY.md#planned-not-yet-built) lists them in
the reporting scope.
