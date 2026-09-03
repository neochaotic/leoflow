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

{{/*
Name of the cluster-scoped TokenReview ClusterRole + ClusterRoleBinding. Cluster
scope means one global namespace for these names, so the name carries BOTH the
release name and the namespace it was installed into: two Leoflow releases in one
cluster must not fight over a single object (and a `helm uninstall` of one must
not revoke the other's permission). Kubernetes allows a 253-char DNS-subdomain
name here, so neither half needs truncating.
*/}}
{{- define "leoflow.tokenReviewName" -}}
{{- printf "%s-%s-tokenreview" (include "leoflow.fullname" .) .Release.Namespace -}}
{{- end -}}

{{/*
Guaranteed replica floor of the Deployment a PodDisruptionBudget would protect:
the smallest number of pods the chart promises to keep, not the largest it may
scale to. Non-split: replicaCount, or autoscaling.minReplicas when the HPA owns
the count (it never scales below that floor, and replicaCount is ignored). Split
(ADR 0049): the active-active api Deployment's count — the scheduler is a single
leader and is never covered by a PDB. The PDB auto mode keys on this value: a
budget over a floor of one blocks every voluntary eviction of that pod (node
drains and auto-upgrades stall), so it is only safe-by-default above one.
*/}}
{{- define "leoflow.controlPlaneReplicaFloor" -}}
{{- $floor := .Values.replicaCount -}}
{{- if .Values.split.enabled -}}
{{- $floor = .Values.split.api.replicaCount -}}
{{- end -}}
{{- if .Values.autoscaling.enabled -}}
{{- $floor = .Values.autoscaling.minReplicas -}}
{{- end -}}
{{- int $floor -}}
{{- end -}}

{{/*
Whether the PodDisruptionBudget renders. podDisruptionBudget.enabled is
tri-state: an explicit true/false wins (true on a single replica is the
operator's informed choice, and NOTES.txt says what it costs); unset (auto)
renders the PDB exactly when the guaranteed replica floor is above one, i.e.
when there is a second pod to keep serving while one is evicted.
*/}}
{{- define "leoflow.pdbEnabled" -}}
{{- $enabled := .Values.podDisruptionBudget.enabled -}}
{{- if kindIs "bool" $enabled -}}
{{- $enabled -}}
{{- else -}}
{{- gt (include "leoflow.controlPlaneReplicaFloor" . | int) 1 -}}
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding generated/inline credentials. */}}
{{- define "leoflow.secretName" -}}
{{- printf "%s-secrets" (include "leoflow.fullname" .) -}}
{{- end -}}

{{/*
agentTLS mode resolution (#690). Precedence, matching the three states the chart
supports:
  1. BYO      — serverCertSecret AND caConfigMap both set: the operator brings a
                cert-manager Certificate Secret + CA trust bundle. Use them verbatim.
  2. auto-gen — otherwise, when agentTLS.autoGenerate is true: the chart renders a
                stable self-signed CA + server cert (agent-tls-autogen.yaml) so a
                fresh cluster needs no cert-manager and no pre-created Secret (#690).
  3. none     — auto-gen off and nothing supplied: the deployment guard `fail`s.
leoflow.agentTLS.autoGenerated returns "true" only in case 2, so every template
that must branch on auto-gen keys off one predicate.
*/}}
{{- define "leoflow.agentTLS.autoGenerated" -}}
{{- if and .Values.agentTLS.enabled (not (and .Values.agentTLS.serverCertSecret .Values.agentTLS.caConfigMap)) .Values.agentTLS.autoGenerate -}}
true
{{- end -}}
{{- end -}}

{{/*
Effective name of the kubernetes.io/tls Secret holding the gRPC server cert. In
BYO mode it is the operator-supplied serverCertSecret; in auto-gen mode it is the
chart-generated "<fullname>-agent-tls" (agent-tls-autogen.yaml renders it under
this exact name). Keeping the name in one helper means the Deployment volume and
the generator can never disagree.
*/}}
{{- define "leoflow.agentTLS.serverCertSecretName" -}}
{{- if eq (include "leoflow.agentTLS.autoGenerated" .) "true" -}}
{{- printf "%s-agent-tls" (include "leoflow.fullname" .) -}}
{{- else -}}
{{- .Values.agentTLS.serverCertSecret -}}
{{- end -}}
{{- end -}}

{{/*
Effective name of the ConfigMap (key ca.crt) task pods mount to verify the server
cert. BYO: the operator-supplied caConfigMap; auto-gen: "<fullname>-agent-ca".
*/}}
{{- define "leoflow.agentTLS.caConfigMapName" -}}
{{- if eq (include "leoflow.agentTLS.autoGenerated" .) "true" -}}
{{- printf "%s-agent-ca" (include "leoflow.fullname" .) -}}
{{- else -}}
{{- .Values.agentTLS.caConfigMap -}}
{{- end -}}
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
