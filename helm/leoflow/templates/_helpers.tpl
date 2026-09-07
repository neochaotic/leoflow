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
Largest number of control-plane pods that may mount the logs volume at once:
the ceiling, not the floor. Non-split: replicaCount, or autoscaling.maxReplicas
when the HPA owns the count. Split (ADR 0049): the scheduler writes logs and
every api replica reads them from the same volume, so the mounters are the api
replicas (or their HPA ceiling) plus the one scheduler. Both the single-writer
PVC render guard and the per-pod emptyDir warning key on this value, so a shape
one of them misses is a shape both miss — there is one definition.
*/}}
{{- define "leoflow.logMounterCeiling" -}}
{{- $n := .Values.replicaCount -}}
{{- if .Values.autoscaling.enabled -}}
{{- $n = .Values.autoscaling.maxReplicas -}}
{{- end -}}
{{- if .Values.split.enabled -}}
{{- $api := .Values.split.api.replicaCount -}}
{{- if .Values.autoscaling.enabled -}}
{{- $api = .Values.autoscaling.maxReplicas -}}
{{- end -}}
{{- $n = add (int $api) 1 -}}
{{- end -}}
{{- int $n -}}
{{- end -}}

{{/*
Whether the PodDisruptionBudget renders. podDisruptionBudget.enabled is
tri-state: an explicit true/false wins (true on a single replica is the
operator's informed choice, and NOTES.txt says what it costs — via
leoflow.pdbEnabledExplicit, so the warning follows every spelling this helper
honours); unset (auto) renders the PDB exactly when the guaranteed replica floor
is above one, i.e. when there is a second pod to keep serving while one is
evicted.

The explicit half accepts the STRING spellings of the two booleans as well as
real booleans, because a GitOps tool does not send booleans: Argo CD's
`helm.parameters` and `helm --set-string` pass every override as a string, and a
`kindIs "bool"` test alone read `"true"` as "not a bool" and fell through to
auto — so an operator who asked for a budget on a single replica got none, with
no diagnostic anywhere (#905). Anything else non-empty is a `fail` rather than
another silent fallback to auto: the whole failure mode here was a value that
looked accepted and did nothing.
*/}}
{{- define "leoflow.pdbEnabled" -}}
{{- $enabled := .Values.podDisruptionBudget.enabled -}}
{{- $auto := gt (include "leoflow.controlPlaneReplicaFloor" . | int) 1 -}}
{{- if kindIs "bool" $enabled -}}
{{- $enabled -}}
{{- else if kindIs "invalid" $enabled -}}
{{- $auto -}}
{{- else -}}
{{- $spelled := lower (toString $enabled) -}}
{{- if eq $spelled "" -}}
{{- $auto -}}
{{- else if eq $spelled "true" -}}
true
{{- else if eq $spelled "false" -}}
false
{{- else -}}
{{- /* %q over the raw interface garbles anything that is not a string: an
integer renders as a quoted rune (`'\x05'`) or a bad-verb error, and neither
names what the operator typed. Quote the string CONVERSION and add the kind, so
the message is legible for every value that can reach here (#905). */ -}}
{{- fail (printf "podDisruptionBudget.enabled must be a boolean, the string \"true\" or \"false\", or empty for auto (got %q, kind %s). It is tri-state: empty renders the PodDisruptionBudget exactly when the guaranteed replica floor is above one, true forces it on, false forces it off. The string spellings are accepted because Argo CD's helm.parameters and helm --set-string pass every override as a string; any other value is refused rather than silently falling back to auto, which would leave a budget the operator asked for unrendered with no diagnostic. See #905." (toString $enabled) (kindOf $enabled)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Whether podDisruptionBudget.enabled was set EXPLICITLY — "true" when it is, empty
when the release is in auto mode. It reports the SPELLING, not the YAML type: a
bool, or a non-empty value that lowercases to `true` or `false`, is explicit;
unset, null and empty are auto.

NOTES.txt needs this and `kindIs "bool"` will not do. Both of its budget branches
used to key on the value being a real bool, which was correct only while the
helper above ignored the string spellings. Once it honoured them, a
string-spelled `"true"` at a single replica rendered the budget and skipped the
warning that says what the budget costs — drains hanging on the one pod,
auto-upgrades stalling — so the Argo CD path this exists to serve got the trap
without the diagnostic, where before it got neither. The upgrade note has the
mirror bug: it attributes the budget to auto-selection ("replica floor > 1") over
a value the operator set by hand. One predicate, used by both branches (#905).

It deliberately does NOT validate: an unparseable value is `fail`ed by
leoflow.pdbEnabled, which every consumer of this predicate also renders, so
duplicating the refusal here would only risk the two disagreeing.
*/}}
{{- define "leoflow.pdbEnabledExplicit" -}}
{{- $enabled := .Values.podDisruptionBudget.enabled -}}
{{- if kindIs "bool" $enabled -}}
true
{{- else if kindIs "invalid" $enabled -}}
{{- else if has (lower (toString $enabled)) (list "true" "false") -}}
true
{{- end -}}
{{- end -}}

{{/*
The EXPLICIT deployment.strategy, trimmed and stringified once — empty means the
caller should auto-select. It is the one definition both the render
(_controlplane-deployment.tpl) and the single-writer RollingUpdate refusal
(deployment.yaml) read, so the value that is judged is the value that renders.

It is an allowlist for the same reason the access mode became one. The template
rendered this value VERBATIM into spec.strategy.type and the refusal compared it
EXACTLY, which left two holes on the same key:

  - `rollingupdate` matched neither, so it skipped the refusal AND the
    auto-selection, rendered as-is, and the apiserver rejected the install after
    a clean render — the defect class this chart refuses at render time.
  - `RollingUpdate ` — one trailing space — skipped the refusal too, but YAML
    strips a trailing space from a plain scalar, so the apiserver ACCEPTED it.
    That is worse: the operator gets exactly the Multi-Attach deadlock the
    refusal exists to prevent, reached THROUGH the refusal.

So: trim, allow only empty / RollingUpdate / Recreate, and fail anything else
rather than pass it to the apiserver (#905).

ASSUMES the chart renders no surge knob. Deployment.spec.strategy.rollingUpdate
(maxSurge / maxUnavailable) is deliberately absent, which is what makes
"RollingUpdate over a single-writer volume" unconditionally a deadlock: the
default maxSurge of 25% rounds up to one extra pod. A future PR that exposes
maxSurge makes this guard WRONG — `maxSurge: 0` rolls a single-writer volume
safely — and it must then gate the refusal on the surge being non-zero. The one
shape where an operator legitimately wants the surge today is a node-pinned
local volume, where both pods land on the same node and the volume is
re-attachable there; that operator can patch spec.strategy on the rendered
Deployment rather than have the chart weaken the guard for everyone.
*/}}
{{- define "leoflow.deploymentStrategy" -}}
{{- $raw := .Values.deployment.strategy -}}
{{- $strategy := "" -}}
{{- if not (kindIs "invalid" $raw) -}}
{{- $strategy = trim (toString $raw) -}}
{{- end -}}
{{- if not (has $strategy (list "" "RollingUpdate" "Recreate")) -}}
{{- fail (printf "deployment.strategy=%q (kind %s) is not a Deployment update strategy: set RollingUpdate, Recreate, or \"\" to let the chart auto-select (Recreate over a single-writer logs PVC, RollingUpdate otherwise). The value is rendered verbatim into spec.strategy.type and compared exactly, so an unrecognized spelling used to bypass both the single-writer RollingUpdate refusal and the auto-selection: the apiserver then rejected the install, or — for a spelling it accepts after trimming, like a trailing space — accepted the surge onto a volume only one pod can hold. See #905." (toString $raw) (kindOf $raw)) -}}
{{- end -}}
{{- $strategy -}}
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

{{/*
Whether this apiserver supports the native preStop `sleep` hook action, which is
gated on PodLifecycleSleepAction: BETA and on by default from 1.30, ALPHA and OFF
in 1.29, and not present in the API types at all before that. Below 1.30 the
apiserver nils the field and its own validation then rejects the empty
`preStop: {}` it is left with ("must specify a handler type"), so rendering the
hook onto an older cluster REJECTS the Deployment and hard-fails `helm install`
— it is not silently dropped. The project's floor is 1.27, so this is a render
gate rather than a `kubeVersion` in Chart.yaml, which would refuse to install on
1.27-1.29 outright; below 1.30 the install succeeds and keeps the pre-hook
behavior instead (#918).

Returns "true" only when the hook can be rendered. Both the renderer
(_controlplane-deployment.tpl) and the NOTES.txt warning that tells the operator
the hook was dropped ask THIS helper, so the two can never disagree about which
clusters get the hook.

Note the limits of a semver test, both documented on
`deployment.preStopSleepSeconds`: a `helm template | kubectl apply` pipeline on a
helm 3 client defaults its capabilities to v1.20.0 and so omits the hook even
against a modern cluster (safe direction — the sleep is an optimization, not a
correctness requirement); and this is a proxy for a feature gate, so a
self-managed 1.30+ apiserver started with
`--feature-gates=PodLifecycleSleepAction=false` renders the hook and rejects the
Deployment, because nothing in discovery reports gate state.
*/}}
{{- define "leoflow.preStopSleepHookSupported" -}}
{{- if semverCompare ">=1.30.0-0" .Capabilities.KubeVersion.Version -}}
true
{{- end -}}
{{- end -}}
