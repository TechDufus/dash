{{- define "dash.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "dash.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/* Cluster-scoped objects carry the namespace, so releases in two namespaces do not collide. */}}
{{- define "dash.clusterName" -}}
{{- printf "%s-%s" .Release.Namespace (include "dash.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "dash.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dash.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "dash.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "dash.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* serviceAccount.create=false without a name is refused in _validate.tpl. */}}
{{- define "dash.serviceAccountName" -}}
{{- default (include "dash.fullname" .) .Values.serviceAccount.name }}
{{- end }}

{{/*
The auth mode dash runs in: DASH_AUTH_MODE from env, else config.auth.mode, else proxy with the
sidecar and dash's default (local) without it.
*/}}
{{- define "dash.authMode" -}}
{{- $mode := dig "auth" "mode" (ternary "proxy" "local" (eq (include "dash.sidecar" .) "true")) .Values.config }}
{{- range .Values.env }}
{{- if and (eq .name "DASH_AUTH_MODE") (hasKey . "value") }}{{ $mode = .value }}{{ end }}
{{- end }}
{{- $mode }}
{{- end }}

{{/* repository:tag, plus @digest when one is set. */}}
{{- define "dash.imageRef" -}}
{{- $ref := printf "%s:%s" .image.repository (.image.tag | default .default) }}
{{- if .image.digest }}{{ $ref = printf "%s@%s" $ref .image.digest }}{{ end }}
{{- $ref }}
{{- end }}

{{/* "true" when the oauth2-proxy sidecar runs, empty otherwise (the helpers below follow this). */}}
{{- define "dash.sidecar" -}}
{{- if .Values.auth.oauth2ProxySidecar.enabled }}true{{ end }}
{{- end }}

{{/* server.listen, always explicit: the pod's loopback behind the sidecar, every address without it. */}}
{{- define "dash.listen" -}}
{{- if include "dash.sidecar" . }}127.0.0.1:8080{{ else }}0.0.0.0:8080{{ end }}
{{- end }}

{{- define "dash.anyAction" -}}
{{- if or .Values.rbac.actions.restart .Values.rbac.actions.fluxReconcile }}true{{ end }}
{{- end }}

{{/* auto: whenever an action can patch; true and false force it. */}}
{{- define "dash.guardrails" -}}
{{- $mode := toString .Values.guardrails.enabled }}
{{- if or (eq $mode "true") (and (eq $mode "auto") (include "dash.anyAction" .)) }}true{{ end }}
{{- end }}

{{/* The pod mounts a ServiceAccount token only when dash has something to do with the API. */}}
{{- define "dash.apiAccess" -}}
{{- if or .Values.rbac.read (include "dash.anyAction" .) .Values.rbac.secretRefs }}true{{ end }}
{{- end }}

{{/* The ingress rule follows the kubernetes plugin's setting that makes it list Ingresses. */}}
{{- define "dash.readIngresses" -}}
{{- if dig "plugins" "kubernetes" "apps" "ingress" false .Values.config }}true{{ end }}
{{- end }}

{{/*
The config file: .Values.config over the chart's defaults. With the sidecar dash trusts the
identity headers the proxy sets on the loopback.
*/}}
{{- define "dash.config" -}}
{{- $defaults := dict }}
{{- if include "dash.sidecar" . }}
{{- $defaults = dict "auth" (dict "mode" "proxy" "proxy" (dict "trustedProxies" (list "127.0.0.1/32" "::1/128"))) }}
{{- end }}
{{- toYaml (mergeOverwrite $defaults (deepCopy .Values.config)) }}
{{- end }}
