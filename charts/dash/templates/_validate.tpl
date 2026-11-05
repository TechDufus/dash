{{/*
Render-time refusals. deployment.yaml includes this first, so a refused value set renders
nothing. A message names the value at fault, never a secret.
*/}}
{{- define "dash.validate" -}}
{{- $v := .Values }}
{{- if not $v.secretKey.existingSecret }}
{{- fail "secretKey.existingSecret is required: create a Secret holding dash's secret key (openssl rand -base64 32) and name it here, with its key in secretKey.key" }}
{{- end }}
{{- if and (not $v.serviceAccount.create) (not $v.serviceAccount.name) }}
{{- fail "serviceAccount.create is false and serviceAccount.name is empty: name the ServiceAccount dash runs as (the RBAC is bound to it), or let the chart create one" }}
{{- end }}

{{- /* Replicas: each pod keeps its own data directory and sessions. */}}
{{- $mode := include "dash.authMode" . }}
{{- if gt (int $v.replicaCount) 1 }}
{{- if or (ne $v.settings.ui "readonly") $v.persistence.enabled }}
{{- fail "replicaCount > 1 needs settings.ui: readonly and persistence.enabled: false: each replica would keep its own settings and sessions" }}
{{- end }}
{{- if ne $mode "proxy" }}
{{- fail "replicaCount > 1 needs auth.mode proxy (auth.oauth2ProxySidecar.enabled, or config.auth.mode: proxy behind an identity proxy): in local and oidc mode each replica keeps its own session salt and setup token, so a request the Service sends to another replica is signed out" }}
{{- end }}
{{- end }}
{{- $allowRemote := dig "auth" "none" "allowRemote" false $v.config }}
{{- range $v.env }}
{{- if and (eq .name "DASH_AUTH_NONE_ALLOW_REMOTE") (hasKey . "value") }}{{ $allowRemote = eq (toString .value) "true" }}{{ end }}
{{- end }}
{{- if and (eq $mode "none") (not (include "dash.sidecar" .)) (not $allowRemote) }}
{{- fail "config.auth.mode none without auth.oauth2ProxySidecar.enabled: dash listens on the pod IP (0.0.0.0:8080), where the none mode refuses to start unless config.auth.none.allowRemote is true, and then every pod that reaches it is an administrator" }}
{{- end }}
{{- /* The checks above read the mode from config or a literal env value; nothing else may set it. */}}
{{- range $list := list "env" "secretEnv" }}
{{- range index $v $list }}
{{- if and (has .name (list "DASH_AUTH_MODE" "DASH_AUTH_MODE_FILE")) (or (eq $list "secretEnv") (not (hasKey . "value")) (eq .name "DASH_AUTH_MODE_FILE")) }}
{{- fail (printf "%s sets %s from a Secret or a file, which the chart's checks cannot read: set config.auth.mode, or DASH_AUTH_MODE with a literal value in env" $list .name) }}
{{- end }}
{{- end }}
{{- end }}

{{- /* What the chart sets itself, through the environment or the volume: one source each. */}}
{{- $server := $v.config.server | default dict }}
{{- if and (kindIs "map" $server) (hasKey $server "listen") }}
{{- fail "config.server.listen is set by the chart (0.0.0.0:8080, or 127.0.0.1:8080 with auth.oauth2ProxySidecar.enabled and without routes.direct): remove it from config" }}
{{- end }}
{{- if and (kindIs "map" $server) (hasKey $server "healthListen") }}
{{- fail "config.server.healthListen is set by the chart (0.0.0.0:8081, where the probes look): remove it from config" }}
{{- end }}
{{- $settings := $v.config.settings | default dict }}
{{- if and (kindIs "map" $settings) (hasKey $settings "ui") }}
{{- fail "config.settings.ui is set by the chart: use the chart value settings.ui (editable or readonly) instead" }}
{{- end }}
{{- if hasKey $v.config "dataDir" }}
{{- fail "config.dataDir is set by the chart (/data, the volume persistence configures): remove it from config" }}
{{- end }}
{{- $owned := dict
  "DASH_CONFIG" "use config"
  "DASH_SERVER_LISTEN" "it follows auth.oauth2ProxySidecar.enabled and routes.direct"
  "DASH_SERVER_LISTEN_FILE" "it follows auth.oauth2ProxySidecar.enabled and routes.direct"
  "DASH_SERVER_HEALTH_LISTEN" "the probes need 0.0.0.0:8081"
  "DASH_SERVER_HEALTH_LISTEN_FILE" "the probes need 0.0.0.0:8081"
  "DASH_SETTINGS_UI" "use settings.ui"
  "DASH_SETTINGS_UI_FILE" "use settings.ui"
  "DASH_SECRET_KEY" "use secretKey.existingSecret"
  "DASH_SECRET_KEY_FILE" "use secretKey.existingSecret"
  "DASH_DATA_DIR" "dash keeps its data on the /data volume (persistence)"
  "DASH_DATA_DIR_FILE" "dash keeps its data on the /data volume (persistence)" }}
{{- range $list := list "env" "secretEnv" }}
{{- range index $v $list }}
{{- if hasKey $owned .name }}
{{- fail (printf "%s sets %s, which the chart sets: %s" $list .name (index $owned .name)) }}
{{- end }}
{{- end }}
{{- end }}

