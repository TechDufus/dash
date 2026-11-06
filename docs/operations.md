# Operating dash

Health probes, dash's own metrics, the secret key and the audit log. Deployment guides are under
[deploy](deploy/docker.md); the settings are in the [configuration reference](configuration.md).

## Health

`/healthz` answers `ok` while dash runs. `/readyz` answers `ok` once both listeners are bound and
every plugin's start has returned; a plugin in `error`, or an upstream that is down, never makes
dash unready, and neither does a kubernetes plugin that is `unavailable` because dash has no
cluster. It does not wait for the Kubernetes caches to fill: list `kubernetes` in
`server.readyRequires` to hold `/readyz` until the new pod has data, so a rollout keeps the old
pod serving. The hold lasts at most `server.readyTimeout` (60 s); after that dash reports ready
without the data and logs a WARN naming the plugins still starting. Each plugin listed must have
left `starting`.
`GET /readyz?verbose=1` adds one `<plugin> <state> [message]` line per plugin, but only for a
loopback peer (`kubectl port-forward`, a shell in the pod); any other peer gets the plain body.
Both answer on the health listener, `server.healthListen` (`0.0.0.0:8081`).

`dash healthcheck` is for a container `HEALTHCHECK`: it reads `server.healthListen` from the
environment and the config file as startup does (an unspecified host such as `0.0.0.0` or `::`
becomes loopback), GETs `/readyz` with a 2 s timeout, and exits 0 on 200 or 1 with one line on
stderr. It reads no secret and needs neither the cluster nor the data directory. It reads the
config file as it is now, so an edit of `server.healthListen` (which needs a restart) points the
probe at the new address before dash listens there: restart dash after changing it.

## Metrics

dash exposes metrics about itself, off by default. `server.metrics: true` (`DASH_SERVER_METRICS=true`)
makes the health listener serve `GET /metrics` in the Prometheus text format; with it off the path
answers 404. The registry is private: there are no Go or process collectors, only these series.

| Series | Labels |
|---|---|
| `dash_build_info` | `version`, `go_version` |
| `dash_http_requests_total`, `dash_http_request_duration_seconds` | `route` (the registered pattern, for example `GET /api/v1/topics/{topic}`; `static` for the web UI, `other` for what no route handled), `code` (counter only) |
| `dash_sse_streams` | `kind` (`events`, `tail`) |
| `dash_plugin_state` | `plugin`, `state`: 1 for the state an instance is in, 0 for the other seven |
| `dash_upstream_requests_total`, `dash_upstream_request_duration_seconds` | `plugin` (`prometheus`, `alertmanager`, `victorialogs`, `gatus`, `proxmox`), `outcome` (counter only: `ok`, `timeout`, `tls`, `auth`, `http_4xx`, `http_5xx`, `invalid_response`, `unreachable`) |
| `dash_actions_total` | `plugin`, `action`, `outcome` (`ok`, `error`, `denied`, `invalid`, `rate_limited`) |
| `dash_auth_failures_total` | `reason` (`unauthenticated`, `csrf`, `forbidden`, `rate_limit`, `namespace_denied`, `stream_limit`, `untrusted_peer`, `direct_host`, `ambiguous_identity`, `login_failed`, `login_throttled`, `invalid_token`, `ambiguous_credentials`, `insufficient_scope`) |

Every label value comes from a fixed set: a route is a pattern, never the path asked for, and no
series carries a user, an object or a host name. The `/metrics` endpoint is unauthenticated like
the probes, and enabling it shows plugin names and states to any peer that reaches the health
listener, which `/readyz` otherwise hides from everyone but loopback. Keep that listener
cluster-internal (a ClusterIP Service and a NetworkPolicy, not an Ingress). The Kubernetes
API calls of the informers are not counted, and `invalid_response` stays at 0
until the clients report it.

## The secret key

