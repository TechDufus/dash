# Settings page and API

dash has a settings page for administrators (`/settings`) and the API it is built on
(`/api/v1/settings/**`). What the page saves goes into `ui-settings.yaml` in the data directory:
a sparse layer below the config file and the environment (see
[Configuration](configuration.md)). Whatever the file or the environment sets wins and
cannot be changed from the page. dash never writes the operator's config file.

## Who can use it

Every settings route is for an administrator signed in through the browser (a session, the
identity proxy, `none`, dev): a viewer gets 403, and so does an API token. The routes
are under the `Settings` tag of [the OpenAPI document](api/openapi.json). In demo mode every
settings route answers 404 `not_found` ("settings are not available in demo mode").

Writes are refused in two more cases:

- **A read-only store** (no or an unwritable data directory, or `settings.ui: readonly`): every
  write answers 409 `readonly_settings`, `DELETE …/stored/{field}` included. The pages still
  show every value with its provenance, the audit log, the restart notice and the export.
- **`auth.mode: none` on a listener that is not loopback** (it starts there only with
  `auth.none.allowRemote: true`): every write and every connection test answers 403
  `forbidden`. An unauthenticated administrator and a writable store would let anyone who
  reaches dash change where it sends credentials, and the test would make dash call any address.

## The pages

- **`/settings`** lists the plugins with their live state (it follows `core.plugins`), an
  enable switch per plugin and a link to its form. The general section shows `server.*`,
  `auth.*`, `settings.*` and `dataDir` read-only with their provenance; `ui.iconBaseURL` is the
  one general setting the page saves (it applies after a restart). A banner says when the
  settings are read-only and why. **Export as config.yaml snippet** downloads
  `dash-settings.yaml`. A link leads to the audit log.
- **`/settings/plugins/<id>`** is a form built from the plugin's JSON Schema. `type` is not
  shown. A field the file or the environment sets is read-only and names its source (`Locked:
  set by env DASH_PLUGINS_PROXMOX_URL`); on a switch or a select the lock is a focusable
  element with the same text. A field whose stored value the file or the environment hides says
  "overridden by env; stored value unused" and offers **Remove stored value**. Any other
  stored value offers **Reset to default**, which removes it from the store. A secret shows
  whether it is set and from where, never its value: **Replace** opens a password input that
  is saved (or tested) with the form, **Clear** asks once more and removes the stored secret. A
  secret the file or the environment sets offers neither. **Test connection** tries the form's
  pending changes without saving them, for the plugins that have a check.
- **`/settings/audit`** shows the audit log newest first, in two lists: `main` (actions,
  settings changes, events) and `unauth` (refused requests). **Load older** fetches the next
  page; the kind filter applies to the records already loaded. A line says where the records
  come from (`file` or `memory`).
- **`/settings/tokens`**, open to every signed-in user, lists their personal access tokens,
  creates one (shown once, with a copy button) and revokes one after a confirmation. An
  administrator also lists one user's or everyone's tokens and may grant `actions:<plugin>`.
  Without a writable data directory the page explains why tokens can't be created.

A viewer gets the navigation entry for the one settings page a viewer may use, **Tokens**
(personal access tokens): `/settings` shows them only that link. Any other settings URL shows
a viewer "Not authorised", and the page sends no settings request. The anonymous viewer of the
`local` mode and every user of the `none` mode have no tokens, so no entry at all.

Two notices sit above every page for administrators: **Restart required** lists the settings
that changed since dash started and apply only after a restart (it reads the settings every
30 s, when the tab regains focus and after a save), and the `anonymous_viewer` and `oidc_open`
warnings of `/api/v1/me`, which can be dismissed for the rest of the tab's session.

## Routes

| Route | What |
|---|---|
| `GET /api/v1/settings` | the revision, the store (`readonly`, `reason`, `dataDir`, `writable`, `secretKey`), `restartRequired`, the general fields and the plugin list |
| `GET /api/v1/settings/schema` | the JSON Schema 2020-12 of the configuration |
| `PUT /api/v1/settings/general` | a JSON merge patch relative to the root; only `ui.*` may be written; `If-Match` required |
| `GET /api/v1/settings/plugins/{id}` | a plugin's schema, every field with its provenance, the revision and `testable` |
| `PUT /api/v1/settings/plugins/{id}` | a JSON merge patch relative to `plugins.<id>`; `If-Match` required |
| `POST /api/v1/settings/plugins/{id}/test` | the connection check with `{patch, secrets}` from the form; nothing is saved; 10 a minute per user |
| `PUT /api/v1/settings/plugins/{id}/secrets/{field}` | `{"value": "…"}` (at most 8 KiB, stored encrypted) or `{"ref": "k8s:…"}`; `If-Match` optional |
| `DELETE /api/v1/settings/plugins/{id}/secrets/{field}` | clears a secret saved from the UI; `If-Match` optional |
| `DELETE /api/v1/settings/plugins/{id}/stored/{field}` | removes the store's value of a field, also one the file or the environment hides; `If-Match` optional |
| `GET /api/v1/settings/export` | the store's values as a `config.yaml` snippet (`text/yaml`) |
| `GET /api/v1/settings/audit?ring=main\|unauth&limit=&before=` | one page of the audit log, newest first: `{enabled, source, ring, records, next}` |

`{field}` is the dotted path inside `plugins.<id>` (`tokenSecret`, `auth.bearerToken`) as one
URL segment; an encoded `/` in it answers 404. `limit` is 1 to 500 (default 100). `before` is the
`next` of the previous page of the same ring; a cursor of the other ring is a 400.

### Provenance

Every field carries:

