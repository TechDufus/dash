# Security model

dash reads a lot (the cluster, the hypervisor, the hosts, monitoring) and writes very little. This
page says who may do what, how requests are checked, how secrets are kept and what is recorded.
To report a vulnerability, see [SECURITY.md](../SECURITY.md).

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
  `default-src 'none'; connect-src 'self'; img-src 'self'`: it may fetch dash and nothing else,
  and its notifications show dash's own icon. The offline page
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

## Hosts over SSH

The [ssh plugin](plugins/ssh.md) reads Linux hosts and installs nothing on them
([ADR 0003](adr/0003-ssh-hosts-binaries-static-demo.md)).

- **What runs on a host.** One command per connection, in a session without a terminal: a script
  that is a constant in dash's source and prints the text of `/proc`, `/sys` and a few read-only
  commands. The only values dash puts into it are numbers it computes and a random marker. No
  setting and nothing a host sends becomes part of a command, and dash has no action, route or
  setting that runs anything else there. It never writes to a host and never asks for more
  privilege.
- **dash's key.** dash signs in with one ed25519 key and nothing else; it stores and asks for no
  password. The private key is generated at first need and sealed in `credentials.yaml` with
  `DASH_SECRET_KEY`, so the plugin needs the secret key and a writable data directory and does
  not start without them. Whoever holds both the data directory and the secret key holds a shell
  as the configured user on every host: give dash a dedicated unprivileged user there, and add
  its key with the `restrict` option, which turns off forwarding and terminals.
- **Host keys.** A host's public key is pinned in its settings (`plugins.ssh.hosts[].hostKey`).
  To a host without one dash sends no credential: it reads the key the host presents, ends the
  handshake before it authenticates and shows the fingerprint. Trusting that key is an
  administrator's settings change, with its audit record. A host that presents another key than
  the pinned one is refused until an administrator replaces the pin.
- **Where it connects.** Through the dial guard of the HTTP plugins: link-local addresses are
  refused whatever the configuration, an address saved from the settings page may not reach
  loopback, and the environment's proxy is not used.
- **What a host sends.** Text, which dash parses and never runs. A frame, a line and every list
  are bounded, and a host that breaks the framing is disconnected. What it reports (names of
  containers, units and file systems) is lab text like a pod's name: an agent gets it inside the
  untrusted-data framing.
- **What viewers see.** Names, states in fixed words, fingerprints, the inventory and the
  metrics. Addresses, users and the text of connection errors stay in the settings API, which
  only administrators read, and in dash's log.
- **What dash can read.** Whatever the SSH user can. Listing containers needs access to the
  container runtime, and membership of the `docker` group is as good as root on that host: leave
  it out unless you accept that, and the Hosts page simply shows no containers.

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

The [MCP server](mcp.md) is off until `mcp.enabled` is set, answers 404 while it is off,
and never runs in `auth.mode: none`. It accepts only a bearer token of the `mcp` audience, which in
turn is refused at `/api/v1`; a present `Origin` must be the public URL's or a listed CORS origin.
`tools/list` shows only what the token's scopes allow, `dash_logs` and log lines in bundles need
`logs` and keep `plugins.victorialogs.denyNamespaces`, and no tool returns a Secret or ConfigMap.
Lab-sourced text sits in a fenced block, longer than any backtick run inside it, after the
untrusted-data sentence; every string is scrubbed before it is encoded; results are capped at
64 KiB. Each token has its rate and concurrency limits, and every call is audited with surface
`mcp` and the SHA-256 of its arguments, never the arguments.

## Agent actions and approvals

An agent's action tools exist only for an administrator's `mcp` token holding `actions:<plugin>`
while the plugin's actions are enabled and its agent policy (`plugins.<id>.actions.agents`) is not
`deny`. Under the default, `approve`, a call runs nothing: it becomes an approval request that
only the token's owner can see and decide, from a browser session (CSRF-checked) or with the
`approvals` scope, which only the first-party `dash-android` grant can hold. Anyone else gets 403.
A push or a link never approves anything: the decision is a POST from the page.

- **Typed names stay human.** dash drops the `confirm` an agent sends; a target that needs its
  name typed waits for a person even under `allow`, and only the name that person types reaches
  the action.
- **Checked twice.** The call is checked before a request exists (role, scope, input, deny lists,
  `actionNamespaces`), and again when it is approved, against the token or grant as it is then:
  revoked, expired, demoted, its scope gone or a plugin now `deny` ends the request denied.
- **Once.** A request is decided once; a concurrent or later decision is 409, so an approval runs
  the action exactly once.
- **Audited, fail-closed.** The decision and the run each write an intent before anything happens
  and an outcome after; without the intent the decision is 503 and nothing runs. Records hold the
  target and the input's SHA-256, never its values.
- **Bounded.** 10 action calls a minute per token, 20 pending requests per user, 10 minutes to
  decide; requests live in memory and a restart forgets them. The URL-elicitation
  `requestState` is signed with a per-process key and bound to the token and the tool.

## Push notifications

Push needs a writable data dir and `DASH_SECRET_KEY`, and is off in `auth.mode=none`, dev and
demo. Each person registers their own devices; a device receives only a verification code until
that code comes back. Besides a signed-in person, only the first-party `dash-android` OAuth grant
holds the `push` scope; personal access tokens and static tokens never do. A browser's device
follows its owner's sessions, and a device the app registered follows its grant: revoking or
expiring the grant stops it. The VAPID private key and each device's endpoint and keys are sealed
with `DASH_SECRET_KEY`; an endpoint is never logged or audited, only its host. Payloads are lab
text, never secrets, end-to-end encrypted (RFC 8291), and dash posts only to global https
endpoints unless `push.allowPrivate` admits a LAN one. The user's role is checked again when an
attention push is sent. Details: [push.md](push.md).
