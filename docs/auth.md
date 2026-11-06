# Signing in to dash

dash knows who makes each request and gives that person one of two roles: a **viewer** reads
everything (dashboards, streams, logs) and an **administrator** can also run actions (restart,
reconcile, silence). Roles always come from dash's configuration, never from the credential, so a
change to the admin lists applies on the next request.

`auth.mode` (`DASH_AUTH_MODE`) chooses how people sign in:

| Mode | Who is signed in | Use it when |
|---|---|---|
| `local` (the default) | one administrator account, with a password, at `/login` | dash is reachable only by you, and you want nothing else in front of it |
| `oidc` | users of an OpenID Connect provider, admitted by an allow-list | you have a provider (Keycloak, Authentik, Dex, ...) and want several users |
| `proxy` | the user an identity proxy in front of dash names in a header | dash already sits behind oauth2-proxy, Authelia or the like |
| `none` | everyone, as an administrator named `anonymous` | only on `127.0.0.1`, for a machine nobody else can reach |

`DASH_DEV` and `DASH_DEMO` outrank the mode and sign every request in as their fixed user.

**`none` needs a loopback listener.** Since v0.14.0 `server.listen` defaults to `0.0.0.0:8080`,
and `none` refuses to start there (as does `dash config validate`): set
`DASH_SERVER_LISTEN=127.0.0.1:8080`. In a container a loopback listener cannot be reached through
`-p`: on Linux use `--network host` with that loopback listener, or set
`DASH_AUTH_NONE_ALLOW_REMOTE=true` (`auth.none.allowRemote`), publish the port to the host's
loopback only (`-p 127.0.0.1:8080:8080`) and put dash on a Docker network of its own: every host
process, and every container on dash's network (it reaches port 8080 directly, past `-p`), is an
administrator. With the opt-in dash logs a second WARN naming the address at
every start, and off loopback it refuses every settings change and connection test (403).

**Upgrading from v0.11 or earlier:** the default used to be `proxy`. If dash sits behind an
identity proxy and you never set the mode, set `DASH_AUTH_MODE=proxy` together with the new image.
An `auth.proxy.adminUsers` or `adminGroups` without that line stops dash from starting, with a
message that says so (and that `local` is the default since v0.12.0); any other `auth.proxy`
setting without it is a warning.

**Rolling back to v0.11:** with `DASH_AUTH_MODE=proxy`, the image alone. An install that set
`local` or `oidc` must remove `DASH_AUTH_MODE` together with the image (v0.11 refuses the value);
v0.11 then runs in proxy mode, ignores `credentials.yaml` (left untouched, and kept by a later
v0.12) and warns about the new variables. Sessions issued by v0.12 do not carry over.

## The local mode

One account, named by `auth.local.adminUser` (default `admin`), signs in with a password. There
are two ways to create it.

**The setup link.** On the first start without an account dash writes one line to its standard
error:

```json
{"msg":"setup token required: open the url within an hour","url":"https://dash.example.com/setup#token=dash_setup_…","expires":"…"}
```

Open the URL within an hour and choose a password (12 to 256 bytes). The token is in the URL
fragment, so it never reaches an access log, a proxy or a `Referer`; the setup page removes it
from the address bar at once. It works once, and a restart prints a new one. dash's log scrubber
redacts it from the pod logs the UI shows. Until an account exists, `/login` points to that log
line instead of showing a form, also once the hour has passed: the setup page then says the link
has expired, and a restart of dash prints a new one. After setup, `/setup` answers 404 (a plain 404
page: the link has done its job).

**From the environment.** `dash hash-password` reads a password from the terminal (without echo,
twice) or from standard input and prints its argon2id hash:

```sh
dash hash-password            # prompts
printf '%s\n' "$PASSWORD" | dash hash-password
```

Give the hash to dash as `DASH_ADMIN_PASSWORD_HASH` (or a file with it as
`DASH_ADMIN_PASSWORD_HASH_FILE`). The account is then managed by the environment: `/setup` is 404,
and changing the hash ends the account's sessions at the next start.

The account and the session state live in `credentials.yaml` in the data directory (below), so the
local mode needs a writable `DASH_DATA_DIR` **or** `DASH_ADMIN_PASSWORD_HASH`; with neither dash
refuses to start: `auth.mode=local needs a writable data dir (DASH_DATA_DIR) or
DASH_ADMIN_PASSWORD_HASH`.