Secrets saved through the settings page are stored in `ui-settings.yaml` encrypted
(XChaCha20-Poly1305, the field path bound in, so a value copied to another field does not
decrypt). The key comes from the process environment only; there is no default and dash never
generates one. Without a key, secrets can only be set through env and file references, and saving
one from the UI is refused with an error that names `DASH_SECRET_KEY`. The rest of dash runs as
usual. The key also seals the sessions ([auth.md](auth.md#sessions)).

```sh
openssl rand -base64 32 > /run/secrets/dash-key   # 32 random bytes, base64
DASH_SECRET_KEY_FILE=/run/secrets/dash-key        # or DASH_SECRET_KEY=<the key>
```

Setting both `DASH_SECRET_KEY` and `DASH_SECRET_KEY_FILE` is an error. Keep the key where you
keep other secrets and back it up: a stored secret cannot be recovered without it. If the store
holds an encrypted value that the configured keys cannot open (no key, the wrong key, a damaged
value, a value moved to another field), dash does not start and lists every affected field. It
never drops, blanks or regenerates a stored secret.

To rotate the key:

1. Generate a new key and start dash with it as `DASH_SECRET_KEY` and the old one as
   `DASH_SECRET_KEY_PREVIOUS` (or `DASH_SECRET_KEY_PREVIOUS_FILE`). Both open stored values.
2. On that start dash re-encrypts every value that the previous key sealed, records
   `secrets.rekey` in the audit log and logs one INFO line. On a read-only store it logs a
   warning that the rekey is pending and keeps running on both keys.
3. Once the log shows the rekey, remove `DASH_SECRET_KEY_PREVIOUS` and restart.

When the settings page replaces or clears a secret dash also rewrites the retained
`ui-settings.yaml.bak.N` copies without the old ciphertext, so restoring a backup cannot bring a
cleared secret back.

## The audit log

Every action, settings change, silence change and stored-secret change is logged as an `audit`
record on stdout. When `dataDir` is writable dash also appends each record to `audit.jsonl` there: one
JSON line with the actor, what was done, the target and the result, never a secret or a changed
value (only the field paths). Opening a pod log tail is recorded as `logs.tail` with the pod as
the target. Refused requests (CSRF, unauthenticated, stream caps, a denied namespace) are recorded
too, at most once per peer and reason per minute. Each line carries the SHA-256 of the previous
one, so an edited, deleted or reordered line is detectable. At 10 MiB the file rotates to
`audit.jsonl.1` (newest) to `audit.jsonl.5` (oldest); older records are dropped.

API tokens add these records:

| Record | Action | Target | Actor |
|---|---|---|---|
| a token created | `token.create` (intent, then outcome) | `{kind: token, namespace: <owner>, name: <token ID>}` | the person who created it |
| a token revoked | `token.revoke` (intent, then outcome) | as above | the person who revoked it (the owner, or an administrator) |
| a request by a token | the action's own (an action, a log tail) | the action's | `type: token`, the owner (`token:<name>` for a static token), `tokenID`, `surface: api` |
| a refused token | the request line, kind `denial`, reason `invalid_token`, `ambiguous_credentials`, `insufficient_scope` or `forbidden` | none | `surface: api`; the token's owner when it resolved |

`authMode` is always the server's mode. No record holds a token's secret.

- **Without a writable data dir** (missing, read-only, or demo mode) the records go to stdout
  and the latest 500 records, plus 200 refusals, are kept in memory and shown on the audit
  page; dash logs one INFO line saying so.
- **One writer per data dir.** dash locks `.audit.lock` in `dataDir`; a second instance on the
  same directory refuses to start, even when its settings are read-only.
- **No record, no change.** An action, silence, settings or secret change first records its
  intent in the file. If that cannot be written the change is refused (actions and silences
  answer `503 audit log unavailable`, settings writes `503 audit_unavailable`) and nothing
  changes. If `audit.jsonl` itself cannot be opened in a writable data dir, dash does not start.

`dash audit verify [--dir DIR]` (default: the configured data directory) checks the chain
across the rotated files and lists intents that never got an outcome, such as an action in
flight when dash crashed; it exits 1 on a broken chain or a dangling intent. A last line without
a newline (a crash mid-write) is reported as a torn tail and does not fail the check; dash drops
it on the next start. The chain cannot see an edit to the very last line or a truncated tail.
