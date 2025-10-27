{{/*
Expand the name of the chart.
*/}}
{{- define "kgateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kgateway.fullname" -}}
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

{{/*
Strip version metadata (everything after and including '+') from Chart.Version.

This is useful with Flux, which appends the first part of the OCI artifact
digest after the '+' unless you use a non-default value for the
DisableChartDigestTracking feature gate. See issue #12728.

Example: v2.1.1+0c2bb8ac869b becomes v2.1.1
*/}}
{{- define "kgateway.chartVersionStripped" -}}
{{- regexReplaceAll "\\+.*$" .Chart.Version "" -}}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kgateway.chart" -}}
{{- printf "%s-%s" .Chart.Name (include "kgateway.chartVersionStripped" $) | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kgateway.labels" -}}
helm.sh/chart: {{ include "kgateway.chart" . }}
{{ include "kgateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kgateway.selectorLabels" -}}
kgateway: kgateway
app.kubernetes.io/name: {{ include "kgateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kgateway.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kgateway.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Validate validation level and return the validated value.
Supported values: "standard" or "strict" (case-insensitive).
*/}}
{{- define "kgateway.validationLevel" -}}
{{- $level := .Values.validation.level | lower | trimAll " " -}}
{{- if or (eq $level "standard") (eq $level "strict") -}}
{{- $level -}}
{{- else -}}
{{- printf "ERROR: Invalid validation.level '%s'. Must be 'standard' or 'strict' (case-insensitive). Current value: '%s'" $level .Values.validation.level | fail -}}
{{- end -}}
{{- end }}
