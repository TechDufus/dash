# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/TechDufus/dash/security/advisories/new)
(the repository's **Security and quality** tab → **Report a vulnerability**). Do not open a public
issue, pull request or discussion for a vulnerability.

A useful report names the version (the image tag or release, or `version` in `/api/v1/me`), the
deployment (binary, Docker, Compose or the Helm chart), the `auth.mode` in use, the steps to
reproduce and what an attacker gains. A proof of concept against your own installation is
welcome; never test against an installation you do not run.

What happens next (dash is maintained by one person on a best-effort basis):

- the report is acknowledged as soon as possible;
- the maintainer confirms or rejects it and agrees on a fix and a disclosure date with you through the
  advisory;
- the fix ships as a patch release of the supported minor, and the advisory is published with it
  (credit to you unless you prefer otherwise).

## Supported versions

dash is 0.x: until the plugin API is stable, every minor release may change behaviour. Only the
latest minor release receives security fixes, as patch releases.

| Version | Supported |
|---|---|
| latest 0.x minor (0.14 today) | yes |
| any older minor | no: upgrade to the latest |

## Scope

In scope is the code in this repository and what it ships: the `dash` binary and image, the web UI
and the Helm chart in `charts/dash`. [docs/security.md](docs/security.md) describes the security
model these protections make up. In particular:

- **The API:** the REST routes under `/api/v1`, the server-sent event streams and log tails, the
  health listener (`/healthz`, `/readyz`, `/metrics`), request limits, the allowed hosts and Origin
  checks, and the Content-Security-Policy and other headers.
- **Sessions and auth modes:** the `local`, `oidc`, `proxy` and `none` modes (see
  [docs/auth.md](docs/auth.md)), the session cookie, login and logout, the setup token, the trust
  in `auth.proxy.trustedProxies`, and the viewer and administrator roles: a viewer reaching an
  administrator's action, or anyone reaching dash as someone else, is a vulnerability.
- **API tokens:** personal access tokens and `auth.tokens`, their scopes, audiences and
  revocation, the CSRF exemption of a bearer request and `server.corsOrigins`.
- **The OAuth authorization server** (`auth.oauth`, since v0.14.7): `/oauth/authorize`,
  `/oauth/token`, `/oauth/revoke` and the `/.well-known/oauth-*` documents, PKCE and the
  redirect URI match, the consent page and Settings → Connected apps, the access and refresh
  tokens and the grants behind them, the fetch of client ID metadata documents and its address
  guard (`auth.oauth.cimdAllowPrivate`), and `server.directURL`.
- **Settings and secrets:** the settings store and the settings page, the encryption of secrets
  saved from the UI (`DASH_SECRET_KEY` and its rotation), the `env:`, `file:` and `k8s:` secret
  references, redaction of secrets in logs, API answers and the audit log, and the audit log
  itself.
- **The plugins' upstream access:** how the plugins reach Kubernetes, Proxmox, Prometheus,
  Alertmanager, VictoriaLogs, Gatus and the others: credentials, TLS verification, the service
  proxy (`plugins.kubernetes.upstreamViaAPIServer`), the actions and their limits
  (`plugins.kubernetes.actions.denyNamespaces`, `plugins.flux.actions.denyNamespaces`,
  `plugins.alertmanager.protectedAlerts`) and `plugins.victorialogs.denyNamespaces`, and anything
  that lets a user make dash send requests the configuration did not intend.
- **The Helm chart's RBAC and guardrail:** the ClusterRole and Roles the chart grants, the
  ValidatingAdmissionPolicy that holds dash's ServiceAccount to what its actions do (see
  [docs/deploy/kubernetes.md](docs/deploy/kubernetes.md#the-guardrail)), and the pod's security
  context.

### Planned, not yet built

These features are designed (ADR 0002 and `docs/design/clients-and-ai.md`) and planned for a later
release. **None of them exists in any release yet**, so there is nothing to report against them
today. Each enters the scope above in the release that ships it:

| Feature | Status |
|---|---|
| An MCP server for AI agents | planned, not built |
| Human approval of agent actions | planned, not built |
| Push notifications (Web Push and UnifiedPush) | planned, not built |

## Out of scope

- **Dev and demo mode.** `DASH_DEV=true` signs every request in as `dev` without authentication
  and only starts on a loopback address; `DASH_DEMO=true` serves fake data. Both are for
  development, not deployment. A way to make dev mode start on a non-loopback address, or demo
  mode reach a real upstream or secret, is in scope.
- **Choices the operator makes explicitly:** `auth.mode: none` (everyone is an administrator,
  by design), `auth.none.allowRemote`, `plugins.<id>.tls.insecureSkipVerify`, the Helm value
  `guardrails.enabled: false`, `server.allowedHosts: ["*"]` and `server.csp: "off"`, each
  documented with its risk.
- **What an administrator can already do,** such as pointing a plugin at any URL or reading the
  configured upstreams: administrators are trusted.
- **The upstream services themselves** (Kubernetes, Proxmox, Prometheus and the others) and
  vulnerabilities in third-party dependencies that dash cannot reach: report those upstream. A
  dependency vulnerability that is reachable through dash is in scope.
- **The examples and docs** (`examples/`, `docs/`), whose placeholders are not meant for
  production as they stand, and the design documents under `docs/design`.
- Denial of service that needs an administrator's configuration or volumetric traffic, reports
  from automated scanners without a demonstrated impact, missing hardening without an exploit,
  and social engineering.