{{- /*
routes.direct: a second host straight to dash's port beside the sidecar. dash then listens on the
pod IP and must believe identity headers from the loopback (the sidecar) only.
*/}}
{{- $direct := $v.routes.direct }}
{{- if $direct.enabled }}
{{- if not (include "dash.sidecar" .) }}
{{- fail "routes.direct.enabled needs auth.oauth2ProxySidecar.enabled: without the sidecar the Service already leads straight to dash" }}
{{- end }}
{{- if not $direct.host }}
{{- fail "routes.direct.enabled needs routes.direct.host: the host bearer clients reach dash on, past the identity proxy (it becomes server.directURL)" }}
{{- end }}
{{- if gt (int $v.replicaCount) 1 }}
{{- fail "routes.direct.enabled needs replicaCount 1: tokens and OAuth grants live in each pod's credentials.yaml, so one created through a replica is unknown to the others" }}
{{- end }}
{{- if eq $mode "none" }}
{{- fail "routes.direct.enabled with auth.mode none: dash would listen on the pod IP, and every client of the direct host would be an administrator" }}
{{- end }}
{{- if eq (int $v.service.port) (int (include "dash.directServicePort" .)) }}
{{- fail (printf "service.port %s is the Service port of routes.direct: choose another" (include "dash.directServicePort" .)) }}
{{- end }}
{{- if and (kindIs "map" $server) (hasKey $server "directURL") }}
{{- fail "config.server.directURL is set by the chart with routes.direct (https://<routes.direct.host>): remove it from config" }}
{{- end }}
{{- $proxy := dig "auth" "proxy" (dict) $v.config }}
{{- range $key := list "trustedProxies" "allowAnyPeer" }}
{{- if and (kindIs "map" $proxy) (hasKey $proxy $key) }}
{{- fail (printf "config.auth.proxy.%s is set by the chart with routes.direct (identity headers are believed from the loopback only, never from the direct host): remove it from config" $key) }}
{{- end }}
{{- end }}
{{- $directOwned := list "DASH_SERVER_DIRECT_URL" "DASH_SERVER_DIRECT_URL_FILE" "DASH_AUTH_PROXY_TRUSTED_PROXIES" "DASH_AUTH_PROXY_TRUSTED_PROXIES_FILE" "DASH_AUTH_PROXY_ALLOW_ANY_PEER" "DASH_AUTH_PROXY_ALLOW_ANY_PEER_FILE" }}
{{- range $list := list "env" "secretEnv" }}
{{- range index $v $list }}
{{- if has .name $directOwned }}
{{- fail (printf "%s sets %s, which the chart sets with routes.direct (the direct URL, and identity headers believed from the loopback only)" $list .name) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{- /* Secrets: references or Secrets only, never a literal in values. */}}
{{- $secretEnv := dict "DASH_SECRET_KEY_PREVIOUS" true }}
{{- range splitList "\n" (.Files.Get "files/secret-env.txt") }}
{{- if . }}{{ $_ := set $secretEnv . true }}{{ end }}
{{- end }}
{{- range $v.env }}
{{- if and (eq .name "DASH_SECRET_KEY_PREVIOUS") (hasKey . "value") (ne (toString .value) "") }}
{{- fail "env DASH_SECRET_KEY_PREVIOUS holds a literal: take it from a Secret (secretEnv), or mount the old key and set DASH_SECRET_KEY_PREVIOUS_FILE" }}
{{- else if and (hasKey $secretEnv .name) (hasKey . "value") }}
{{- $ref := and (kindIs "string" .value) (or (hasPrefix "env:" .value) (hasPrefix "file:" .value) (hasPrefix "k8s:" .value)) }}
{{- if not (or (eq (toString .value) "") $ref) }}
{{- fail (printf "env %s is a secret and holds a literal: take it from a Secret (secretEnv) or give an env:, file: or k8s: reference" .name) }}
{{- end }}
{{- end }}
{{- end }}
{{- range $v.auth.oauth2ProxySidecar.env }}
{{- if and (has .name (list "OAUTH2_PROXY_CLIENT_SECRET" "OAUTH2_PROXY_COOKIE_SECRET")) (hasKey . "value") }}
{{- fail (printf "auth.oauth2ProxySidecar.env %s holds a literal: take it from a Secret (valueFrom, or envFrom)" .name) }}
{{- end }}
{{- end }}
{{- range $v.auth.oauth2ProxySidecar.args }}
{{- $arg := toString . }}
{{- range $flag, $var := dict "--client-secret" "OAUTH2_PROXY_CLIENT_SECRET" "--cookie-secret" "OAUTH2_PROXY_COOKIE_SECRET" }}
{{- if or (eq $arg $flag) (hasPrefix (printf "%s=" $flag) $arg) }}
{{- fail (printf "auth.oauth2ProxySidecar.args sets %s: give %s through valueFrom in auth.oauth2ProxySidecar.env, or envFrom" $flag $var) }}
{{- end }}
{{- end }}
{{- end }}
{{- $schema := .Files.Get "files/config.schema.json" | fromJson }}
{{- include "dash.secretWalk" (dict "root" $schema "schema" $schema "value" $v.config "path" "config") }}
{{- include "dash.userinfoWalk" (dict "value" $v.config "path" "config") }}

{{- /* RBAC: a secretRefs entry without names would be a Role over every Secret of its namespace. */}}
{{- $namespaces := dict }}
{{- range $v.rbac.secretRefs }}
{{- if not .namespace }}
{{- fail "rbac.secretRefs has an entry without a namespace" }}
{{- end }}
{{- if hasKey $namespaces .namespace }}
{{- fail (printf "rbac.secretRefs lists namespace %s twice: put its names in one entry" .namespace) }}
{{- end }}
{{- $_ := set $namespaces .namespace true }}
{{- if not .names }}
{{- fail (printf "rbac.secretRefs entry for namespace %s needs names: without them its Role would grant every Secret of the namespace" .namespace) }}
{{- end }}
{{- $ns := .namespace }}
{{- range .names }}
{{- if not (and (kindIs "string" .) (trim .)) }}
{{- fail (printf "rbac.secretRefs entry for namespace %s needs names: each must be a non-blank Secret name (an empty one would grant every Secret of the namespace)" $ns) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Walks .Values.config beside dash's config schema (files/config.schema.json, written by
`make generate` from `dash config schema`) and fails on a literal in any x-dash-secret field.
Empty is unset; anything else must be an env:, file: or k8s: reference, exactly as dash reads it.
*/}}
{{- define "dash.secretWalk" -}}
{{- $s := .schema }}
{{- if hasKey $s "$ref" }}
{{- $s = index .root "$defs" (trimPrefix "#/$defs/" (index $s "$ref")) }}
{{- end }}
{{- $v := .value }}
{{- if hasKey $s "x-dash-secret" }}
{{- $ref := and (kindIs "string" $v) (or (hasPrefix "env:" $v) (hasPrefix "file:" $v) (hasPrefix "k8s:" $v)) }}
{{- if not (or $ref (eq (toString $v) "") (kindIs "invalid" $v)) }}
{{- fail (printf "%s is a secret and holds a literal: use env:NAME (secretEnv), file:/path (a mounted Secret, extraVolumes) or k8s:<namespace>/<name>#<key> (rbac.secretRefs)" .path) }}
{{- end }}
{{- else if kindIs "map" $v }}
{{- range $k, $child := $v }}
{{- $cs := dict }}
{{- if hasKey ($s.properties | default dict) $k }}
{{- $cs = index $s.properties $k }}
{{- else if kindIs "map" $s.additionalProperties }}
{{- $cs = $s.additionalProperties }}
{{- end }}
{{- include "dash.secretWalk" (dict "root" $.root "schema" $cs "value" $child "path" (printf "%s.%s" $.path $k)) }}
{{- end }}
{{- else if and (kindIs "slice" $v) (kindIs "map" $s.items) }}
{{- range $i, $child := $v }}
{{- include "dash.secretWalk" (dict "root" $.root "schema" $s.items "value" $child "path" (printf "%s[%d]" $.path $i)) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Fails on any string of config that is an absolute URL with a user name or password in it. */}}
{{- define "dash.userinfoWalk" -}}
{{- $v := .value }}
{{- if and (kindIs "string" $v) (regexMatch "^\\s*[A-Za-z][A-Za-z0-9+.-]*://[^/?#]*@" $v) }}
{{- fail (printf "%s is a URL with credentials in it (user:password@ or token@): give them through the plugin's auth settings with an env:, file: or k8s: reference" .path) }}
{{- else if kindIs "map" $v }}
{{- range $k, $child := $v }}
{{- include "dash.userinfoWalk" (dict "value" $child "path" (printf "%s.%s" $.path $k)) }}
{{- end }}
{{- else if kindIs "slice" $v }}
{{- range $i, $child := $v }}
{{- include "dash.userinfoWalk" (dict "value" $child "path" (printf "%s[%d]" $.path $i)) }}
{{- end }}
{{- end }}
{{- end }}