If you change `auth.local.adminUser` after setup, dash refuses to start and names both names: set
the name back, or stop dash and delete `credentials.yaml` to run setup again (which also ends every
session).

**Login protection.** A wrong name and a wrong password get the same answer. After five failures
from one address, or for the account, further attempts are refused with 429 and `Retry-After`
for 1 second, doubling up to 15 minutes, without being tried. Behind a reverse proxy every client
shares the proxy's address, so one attacker can delay everyone's login (and the account key lets
them delay yours): the local mode is the zero-infrastructure mode, not the hardened one. Failed
logins are counted (`dash_auth_failures_total{reason="login_failed|login_throttled"}`) and
audited at most once per address and minute. Password hashing is argon2id (64 MiB, 3 passes, 4
lanes) and at most two run at once (128 MiB); a third login at the same moment gets 429 at once.

**The anonymous viewer.** `auth.local.anonymousViewer: true` lets a visitor who has not signed in
read the dashboard as a viewer, once the account exists (until then a visitor gets the 401 that
points to the setup link): GET requests only, never a log route (403; its scopes are `read`
alone, so the pod sheet offers no Logs tab), never an action. dash warns at every start while it
is on, and `/api/v1/me` lists `anonymous_viewer` in an administrator's `warnings` (the web UI shows
no banner for it yet).

## Sessions

A login sets a cookie: `__Host-dash_session` when `server.publicURL` is https (with `Secure`), else
`dash_session`; always `HttpOnly`, `SameSite=Lax`, `Path=/`, without `Domain`. It holds the user
and the groups and role values the mapping can use, sealed (XChaCha20-Poly1305), and no role. A
session lasts 12 hours from the last request that renewed it (a request in the second half of that
window renews it) and at most 7 days from the login.

The session key is derived from `DASH_SECRET_KEY`, a random salt in `credentials.yaml`, and the
mode (a session of the local mode never opens in the oidc mode, or the other way round). So:

- without `DASH_SECRET_KEY` or without a writable data dir the key is random for each start, and
  every restart signs everyone out (dash says so in its log);
- a new `DASH_SECRET_KEY` signs everyone out (`DASH_SECRET_KEY_PREVIOUS` is not used for sessions);
- switching between `local` and `oidc` signs everyone out;
- deleting or restoring `credentials.yaml` signs everyone out: **stop dash, then delete (or
  restore) the file, then start it.** A file removed or replaced while dash runs is not written
  again; dash logs a warning and the sessions end at the next restart.