| Key | Meaning |
|---|---|
| `value` | the effective value; absent for a secret; a URL's `user:password@` is redacted |
| `set` | a value (for a secret: a secret) is configured, in any layer |
| `source` | `default`, `ui`, `file` or `env` |
| `ref` | the environment variable, the config file, or a secret's reference (`k8s:…`); never a value |
| `locked`, `lockedBy` | the page cannot change it: `file`, `env`, `ui-ineligible` or `readonly`, in that order of precedence |
| `stored` | the store holds a value for it |
| `shadowed` | the store holds a value that the file or the environment hides |
| `restart` | a change applies after a restart |

UI-ineligible are `server.*`, `auth.*`, `settings.ui`, `settings.secretNamespaces` and `dataDir`.
An unset `plugins.<id>.enabled` shows what runs: `value` is `true` unless the plugin's live row is
`disabled`, with `set: false` and `source: default`.

### Writes

Each write runs under the reloader's lock, so no reload tick can interleave: the checks, the
resolution of the candidate configuration, the destination rule below, the audit intent, one
store write, then a reload. If the audit intent cannot be written the answer is 503
`audit_unavailable` and nothing changes. If the reload refuses the new configuration, the store
gets its previous values back as a new revision (so the old `If-Match` is now stale), the
outcome is recorded as failed and the answer names the problem. Every successful write answers
`{"revision", "restartRequired"}`, both read inside the lock.

- **`If-Match: "<revision>"`** is required on the two `PUT` patches: without it 428
  `precondition_required`; a stale revision, or `*`, 412 `precondition_failed`. On the secret
  and stored routes it is optional and checked when sent.
- **A merge patch:** `null` resets a field to its default (removes it from the store).
- **Locks:** a field the file or the environment sets is 409 `locked_field` with
  `details {path, source, ref}`; a UI-ineligible one 409 `locked_field` with `source:
  ui-ineligible`.
- **References:** `env:` and `file:` cannot be saved from the page. A `k8s:` reference may name
  only dash's own namespace (`POD_NAMESPACE`, else the ServiceAccount's namespace file) or one
  listed in `settings.secretNamespaces`. A `k8s:` reference that names the same Secret key as
  one the file or the environment uses is 409 `locked_field` ("referenced by the operator's
  configuration").
- **URLs with credentials** (`https://user:pass@host`) in any value that is not a secret are
  refused ("credentials belong in the secret fields").
- **Secrets** are write-only and sealed with `DASH_SECRET_KEY`; without a key, saving one is 409
  `conflict` with `details.reason: no_secret_key`. Replacing or clearing one also removes its
  old ciphertext from the store's `.bak.N` backups. A secret in a `PUT` patch is accepted only
  with an edit of where the plugin's requests go (below); otherwise it is a field error, "use the
  secrets endpoint".

### Errors never repeat a value

A refused write is `{"error", "code", "requestId", "details"}`. `details.fields[]` lists
`{path, message}` for the paths the request sent; the message is fixed text, never the value. A
problem anywhere else is 422 `invalid_input` with `details.reason: invalid_configuration`; when
the store's values fail even without the request, the text is "the configuration file or
environment has an invalid value; fix it first". Field errors can come in two rounds: a value of
the wrong type stops the resolution before the other checks run.

### Where requests go

A stored secret is never sent somewhere other than where it was saved for.

- A plugin's **destination** is the scheme, host and port of each URL setting (a missing port
  is the scheme's default) and its `tls.caFile` and `tls.insecureSkipVerify`.
- **The page** asks for every set secret again on any change of a URL or TLS setting (compared
  as text) and sends them in the same `PUT`.
- **The server** requires every set secret in the patch when the destination changes; a secret
  the file or the environment sets makes that change 409 `locked_field` with
  `details.reason: secret_locked`. It accepts a secret in a `PUT` patch on any change of a URL
  or TLS setting's text.
- **Test connection** never sends a stored secret to a changed destination: it leaves the
  secret out of the test. A typed value for a secret the file or the environment sets is 409
  `locked_field`. The test changes neither the store nor the running configuration. A new
  `k8s:` reference has no value until it is saved, so its test reports `not_configured` (the
  page says to save first). The test's requests are not counted in the plugin's upstream
  metrics. Each test is audited as a `settings.test` event, a failed one with its failed
  categories.

### Export

`GET /api/v1/settings/export` writes the store's keys only, as YAML to merge into `config.yaml` or
the environment. A stored secret becomes a comment
(`# plugins.proxmox.tokenSecret: secret stored in dash; supply env:NAME or file:/path here`), and
a URL's credentials are redacted. On a read-only store without a data directory the export is
empty.

## Audit

The settings writes are recorded as `settings.set`, `settings.remove-stored`,
`settings.secret.replace` and `settings.secret.clear` (an intent and an outcome, with the field
paths, the actor, the target, the peer and the request id; never a value), and connection tests
as `settings.test` events. Without a data directory dash keeps the latest 500 records and,
separately, the latest 200 refusals in memory, and the audit page shows them; a kept record has
at most 32 field paths (then `+N more`) and an error of at most 256 bytes.

## Reloads

A save reloads the configuration at once. A secret that appears after startup in a plugin's
section needs no restart: every shipped plugin restarts in-process with it. A save that finds the
config file missing, empty or not YAML keeps the file settings last applied; the next checks
decide about the file.

## Known limits

- **The double fault:** a store write that lands with an error and whose revert fails too can
  leave a value in the store that is not in effect; the write is answered as failed.
- A half-written config file that still parses is applied until the next check.
- **Reset to default** removes the stored value through `DELETE …/stored/{field}`.
