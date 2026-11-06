# The Android app contract

The dash Android app lives outside this repository and is not released yet. This page is the
contract it builds against: everything the app needs from a dash server, and nothing it may
assume beyond it. A change to anything here is a change to the app's contract.

| Identifier | Value |
|---|---|
| Application ID | `io.github.techdufus.dash` |
| OAuth client | `dash-android` (built in, public, no client secret) |
| Redirect URI | `io.github.techdufus.dash:/oauth2redirect` (a private-use scheme, [RFC 8252 §7.1](https://www.rfc-editor.org/rfc/rfc8252#section-7.1)) |

These are published identifiers: they are compiled into dash and cannot change casually.

## What the server needs

- `auth.mode` `local`, `oidc` or `proxy`, a writable data dir and `auth.oauth.enabled: true`
  ([OAuth](../auth.md#oauth)). Without them every `/oauth/*` and `/.well-known/oauth-*` path is a
  JSON 404, and `/api/v1/me` says `capabilities.oauth: false`.
- For push: `DASH_SECRET_KEY` and a VAPID contact ([push](../push.md#what-push-needs)).
- **Behind an identity proxy** (`auth.mode: proxy`): `server.directURL`, a second host that
  reaches dash without the proxy. The app talks to that host for everything but the sign-in page
  ([OAuth in the proxy mode](../auth.md#oauth)).

## The server URL and the QR code

The user types the server URL, or scans it from the QR code on **Settings → Devices** in the web
UI. The QR code holds the URL only, never a credential: `server.directURL` when it is set,
otherwise `server.publicURL` (the same value as `serverUrl` in `GET /api/v1/push/vapid`). Call it
`<server>` below.

## Discovery

1. `GET <server>/api/v1/me` without a token answers 401 with
   `WWW-Authenticate: Bearer resource_metadata="<server>/.well-known/oauth-protected-resource/api"`.
2. That document ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)) gives `resource`
   (`<server>/api`), `authorization_servers` (`[<server>]`) and `scopes_supported`.
3. `<server>/.well-known/oauth-authorization-server` ([RFC 8414](https://www.rfc-editor.org/rfc/rfc8414))
   gives `issuer`, `authorization_endpoint`, `token_endpoint` and `revocation_endpoint`. Use the
   endpoints it names: behind an identity proxy `authorization_endpoint` is on the public URL,
   which the proxy guards, while the issuer and the token and revoke endpoints are on the direct
   URL. `/oauth/authorize` on the direct host is a 404.

## Signing in

Use [AppAuth-Android](https://github.com/openid/AppAuth-Android) with Custom Tabs: the
authorization code flow with PKCE.

- **Authorization request** (`authorization_endpoint`): `response_type=code`,
  `client_id=dash-android`, `redirect_uri=io.github.techdufus.dash:/oauth2redirect`, a random
  `state`, `code_challenge` with `code_challenge_method=S256` (`plain` is refused),
  `resource=<server>/api` (as an additional parameter) and
  `scope=read logs approvals push`. Add `actions:<plugin>` for a plugin listed in
  `scopes_supported` to let the user run that plugin's actions from the app (administrators only;
  through the `api` resource they run without an approval, as in the web UI).
- dash signs the person in if needed, shows its consent page and sends the Custom Tab to the
  redirect URI with `code`, `state` and `iss` ([RFC 9207](https://www.rfc-editor.org/rfc/rfc9207)).
  Check `state`, and that `iss` equals the metadata's `issuer`. The consent page ticks only `read`;
  the person ticks the rest, so the token may carry fewer scopes than asked: read `scope` in the
  token response.
- An error after the client and redirect URI are verified comes back the same way:
  `error` (`invalid_request`, `invalid_scope`, `invalid_target`, `access_denied`, …),
  `error_description`, `state` and `iss`.
- **Token request** (`token_endpoint`, a form, no client authentication):
  `grant_type=authorization_code`, `code`, the same `redirect_uri`, `client_id=dash-android`,
  `code_verifier` and `resource=<server>/api`. The code lives 60 seconds and works once; a second
  use, or a wrong verifier, client or redirect URI, revokes the grant the code made.

The answer:

```json
{
  "access_token": "dash_at_…",
  "token_type": "Bearer",
  "expires_in": 900,
  "refresh_token": "dash_rt_…",
  "scope": "read logs approvals push"
}
```

`dash-android` is the only client dash grants `approvals` or `push`; any other client asking for
them gets `invalid_scope`.

## Tokens

- The **access token** lives 15 minutes and works only for `/api/v1` (`/mcp` refuses it). A 401
  `invalid_token` means it expired, or the grant ended: refresh.
- **Refresh** at `token_endpoint` with `grant_type=refresh_token`, `refresh_token` and
  `client_id=dash-android` (optionally `scope` to narrow, never widen). Every refresh returns a
  **new refresh token**; store it before using the access token. Presenting a refresh token a
  second time revokes the whole grant (`invalid_grant`, "the refresh token was already used; the
  grant is revoked"), so never retry a refresh with the old token after the answer arrived. A
  grant ends 30 days after its last refresh and 180 days after the consent.
- A `400 invalid_grant` on refresh means the grant is gone (revoked in Settings → Connected apps,
  by Log out everywhere, by reuse, or expired): drop the tokens and sign in again.
- `/oauth/token` and `/oauth/revoke` allow 60 requests a minute per grant (429 after).

**Logout:** `POST <revocation_endpoint>` with `token=<refresh token>` and `client_id=dash-android`
([RFC 7009](https://www.rfc-editor.org/rfc/rfc7009)). It answers 200 and revokes the whole grant:
its access tokens stop working at once, and the push device the app registered with it turns
stale. Then delete the tokens on the phone.

## Data

Send `Authorization: Bearer <access token>` on every request to `/api/v1`; the endpoints and their
answers are in [the API reference](../api/README.md).

- `GET /api/v1/me`: the user, the role, the token's `scopes`, and `capabilities`
  (`actions`, `push`, `oauth`, `approvals`, …). Read it after sign-in and after each refresh.
- `GET /api/v1/topics/{topic}` with `If-None-Match` answers 304 while the topic is unchanged.
- **Live updates:** `GET /api/v1/stream?topics=<a,b,…>` (at most 32 topics, else 400
  `invalid_input`) is server-sent events with the same `Authorization` header (OkHttp EventSource
  can set it; a browser `EventSource` cannot). Each event's `event:` is the topic and its `data:`
  the topic's envelope. Open it only in the foreground; in the background, updates come only
  through push.
- **Errors** are JSON `{error, code, requestId, details?}`; branch on `code`. A token without the
  scope a route needs gets 403 `insufficient_scope`; routes for a signed-in person (settings,
  tokens, consents) give a token 403 `forbidden`.

## Push

UnifiedPush only; there is no FCM. The app registers the endpoint its UnifiedPush distributor
hands it (for example an ntfy server) with the `push` scope:

1. `GET /api/v1/push/vapid` gives `publicKey`, the VAPID key for distributors that want it
   ([RFC 8292](https://www.rfc-editor.org/rfc/rfc8292)).
2. `POST /api/v1/push/subscriptions`:

   ```json
   {
     "endpoint": "https://ntfy.example.com/upAbCdEf",
     "keys": { "p256dh": "<P-256 public key, base64url>", "auth": "<16 bytes, base64url>" },
     "kind": "unifiedpush",
     "deviceName": "Pixel 9",
     "notify": { "approvals": true, "attention": "crit" }
   }
   ```

   201 with the subscription (`id` `sub_…`, `state: "pending"`); 200 when the user already holds
   that endpoint. 400 `invalid_input` for an endpoint that is not https or that dash may not post
   to (a private address outside `push.allowPrivate`: an ntfy on the LAN needs listing there).
   409 `push_limit` past 10 devices, 429 past 10 registrations a minute, 409 `push_unavailable`
   when push is off.
3. dash pushes a `verify` message carrying `code`, and nothing else until the code comes back. The
   app reads it and calls `POST /api/v1/push/subscriptions/{id}/verify {"code": "…"}`: 200 with
   `state: "verified"`; a wrong code is 422, the fifth deletes the device, and an unverified one is
   deleted after 10 minutes.
4. `POST /api/v1/push/subscriptions/{id}/test` sends a test push (202, one a minute);
   `DELETE /api/v1/push/subscriptions/{id}` removes the device.

Every push is encrypted end to end ([RFC 8291](https://www.rfc-editor.org/rfc/rfc8291),
`aes128gcm` only) with the device's `p256dh` and `auth`, at most 3 KiB of JSON:

```json
{ "type": "verify", "id": "sub_…", "title": "dash: verify this device", "body": "Code …",
  "url": "https://dash.example.com/settings/devices", "ts": "…", "code": "84P3GK7R" }
```

`type` is `verify`, `test`, `attention` or `approval`; `id` names what it is about (use it as the
notification's tag); `url` is the page a tap opens; `code` is present only on `verify`. A device
the app registered follows its grant: when the grant ends it turns stale and receives nothing, and
the app registers again after the next sign-in.

## Approvals (dash v0.14.10 and later)

When an AI agent asks to run an action, dash holds it for its owner's approval for 10 minutes
([action tools and approvals](../mcp.md#action-tools-and-approvals)). `capabilities.approvals` in
`/api/v1/me` says the server has approvals; an older server answers these routes 404.

- **Push:** each verified device of the owner with `notify.approvals` on receives
  `{"type": "approval", "id": "appr_…", "title": "dash: approve Restart?", "body": "Restart
  Deployment media/web - asked by agent. …", "url": "<publicURL>/approve/appr_…", "ts": "…"}`, with
  `Urgency: high`.
- **The pending list** is the user's own topic `core.approvals` (`{"pending": [<request>, …]}`,
  oldest first), from `GET /api/v1/topics/core.approvals` or the stream.
- `GET /api/v1/approvals/{id}`: the request: `state` (`pending`, `approved`, `denied`,
  `expired`), `credentialName` (the agent's token or connected app) and `clientName`, `plugin`,
  `action`, `title`, `summary`, `target {kind, namespace, name}`, the full `input`,
  `destructive`, `confirm` (`none`, `dialog` or `typeName`), `createdAt` and `expiresAt`; once
  decided `decidedAt` and `decidedBy`, a `reason` when dash denied it itself, and an `outcome`
  `{ok, result, truncated, error, at}` once it ran.
- `POST /api/v1/approvals/{id}/decision` with `{"approve": true}` or `{"approve": false}` and the
  `approvals` scope (403 `insufficient_scope` without it). When `confirm` is `typeName` (a
  protected namespace), approving needs `"confirm": "<target name>"`, typed by the person, never
  filled in by the app: without it the answer is 400 `invalid_input` ("type the name of the
  target … to approve"). The answer is the request as decided. Approving runs the action at once;
  an action that ran and failed stays `approved`, with `outcome.ok: false` and the `error`.
- Only the user the request belongs to may decide (403 for anyone else). A request decided already
  is 409 `conflict`; one that expired or that dash forgot at a restart is 410 `approval_expired`;
  503 `audit_unavailable` means nothing was decided.

Opening `url` in the browser instead shows the same request on the web UI's approval page.
