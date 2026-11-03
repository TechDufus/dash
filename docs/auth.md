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

"Log out" ends your sessions on every device (a copied cookie stops working too); "Log out
everywhere" (administrators) ends every session of every user. An event stream or log tail that is
already open ends at its next heartbeat, within 15 seconds, and the page goes to sign in again.
`credentials.yaml` is mode 0600, written atomically, never backed up (`.bak`), and locked
(`.credentials.lock`) so that a second dash on the same data dir refuses to start.

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

**Example: an oauth2-proxy sidecar in the same pod.** The proxy listens for the world and forwards
to dash on the pod's loopback, so the default `trustedProxies` already believe it and nothing else
can reach dash's port:

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
`Authorization`; a `Bearer` credential that does reach dash is refused (401 `invalid_token`).

## What the API answers

`/api/v1/me` says who you are: `user`, `role`, `authMode` (`local`, `oidc`, `proxy`, `none`,
`dev` or `demo`) and `warnings` (`admin_wildcard`, `auth_none`, and for administrators
`anonymous_viewer` and `oidc_open`).

A request to `/api` without a valid sign-in is 401 with
`{"code": "unauthenticated", "details": {"mode": "local", "setup": false, "oidc": {"issuer": "<host>"}}}`
(`oidc` only in that mode), which the web UI uses to go to `/login`, `/setup` or the provider, or to
reload behind a proxy. Each browser tab does that at most once per 30 seconds (the guard is in the
tab's `sessionStorage`), so neither a reload nor a redirect can loop.

The sign-in routes are outside `/api` and not in the OpenAPI document:

| Route | What |
|---|---|
| `POST /login` `{"username","password"}` | 204 and the session cookie; 401 `unauthenticated` for any wrong name or password; 429 `rate_limited` with `Retry-After` |
| `POST /setup` `{"token","password"}` | 204 and the session cookie; 401 `invalid_token`; 400 for a password out of bounds; 404 once the account exists |
| `POST /auth/logout` `{"scope":"this"\|"user"\|"all"}` | 204 and a cookie that clears the session; `all` is for administrators |
| `GET /auth/oidc/start?next=/path` | 302 to the provider; 503 `upstream_unavailable` when it cannot be reached |
| `GET /auth/callback` | 302 to `next` with the session, or to `/login?error=not_allowed` / `oidc_failed` |

Every `POST` needs `X-Dash-Csrf: 1` and an `Origin` equal to `server.publicURL`'s; every answer of
these routes is `Cache-Control: no-store`.