"Log out" ends your sessions on every device (a copied cookie stops working too). "Log out
everywhere" does the same and revokes your [connected apps](#oauth) too; "Sign out all users"
(administrators) ends every session of every user and revokes every connected app. "Log out
everywhere" and "Sign out all users" ask first. No logout revokes a personal access token: revoke
those in Settings → Tokens. An event
stream or log tail that is already open ends at its next heartbeat, within 15 seconds, and the
page goes to sign in again. `credentials.yaml` is mode 0600, written atomically, never backed up
(`.bak`), and locked (`.credentials.lock`) so that a second dash on the same data dir refuses to
start. It also holds the [personal access tokens](#api-tokens) and the [OAuth grants](#oauth)
(their hashes), so deleting it revokes every token and grant too.

## The oidc mode

dash signs users in at an OpenID Connect provider with the authorization code flow and PKCE.
Register dash as a confidential client with the redirect URL `<server.publicURL>/auth/callback`
(`server.publicURL` must be https, except on a loopback address), then set:

```yaml
auth:
  mode: oidc
  oidc:
    issuer: https://login.example.com/realms/home   # exactly as the ID tokens name it; https unless loopback
    clientID: dash
    clientSecret: file:/run/secrets/dash-oidc      # or env:NAME, k8s:<ns>/<name>#<key>
    allowedGroups: [family]                         # or allowedUsers; one is required
    adminGroups: [ops]
```

| Setting | Default | What |
|---|---|---|
| `scopes` | `["openid","profile","email"]` | the scopes requested; `openid` is always added |
| `userClaim` | `preferred_username` | the ID token claim that names the user. `email` is accepted only with `email_verified: true` |
| `groupsClaim` | `groups` | the claim with the groups: a list or one comma-separated string |
| `roleClaim` | empty | a claim whose values are matched against `adminRoles` |
| `allowedUsers`, `allowedGroups` | `[]` | who may sign in. One is required; `allowedUsers: ["*"]` admits every account the provider signs in (a warning at every start, and `oidc_open` in an administrator's `/api/v1/me` `warnings`) |
| `adminUsers`, `adminGroups`, `adminRoles` | `[]`, `[]`, `["admin"]` | who is an administrator; these three apply on a reload |

Anyone else is sent back to `/login?error=not_allowed`. Only ID-token claims are read; there is no
UserInfo call. dash contacts the provider at the first login, not at start, so an unreachable
provider does not stop dash; the login answers 503 until it is back.

Things to know:

- **`preferred_username` can be changed by the user** at some providers. If yours allows that, use
  `userClaim: sub` and list subject IDs in `allowedUsers` and `adminUsers`.
- **Groups are read at login.** The session keeps only the groups listed in `adminGroups` or
  `allowedGroups` and the role values listed in `adminRoles` (so the cookie stays small): a group
  you add to `adminGroups` applies at the member's next login, one you remove applies at once.
- **Logout is local.** It ends the dash session, not the provider's: a provider with an active
  single sign-on session signs you straight back in.
- A user, group or role entry that the provider's claims can never produce (empty, a control
  character, invalid UTF-8, too long, a comma or `*` in a group or role) stops dash from starting.
- **RSA keys are at most 4096 bits.** A provider whose key set holds an RSA key over 4096 bits
  that names a signing algorithm, or none, even if the provider signs with another key, makes the
  whole key set fail to decode, so every sign-in fails with "Signing in with the identity provider
  failed. Try again." and dash logs `oidc login refused` with `stage=verify` and `invalid RSA key,
  modulus too large`. An oversized encryption-only key (`alg: RSA-OAEP`) does no harm. Remove the
  oversized signing key from the provider's key set (or rotate to keys of at most 4096 bits).

## The proxy mode

An identity proxy authenticates the user and forwards the name in a header. dash believes the
header **only from the TCP peers in `auth.proxy.trustedProxies`** (default the loopback) and never
reads `X-Forwarded-For`; any other peer sending it gets 401.

| Setting | Default | What |
|---|---|---|
| `userHeaders` | `["X-Forwarded-Preferred-Username","X-Forwarded-User"]` | the headers that name the user; the first one present wins. Two lines of one header are refused |
| `groupsHeader` | empty (groups off) | the header with the user's groups, comma-separated |
| `trustedProxies` | `["127.0.0.1/32","::1/128"]` | the believed peers (CIDRs or addresses); `0.0.0.0/0` needs `allowAnyPeer: true` |
| `adminUsers`, `adminGroups` | `[]` | who is an administrator, exact and case-sensitive; with both empty everyone is a viewer. They apply on a reload |

An admin entry no header can carry (a user over 128 bytes, a group over 256, invalid UTF-8, a
control character, a comma in a group) stops dash from starting, and so does an `auth.proxy`
admin list set while `auth.mode` is not `proxy`.

**Example: an oauth2-proxy sidecar in the same pod.** The proxy listens for the world and forwards
to dash on the pod's loopback, so the default `trustedProxies` already believe it. Since v0.14.0
dash listens on `0.0.0.0:8080` by default, so the sidecar setup sets
`DASH_SERVER_LISTEN=127.0.0.1:8080` explicitly; then nothing else can reach dash's port. Without
that line dash still refuses identity headers from any other peer, and it logs a WARN at start
("the API listens on a non-loopback address but only loopback proxies are trusted") whenever
every trusted proxy is a loopback address and the listener is not, unless `server.directURL`
says a direct route reaches that listener on purpose
([API tokens in the proxy mode](#api-tokens-in-the-proxy-mode)):

```yaml
containers:
  - name: dash
    env:
      - {name: DASH_AUTH_MODE, value: proxy}
      - {name: DASH_SERVER_LISTEN, value: "127.0.0.1:8080"}
      - {name: DASH_AUTH_PROXY_TRUSTED_PROXIES, value: '["127.0.0.1/32","::1/128"]'}
      - {name: DASH_AUTH_PROXY_ADMIN_USERS, value: '["alice"]'}   # the name the proxy forwards, exactly
  - name: oauth2-proxy
    args:
      - --upstream=http://127.0.0.1:8080/
      - --http-address=0.0.0.0:4180
      - --pass-user-headers=true      # X-Forwarded-User, -Email, -Groups
      - --reverse-proxy=true
      # plus your provider, client and allowed users or groups
```

The Service and the route point at the proxy's port (4180), never at dash's. dash logs one
`principal seen` line the first time each user appears, with the header that named them: use it to
check the exact name to put in `adminUsers`. oauth2-proxy strips any `X-Forwarded-*` a client
sends before it sets its own. With `--pass-authorization-header` unset it forwards no
`Authorization`, so a bearer client cannot reach dash through the proxy at all; see
[API tokens in the proxy mode](#api-tokens-in-the-proxy-mode).

## API tokens

Programs (scripts, CI jobs, agents) call the API with a bearer token:
`Authorization: Bearer <token>`. In every mode but `none` (which ignores the header, and where the
token routes answer 404) dash looks at a bearer credential **first**: the request is the token's
and nothing else, so the identity headers of a proxy are not read.

- With dash's session cookie as well it is 400 `ambiguous_credentials`. An identity proxy's own
  cookie is not dash's and does not count.
- A credential that does not start with `dash_` is refused before any lookup. An unknown, expired
  or revoked token is 401 `invalid_token` ("invalid token"); a valid token of the other audience
  is 401 `invalid_token` too, saying "token not valid for this API (audience mcp)". Every 401 of
  `/api` carries `WWW-Authenticate: Bearer resource_metadata="<base>/.well-known/oauth-protected-resource/api"`,
  where `<base>` is `server.directURL` when it is set and `server.publicURL` otherwise. The
  document is served while [OAuth](#oauth) is on; otherwise every `/.well-known/` path answers a
  JSON 404.
- A token request needs no `X-Dash-Csrf` header and no `Origin`: the skip applies to a request
  whose token dash resolved, never to one that merely carries the header.
- An `api` token gets 300 requests a minute on average, in bursts of up to 300 (a token bucket);
  past that it is 429 `rate_limited` with `Retry-After`.
- Settings, the token routes and every other route for people answer a token 403 `forbidden`
  ("this route needs a signed-in user, not a token").
- An open event stream or log tail re-checks its token at every heartbeat, so a revoked token's
  stream ends within 15 seconds.
- The audit log records a token's request with `actor.type: token`, the `api` surface and its
  `tokenID` (the token's ID, or `static:<name>`), or for an OAuth access token its `grantID` and
  `clientID`, and the audit view shows them. `authMode` stays the server's mode (`proxy`,
  `local`, ...), in `/api/v1/me` as in the audit log, and a refused bearer credential is recorded
  on the `api` surface.

**Scopes** narrow what the role allows:

| Scope | Grants |
|---|---|
| `read` | `/api/v1/me`, topics, the stream, series and the plugins' read routes |
| `logs` | log queries and tails |
| `actions:<plugin>` | that plugin's enabled actions; minted by administrators only, for a plugin whose actions are enabled |

A token without the scope a route needs gets 403 `insufficient_scope`, and the message names the
scope ("this token lacks the read scope"). `approvals` and `push` never go on a personal access
token or a static token. A token's role is derived on every request (below), and each action
still needs its own role: when an administrator who minted an `actions:` token is demoted, the
token keeps its scope but runs only the actions a viewer may run (most need an administrator).
Revoke the token if it should run nothing.

**Action namespaces.** A token's `actionNamespaces` restrict where its actions may act: each entry
is an exact namespace (`media` admits `media`, not `media-prod`) or a prefix ending in `*` (`apps-*`
admits `apps-web`, never `my-apps-x`). An empty list adds no restriction. A non-empty list also
refuses an action whose target has no namespace, a silence for example (403), unless it holds
`*`.

**Audiences.** Each token is bound to `api` (`/api/v1`) or `mcp` (`/mcp`, the read-only
[MCP server](mcp.md), which is off until `mcp.enabled` is set). Presented at the other surface it
is 401 `invalid_token`, so a token handed to an agent for MCP cannot be replayed against the REST
API.

### Personal access tokens

Settings → Tokens (`/settings/tokens`), open to everyone who signs in, creates them; the token is
shown once, and the dialog that shows it closes only with Done or its close button. The format is
`dash_pat_<10-character ID>_<43-character secret>`; dash keeps only its SHA-256 and compares in
constant time. Each token has a name (1 to 64 characters with at least one visible one, no control
or format characters), an audience, scopes, the optional `actionNamespaces` (above) and an expiry
of 1 to 365 days (30 when absent). `lastUsedAt` is noted only for a request the token was accepted
for, kept in memory and written (in UTC, to the second) at least every 5 minutes and when dash
stops.

A viewer creates `read` and `logs` tokens and revokes their own; an administrator also creates
`actions:<plugin>` tokens and lists (`GET /api/v1/tokens?user=<name>` or `?user=*`) and revokes
anyone's. A user holds at most 50 tokens that have not expired (409 `token_limit`) and creates at
most 10 a minute (429). Tokens that expired more than 30 days ago are deleted. Revocation applies
to the next request. Creating and revoking are audited before they happen: when the audit log
cannot be written, the answer is 503 `audit_unavailable` and nothing changes. A revocation that
cannot be written to `credentials.yaml` (a full disk, a file replaced while dash runs) still holds
in dash, but answers 500: the token would come back at the next restart. In an emergency stop dash
and delete `credentials.yaml`: that revokes every token and ends every session.

Tokens need somewhere to live. With a writable data dir they are in `credentials.yaml` in the
local, oidc and proxy modes. Without one the store is in memory, and creating a token is refused
(409 `tokens_unavailable`; `/api/v1/me` says `capabilities.tokens: false`, and the Tokens page
says tokens are not available) because it would be lost at the next restart. Dev and demo mode
keep tokens in memory anyway, so you can try a client against them; they are gone when dash
stops. On Kubernetes the same holds for the pod: with the chart's `persistence.enabled: false`
the data dir is an `emptyDir`, so every token vanishes when the pod restarts, and with several
replicas each pod has its own tokens: a token created through one pod is unknown to the others.
Run one replica with persistence for tokens.

**Tokens follow the identity provider.** A token records its owner's groups and role-claim values
(local users have none), and the role is derived from them with the current admin lists on every
request, then narrowed by the scopes. Whenever dash sees the person again (a login in the local
and oidc modes, any request in the proxy mode) it rewrites what their tokens record, and writes
nothing when it is unchanged; when the file cannot be written the change still holds in dash.
A group change therefore reaches a token at the owner's next login (local, oidc) or next request
(proxy). Removing someone at the identity provider does not reach a token they never use
interactively: when you offboard a person, revoke their tokens in dash too.

| Route | What |
|---|---|
| `GET /api/v1/tokens` | `{"tokens": [{id, name, user, audience, scopes, actionNamespaces, createdAt, expiresAt, lastUsedAt}]}`; `?user=` for administrators (empty: your own) |
| `POST /api/v1/tokens` `{name, audience, scopes, actionNamespaces?, expiresInDays?}` | 201 `{"token", "info"}`; 400 `invalid_input` with `details.fields[{path, message}]`; 403 for `actions:*` by a viewer and for `approvals` or `push`; 409 `token_limit` or `tokens_unavailable`; 429 |
| `DELETE /api/v1/tokens/{id}` | 204; 404 for a token that is not yours (viewers); 500 when the revocation could not be written |

### Static tokens

For GitOps installs, `auth.tokens` (file, or `DASH_AUTH_TOKENS` as JSON) lists tokens by their
hash. Make each token with `dash token generate`, which prints a `dash_st_<43 characters>` token
and its hash; only the hash goes into the config. A token you make some other way must start with
`dash_` (dash refuses any other bearer credential before looking it up) and should be as random:
the log scrubber redacts `dash_st_` tokens by their shape.

```yaml
auth:
  tokens:
    - {name: ci, role: viewer, audience: api, scopes: [read], hash: "sha256:<64 hex digits>"}
```

The principal is `token:<name>` with the configured role. Scopes are limited as for personal
access tokens, and an `mcp` token cannot hold `actions:*`: an agent's actions wait for the owning
user's approval, and a static token has no owner (use a personal access token). A broken entry
stops dash from starting, naming its position, never its value. A change in the config file
applies on reload, so removing an entry revokes it at once; from the environment it needs a
restart. In `auth.mode=none` the list is ignored, with a WARN at start. The settings page shows
each entry as `ci (viewer, api, read)`, never its hash.

### CORS

Browser-based bearer clients (an MCP inspector, a web tool on another origin) need
`server.corsOrigins`, a list of origins (`scheme://host[:port]`, never `*`; restart-only). Without
it dash sends no `Access-Control-*` header at all, and neither does it in `auth.mode=none`, which
has no bearer clients (the setting is ignored there, with a WARN at start). A listed origin gets:

- an answer to its preflight (`OPTIONS` with `Access-Control-Request-Method`), on the origin alone:
  `Access-Control-Allow-Origin: <origin>`, `Access-Control-Allow-Methods: GET, POST, DELETE`,
  `Access-Control-Allow-Headers: authorization, content-type, if-none-match, mcp-protocol-version, mcp-method, mcp-name`,
  `Access-Control-Max-Age: 600` and `Vary: Origin`;
- `Access-Control-Allow-Origin` and `Access-Control-Expose-Headers: ETag, X-Request-Id, WWW-Authenticate`
  on every answer to a request dash resolved to a token, and on any 401. A `Bearer` header that
  resolves to no token grants nothing.

`Access-Control-Allow-Credentials` is never sent, so cookie and identity-proxy requests stay
same-origin.

### API tokens in the proxy mode

A bearer client has no identity proxy session, so the proxy in front of dash turns it away (or
strips its `Authorization`). Bearer clients need a second host that reaches dash without the
proxy, named by `server.directURL` (restart-only): an origin only (`https://dash-direct.example.com`,
no path), never on `server.publicURL`'s host, at any port, https unless the host is loopback, and
refused in `auth.mode=none`. With it set:

- dash answers that host (no need to list it in `server.allowedHosts`);
- the start no longer warns that dash listens on a non-loopback address while only loopback
  proxies are trusted: that is the point of the direct host;
- identity headers on that host are refused whatever the peer (401, recorded in the audit log
  and counted with reason `direct_host`);
- the 401 challenge points at `<directURL>/.well-known/oauth-protected-resource/api`, and
  [OAuth](#oauth) uses it as the issuer and the base of its token, revoke and discovery endpoints.

The Helm chart's `routes.direct` sets all of this up for the oauth2-proxy sidecar
([kubernetes.md](deploy/kubernetes.md#a-direct-host-for-bearer-clients-routesdirect)). A route
you build yourself needs:

- dash listening where that route reaches it: `server.listen` on a non-loopback address such as
  `0.0.0.0:8080` (the sidecar setup binds `127.0.0.1`, which only the proxy reaches);
- `server.directURL` set to that route's origin;
- the route's peer never in `auth.proxy.trustedProxies`: dash would then believe identity headers
  that peer forwards. Keep the trusted list to the sidecar's loopback addresses, so the direct
  route carries bearer tokens and nothing else, and its browser pages sign nobody in.

## OAuth

The [Android app](clients/android.md) and other API clients get their tokens from dash itself:
dash is an OAuth 2.1 authorization server, and MCP clients ([connecting them](agents/mcp.md)) sign
in the same way for the `mcp` resource of the [MCP server](mcp.md). The `mcp` resource follows
`mcp.enabled`, live: while it is off a request for
it is `invalid_target` ("the MCP server is off on this dash") and
`/.well-known/oauth-protected-resource/mcp` answers 404. The authorization server is **off**
until `auth.oauth.enabled` is true (restart-only), because it
adds an endpoint that needs no sign-in (`/oauth/token`), and it runs only in the `local`, `oidc`
and `proxy` modes with a writable data dir (the grants live in `credentials.yaml`). Otherwise every
`/oauth/*` and `/.well-known/oauth-*` path answers a JSON 404, dash logs why at start, and
`/api/v1/me` says `capabilities.oauth: false`. With OAuth on, `server.publicURL` may not have a
path. There is no dynamic client registration.

**Clients** are public (no client secret):

- `dash-android`, the first-party app: redirect `io.github.techdufus.dash:/oauth2redirect`, the
  `api` audience only, and the only client that may be granted `approvals` and `push`;
- a client ID metadata document: the client's `client_id` is an https URL with a path, and dash
  fetches it (5 seconds, 64 KiB, no redirect, no proxy from the environment, cached 24 hours; a
  failed fetch is remembered for 60 seconds, so the URL is not fetched again on every request).
  The document needs `client_id` equal to its URL, a `client_name`, and `redirect_uris` that are
  https, http on a loopback host, or a private-use scheme containing a dot
  (`com.example.app:/callback`). dash fetches only from global addresses, unless
  `auth.oauth.cimdAllowPrivate` is true, which adds private (RFC 1918, CGNAT, ULA) and loopback
  addresses; link-local addresses, where cloud metadata services live, stay refused. A document
  whose `client_name` is the name or ID of `dash-android` or of an `auth.oauth.clients` entry is
  refused, so it cannot pass for one of them. The names are compared after Unicode NFKC, with
  every run of spaces (any Unicode space) made one space, surrounding spaces and trailing
  punctuation dropped, and case ignored;
- `auth.oauth.clients` from the config (`DASH_AUTH_OAUTH_CLIENTS` as JSON):
  `[{clientID, name, redirectURIs, audiences}]`, with the same redirect rules.

Any client but `dash-android` that asks for `approvals` or `push` gets `invalid_scope`, and the
consent page never offers them to it. The consent page and Settings → Connected apps badge each
client by kind: "Built in", "Configured by your admin", or "Unverified · metadata from
`<host>`"; for a metadata-document client the consent headline names the host too ("Connect
Agent from agent.example.com?").

**The flow** is the authorization code flow with PKCE:

1. The client opens `/oauth/authorize` in the browser with `response_type=code`, `client_id`,
   `redirect_uri`, `state`, `code_challenge` with `code_challenge_method=S256` (`plain` is
   refused), `resource` (`<issuer>/api`, the token's audience; `<issuer>/mcp` once dash serves MCP) and `scope`
   (default `read`). The redirect URI must be one the client registered, exactly; an http
   loopback one matches on any port. A request whose client or redirect URI cannot be verified is
   answered there (400); any other error goes back to the client with `state` and `iss`.
2. A person who is not signed in goes to sign in first and comes back to the same
   `/oauth/authorize` URL (which must fit in 1024 bytes, else 400); dash keeps nothing for them
   meanwhile, and fetches a client's metadata document only for a signed-in person. For a signed-in
   person dash keeps the request for 10 minutes (at most 20 per person; the oldest goes) and sends
   the browser to `/consent/<id>`, which only that person sees. The page ticks only `read`; `logs`
   and `actions:<plugin>` carry warnings, and only an administrator is offered `actions:<plugin>`
   (a viewer's page names the requested scopes their role cannot grant).
3. Approving sends the browser back to the client with `code`, `state` and `iss` (RFC 9207);
   denying with `error=access_denied`.
4. The client exchanges the code (60 seconds, once) with its `code_verifier` at `/oauth/token`
   for an access token and a refresh token. A wrong verifier, client or redirect URI, or a code
   used twice, revokes what the code made. A code never exchanged takes its grant with it; the
   audit log records that as `oauth.grant.expire` (reason `code_expired`).

**Tokens.** An access token is `dash_at_<base64url>`, lives 15 minutes and is sealed with
XChaCha20-Poly1305 under a key derived from `DASH_SECRET_KEY` and the salt in `credentials.yaml`:
without the key it is random per start, so access tokens end at a restart (clients refresh).
Each request re-checks that its grant still exists, derives the role from the groups and
role-claim value the grant recorded and the current mapping, and narrows it to the grant's scopes;
it works only at its audience. A refresh token is `dash_rt_<grant>_<secret>`, stored as a hash and
rotated on every refresh; presenting a rotated one again revokes the grant. A grant ends 30 days
after its last refresh and 180 days after the consent. `/oauth/revoke` (RFC 7009) takes either
token and revokes the whole grant; a refresh token is never a bearer credential.

**Connected apps.** Each consent is a grant: Settings → Connected apps (`/settings/connected-apps`)
lists yours (client, audience, scopes, created and last used: the last refresh or request with
one of its access tokens, written to `credentials.yaml` like a personal access token's, at most
five minutes late) with a revoke button, and an administrator sees and revokes everyone's. A
user holds at most 20 grants (409 `grant_limit`).
Grants follow the identity provider like personal access tokens: the groups and role-claim value
they record are rewritten at the user's next login (`local`, `oidc`) or next request (`proxy`).
Removing a user at the identity provider does not reach grants they no longer use
interactively, so offboarding includes revoking them. "Log out everywhere" revokes your grants and
"Sign out all users" everyone's; the proxy mode, which has no logout, offers "Revoke all my
connected apps" on the Mine tab instead. In every mode an administrator's "All users" tab has
"Revoke every user's connected apps" (`DELETE /api/v1/oauth/grants?user=*`; a viewer gets 403),
the step to take before rolling back to v0.14.6. Each of these asks first. Approving a consent and
revoking a grant are audited before they happen: when the audit log cannot be written the answer
is 503 `audit_unavailable` and nothing changes.

**CORS and limits.** A `server.corsOrigins` origin gets `Access-Control-Allow-Origin` on the
`/.well-known/oauth-*` documents, `/oauth/token` and `/oauth/revoke`, never on `/oauth/authorize`
(a browser navigation). `/oauth/token` and `/oauth/revoke` take a form with no CSRF header and
allow 60 requests a minute per key (429 after): the grant a refresh token names, else an
authorization code dash holds, else a known `client_id` and the peer, else the peer alone. Every
`/oauth` answer is `Cache-Control: no-store`.

**In the proxy mode** `/oauth/authorize` and the consent page must go through the identity proxy,
which knows the person; the client reaches everything else on the direct host. With
`server.directURL` set (see [API tokens in the proxy mode](#api-tokens-in-the-proxy-mode)), the
issuer, the resource (`<directURL>/api`), the token and revoke endpoints and
the discovery documents are on the direct URL, while the metadata's `authorization_endpoint`
stays `<publicURL>/oauth/authorize`. The identity proxy's skip-auth rules (oauth2-proxy
`--skip-auth-route` or `--skip-auth-regex`) must not cover `/oauth/authorize` or `/consent`. On
the direct host `/oauth/authorize` and `/consent` answer 404, since that host bypasses the proxy:

1. the client calls `https://dash-direct.example.com/api/v1/...` without a token and reads the
   `resource_metadata` of the 401, then `/.well-known/oauth-authorization-server` there;
2. it opens `https://dash.example.com/oauth/authorize?...&resource=https://dash-direct.example.com/api`
   in the browser; the proxy signs the person in and the consent page runs behind it;
3. the browser returns to the client with `iss=https://dash-direct.example.com`, and the client
   exchanges the code and refreshes at `https://dash-direct.example.com/oauth/token`.

The endpoints and their answers are in [the API reference](api/README.md#oauth).

## What the API answers

`/api/v1/me` says who you are: `user`, `role`, `authMode` (`local`, `oidc`, `proxy`, `none`,
`dev` or `demo`), `scopes` (for a person what they may put on a token; for a token its own),
`audience`, `capabilities.tokens` (whether you may create a token here), `capabilities.oauth`
(whether dash serves [OAuth](#oauth)) and `warnings` (`admin_wildcard`, `auth_none`, and for
administrators `anonymous_viewer` and `oidc_open`).

A request to `/api` without a valid sign-in is 401 with
`{"code": "unauthenticated", "details": {"mode": "local", "setup": false, "oidc": {"issuer": "<host>"}}}`
(`oidc` only in that mode), which the web UI uses to go to `/login`, `/setup` or the provider, or to
reload behind a proxy. Each browser tab does that at most once per 30 seconds (the guard is in the
tab's `sessionStorage`), so neither a reload nor a redirect can loop.

The sign-in routes are outside `/api` and not in the OpenAPI document:

| Route | What |
|---|---|
| `POST /login` `{"username","password"}` | 204 and the session cookie; 401 `unauthenticated` for any wrong name or password; 429 `rate_limited` with `Retry-After` |
| `POST /setup` `{"token","password"}` | 200 `{"adminUser"}` (the account it created; no other answer names it) and the session cookie; 401 `invalid_token`; 400 for a password out of bounds; 404 once the account exists |
| `POST /auth/logout` `{"scope":"this"\|"user"\|"all"}` | 204 and a cookie that clears the session; `user` also revokes your OAuth grants, `all` (administrators) every session and every grant; 503 `audit_unavailable`, with nothing changed, when there are grants to revoke and the audit log cannot be written |
| `GET /auth/oidc/start?next=/path` | 302 to the provider; 503 `upstream_unavailable` when it cannot be reached |
| `GET /auth/callback` | 302 to `next` with the session, or to `/login?error=not_allowed` / `oidc_failed` |

Every `POST` needs `X-Dash-Csrf: 1` and an `Origin` equal to `server.publicURL`'s; every answer of
these routes is `Cache-Control: no-store`.
