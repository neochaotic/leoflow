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
suffixRole appends "-<role>" to a base name. It truncates the BASE to 53 first,
so even with a max-length base the result fits in 63 chars AND the "-api" /
"-scheduler" suffix survives — without this, a ~62-char base truncates the suffix
off and api and scheduler collapse to the same name (an install-time collision).
An empty or "all" role returns the base unchanged (byte-identical to non-split).
Takes {base, role}.
*/}}
{{- define "leoflow.suffixRole" -}}
{{- if and .role (ne .role "all") -}}
{{- printf "%s-%s" (.base | trunc 53 | trimSuffix "-") .role | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .base -}}
{{- end -}}
{{- end -}}

{{/*
Role-suffixed resource name. "all" keeps the bare fullname (byte-identical to the
non-split Deployment/Service names); api/scheduler get a "-api"/"-scheduler"
suffix so the two Deployments, Services, SAs, etc. do not collide. Takes {ctx, role}.
*/}}
{{- define "leoflow.roleName" -}}
{{- include "leoflow.suffixRole" (dict "base" (include "leoflow.fullname" .ctx) "role" .role) -}}
{{- end -}}

{{- define "leoflow.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "leoflow.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Role-aware ServiceAccount name (ADR 0049). Takes {ctx, role}. "all" keeps the
existing leoflow.serviceAccountName (honoring serviceAccount.create/name), so the
non-split chart is unchanged. api/scheduler each get their own SA named after the
role, so the api can carry a RESTRICTED identity (no pod-create/apiserver RBAC)
while the scheduler keeps the privileged one — the split's security payoff.
*/}}
{{- define "leoflow.roleServiceAccountName" -}}
{{- if and .role (ne .role "all") -}}
{{- /* Honor serviceAccount.name as the base (default fullname), then suffix by
role. So `serviceAccount.name=X` yields X-api / X-scheduler — the names an
operator using create=false must pre-provision (IRSA / Workload Identity). */ -}}
{{- $base := default (include "leoflow.fullname" .ctx) .ctx.Values.serviceAccount.name -}}
{{- include "leoflow.suffixRole" (dict "base" $base "role" .role) -}}
{{- else -}}
{{- include "leoflow.serviceAccountName" .ctx -}}
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

{{/*
In-cluster gRPC address task pods dial, unless overridden. When split.enabled
(ADR 0049) the agent gRPC endpoint lives on the scheduler Service, so task pods
must dial "<fullname>-scheduler", not the bare fullname (which in split mode is
the api Service and serves no gRPC).
*/}}
{{- define "leoflow.agentControlPlaneAddr" -}}
{{- if .Values.config.agentControlPlaneAddr -}}
{{- .Values.config.agentControlPlaneAddr -}}
{{- else -}}
{{- $svc := include "leoflow.fullname" . -}}
{{- if .Values.split.enabled -}}
{{- /* Use roleName (not a raw printf) so this DNS name matches the scheduler
Service exactly, including its trunc-63 — a long release name would otherwise
diverge and agents would dial a name no Service answers to. */ -}}
{{- $svc = include "leoflow.roleName" (dict "ctx" . "role" "scheduler") -}}
{{- end -}}
{{- printf "%s.%s.svc.cluster.local:%d" $svc .Release.Namespace (int .Values.ports.grpc) -}}
{{- end -}}
{{- end -}}
