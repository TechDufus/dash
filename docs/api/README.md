# HTTP API

The public API is under `/api/v1` (`me`, `stream`, `series`, `topics/<topic>`, `plugins`,
`settings`, `tokens`), described by [openapi.json](openapi.json), an OpenAPI 3.1 document generated from the
code (`dash api openapi`; `make generate` rewrites it and `make check` fails when it drifts or does
not lint with [ruleset.yaml](ruleset.yaml)). The web UI uses the same paths. Before 1.0 a breaking
change to them is called out in the release notes; the paths that moved under `/api/v1` are in
[upgrading.md](../upgrading.md#removed-api-paths).

The data is live, not polled from the browser: informers and upstream pollers rebuild each
plugin's topics, the core composes `core.overview` from what the plugins contribute, and only
what changed goes out over a single SSE stream, `/api/v1/stream`. `core.plugins` says which
plugins run and whether each is healthy.

Every response of the API listener carries an `X-Request-Id` (128 random bits as hex; a proxy on
the loopback address may supply its own), which also appears in the error body, the audit record
and the log line of an upstream failure. The health listener (`/healthz`, `/readyz`, `/metrics`)
sends none. `/api/v1` answers are `Cache-Control: no-store`, except a topic snapshot
(`private, no-cache` with an `ETag`) and `/api/v1/stream` (`no-cache`, as for any event stream).

## Bearer tokens

A program calls the API with `Authorization: Bearer <token>`: a personal access token
(Settings → Tokens, `/api/v1/tokens`) or a static token of `auth.tokens`
([auth.md](../auth.md#api-tokens)). The OpenAPI document declares it as `bearerAuth`; each
operation's `x-dash-scope` names the scope a token needs, and `x-dash-interactive: true` marks the
operations for people only, which refuse a token with 403 `forbidden`. A token request needs no
CSRF header, gets 300 requests a minute, and is refused with 401 `invalid_token` when the token is
unknown, expired, revoked or for the `mcp` audience. Browser clients on another origin need
`server.corsOrigins`.

## Plugin routes and actions

`GET /api/v1/plugins` lists every plugin instance with its state, topics, mounted actions and
navigation (the same rows arrive on the `core.plugins` stream topic). A running plugin's own
read routes live under `/api/v1/plugins/<id>/…`; each [plugin page](../plugins/README.md) lists
its routes, topics and series.

The framework mounts a plugin's actions as `POST /api/v1/plugins/<id>/actions/<name>` while
`plugins.<id>.actions.enabled` is true, and answers 404 otherwise. Three plugins have them: the
alertmanager plugin's `silence.create` and `silence.expire`, the kubernetes plugin's `restart` and
the flux plugin's `reconcile`, all for administrators and all answering 202. Changing an
`actions.enabled` restarts that plugin, and until it is set the web UI shows no such button.
`dash config validate` rejects `plugins.<id>.actions.*` on a plugin without actions.

Every action request runs one chain: the principal's role, a token's `actions:<plugin>` scope, a
rate limit of 10 actions a minute per user, the body checks (JSON only, unknown fields refused,
`required` and `enum` honoured), the namespace deny list where the plugin has one and a token's
`actionNamespaces`, the audit intent, the action, then the audit outcome. A refusal is audited and
the action does not run.

## Errors

Every error answer is `{"error": "<text for a person>", "code": "<reason>", "requestId": "<id>"}`;
a 401 `unauthenticated` from `/api` also carries `"details": {"mode": "proxy|local|oidc", "setup":
<bool>, "oidc": {"issuer": "<host>"}}`, so a client knows how to sign in, and every 401 from `/api`
carries `WWW-Authenticate: Bearer resource_metadata="…"`. Clients branch on `code`.
These are sent today: `unauthenticated`, `forbidden`, `csrf_failed`, `not_found`, `invalid_input`,
`invalid_token` (a bearer token that is unknown, expired, revoked or for another audience, and a
wrong or used setup token), `ambiguous_credentials` (400: a bearer token together with the
session cookie), `insufficient_scope` (403: a token without the route's scope), `rate_limited`
(the login backoff, plugin actions, connection tests and an api token past 300 requests a
minute), `conflict`, `tokens_unavailable` (409: no writable data dir keeps a new token),
`too_many_streams`, `upstream_unavailable` (502, or 503 when the OIDC provider cannot be reached),
`audit_unavailable`, `unknown_host`, and from the settings API `locked_field` (409),
`readonly_settings` (409), `precondition_failed` (412) and `precondition_required` (428);
`internal` comes from plugin action routes, a settings write that fails and a token the credential
store cannot write. `approval_required`, `approval_denied` and `approval_expired` are reserved for
the approval work that follows, and the OpenAPI document lists them all.

The sign-in routes (`POST /login`, `POST /setup`, `POST /auth/logout`, `GET /auth/oidc/start`,
`GET /auth/callback`) are outside `/api` and not in the OpenAPI document;
[auth.md](../auth.md#what-the-api-answers) describes them. The settings routes are in
[settings.md](../settings.md#routes).
