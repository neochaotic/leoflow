{{/* Expand the name of the chart. */}}
{{- define "leoflow.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "leoflow.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "leoflow.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "leoflow.labels" -}}
helm.sh/chart: {{ include "leoflow.chart" . }}
{{ include "leoflow.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "leoflow.selectorLabels" -}}
app.kubernetes.io/name: {{ include "leoflow.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Role-aware selector labels (ADR 0049). Takes a dict {ctx, role}. For the "all"
role (the default, non-split monolith) this is EXACTLY leoflow.selectorLabels —
no extra label — so the single Deployment's immutable selector is unchanged and
`helm upgrade` from a pre-split install does not trip "field is immutable". For
the api/scheduler roles it adds app.kubernetes.io/component so each Deployment
selects only its own pods and Services can target one role.
*/}}
{{- define "leoflow.roleSelectorLabels" -}}
{{ include "leoflow.selectorLabels" .ctx }}
{{- if and .role (ne .role "all") }}
app.kubernetes.io/component: {{ .role }}
{{- end }}
{{- end -}}

{{/*
Role-suffixed resource name. "all" keeps the bare fullname (byte-identical to the
non-split Deployment/Service names); api/scheduler get a "-api"/"-scheduler"
suffix so the two Deployments, Services, SAs, etc. do not collide.
*/}}
{{- define "leoflow.roleName" -}}
{{- $full := include "leoflow.fullname" .ctx -}}
{{- if and .role (ne .role "all") -}}
{{- printf "%s-%s" $full .role | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $full -}}
{{- end -}}
{{- end -}}

{{- define "leoflow.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "leoflow.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding generated/inline credentials. */}}
{{- define "leoflow.secretName" -}}
{{- printf "%s-secrets" (include "leoflow.fullname" .) -}}
{{- end -}}

{{/* The image reference, defaulting the tag to the chart appVersion. */}}
{{- define "leoflow.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/* In-cluster gRPC address task pods dial, unless overridden. */}}
{{- define "leoflow.agentControlPlaneAddr" -}}
{{- if .Values.config.agentControlPlaneAddr -}}
{{- .Values.config.agentControlPlaneAddr -}}
{{- else -}}
{{- printf "%s.%s.svc.cluster.local:%d" (include "leoflow.fullname" .) .Release.Namespace (int .Values.ports.grpc) -}}
{{- end -}}
{{- end -}}
