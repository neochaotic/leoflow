{{/*
Control-plane Deployment (ADR 0049), parameterized by role so one template
renders the monolith ("all") and the split api/scheduler Deployments. Takes a
dict {ctx, role, replicas}. The "all" role reproduces the pre-split Deployment
byte-for-byte (same name, selector, env, ports, probes); api/scheduler differ
only where the role demands (name suffix, component selector label, role env,
served ports, and the scheduler's health probes on the metrics port since it
serves no HTTP API).
*/}}
{{- define "leoflow.controlPlaneDeployment" -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "leoflow.roleName" (dict "ctx" .ctx "role" .role) }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "leoflow.labels" .ctx | nindent 4 }}
spec:
  replicas: {{ .replicas }}
  selector:
    matchLabels:
      {{- include "leoflow.roleSelectorLabels" (dict "ctx" .ctx "role" .role) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "leoflow.roleSelectorLabels" (dict "ctx" .ctx "role" .role) | nindent 8 }}
      annotations:
        # #316: tie the podTemplate hash to the rendered Secret so `helm
        # upgrade` rolls the pod whenever a Secret-backed value
        # (database.url, redis.url, auth.jwtSecret, secretKey,
        # bootstrap.password) changes. Without this annotation the Secret
        # is updated but K8s leaves the running pod on the OLD values
        # until a manual `kubectl rollout restart`. Note: this only covers
        # the chart-managed Secret; credentials wired via
        # `*.existingSecret` are outside the chart's visibility and still
        # require a manual restart on rotation (callout in chart README).
        checksum/secret: {{ include (print .ctx.Template.BasePath "/secret.yaml") .ctx | sha256sum }}
        {{- with .ctx.Values.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ include "leoflow.roleServiceAccountName" (dict "ctx" .ctx "role" .role) }}
      {{- with .ctx.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        {{- toYaml .ctx.Values.podSecurityContext | nindent 8 }}
      containers:
        - name: leoflow-server
          image: {{ include "leoflow.image" .ctx | quote }}
          imagePullPolicy: {{ .ctx.Values.image.pullPolicy }}
          securityContext:
            {{- toYaml .ctx.Values.securityContext | nindent 12 }}
          ports:
            {{- if or (eq .role "all") (eq .role "api") }}
            - name: http
              containerPort: {{ .ctx.Values.ports.http }}
            {{- end }}
            - name: metrics
              containerPort: {{ .ctx.Values.ports.metrics }}
            {{- if or (eq .role "all") (eq .role "scheduler") }}
            - name: grpc
              containerPort: {{ .ctx.Values.ports.grpc }}
            {{- end }}
          env:
            {{- if ne .role "all" }}
            # ADR 0049 — select this process's role. Omitted for the "all"
            # Deployment so its rendered env is identical to the pre-split chart.
            - name: LEOFLOW_SERVER_ROLE
              value: {{ .role | quote }}
            {{- end }}
            # Mark this deployment as the Pro edition. The control plane uses
            # this signal for two things: (a) refuse the
            # LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true dev escape hatch at boot
            # (#58 / ADR 0014) so a Pro install cannot accidentally ship secrets
            # over a plaintext gRPC channel; (b) inject the gold PRO badge into
            # the served SPA shell, mirroring Lite's silver LITE pill.
            - name: LEOFLOW_UI_EDITION
              value: "pro"
            - name: LEOFLOW_SERVER_HTTP_ADDR
              value: ":{{ .ctx.Values.ports.http }}"
            - name: LEOFLOW_SERVER_METRICS_ADDR
              value: ":{{ .ctx.Values.ports.metrics }}"
            - name: LEOFLOW_SERVER_GRPC_ADDR
              value: ":{{ .ctx.Values.ports.grpc }}"
            - name: LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR
              value: {{ include "leoflow.agentControlPlaneAddr" .ctx | quote }}
            # The namespace the control plane creates task pods + staging PVCs in.
            # MUST equal the namespace the executor Role/RoleBinding are granted in
            # (rbac.yaml, also .Values.taskNamespace) or every dispatch 403s (#480).
            - name: LEOFLOW_EXECUTOR_TASK_NAMESPACE
              value: {{ .ctx.Values.taskNamespace | quote }}
            {{- if .ctx.Values.config.trustedProxies }}
            # Proxy IPs/CIDRs whose X-Forwarded-For the server honors (#725).
            # Rendered comma-joined because the chart ships no server config file
            # and env is the only override path; viper's decode hook splits the
            # single env var back into server.trusted_proxies ([]string). Without
            # it the per-IP login limiter keys on the ingress IP and one client's
            # bad logins lock out every user. Omitted when empty (trust none).
            - name: LEOFLOW_SERVER_TRUSTED_PROXIES
              value: {{ join "," .ctx.Values.config.trustedProxies | quote }}
            {{- end }}
            {{- if .ctx.Values.executor.defaults.resources.cpu }}
            # L0 per-cluster CPU default (ADR 0023). The server applies it as both
            # request and limit → Guaranteed QoS for tasks that declare none (#725).
            - name: LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU
              value: {{ .ctx.Values.executor.defaults.resources.cpu | quote }}
            {{- end }}
            {{- if .ctx.Values.executor.defaults.resources.memory }}
            # L0 per-cluster memory default (ADR 0023), request == limit (#725).
            - name: LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_MEMORY
              value: {{ .ctx.Values.executor.defaults.resources.memory | quote }}
            {{- end }}
            - name: LEOFLOW_LOGS_DIR
              value: {{ .ctx.Values.config.logsDir | quote }}
            {{- if ne .ctx.Values.logs.sink.provider "disk" }}
            # Object-store log backend (opt-in, ADR 0035/0056 keyless-first). With
            # provider s3 or gcs, task logs ship to a bucket instead of the PVC; set
            # logs.persistence.enabled=false to drop the (RWX) log PVC entirely.
            # Keyless: bind this control-plane ServiceAccount to a cloud identity
            # (IRSA for s3, Workload Identity for gcs) via serviceAccount.annotations
            # and leave the credential fields empty.
            - name: LEOFLOW_LOGS_BACKEND
              value: {{ .ctx.Values.logs.sink.provider | quote }}
            - name: LEOFLOW_LOGS_SINK_BUCKET
              value: {{ .ctx.Values.logs.sink.bucket | quote }}
            - name: LEOFLOW_LOGS_SINK_PREFIX
              value: {{ .ctx.Values.logs.sink.prefix | quote }}
            {{- if eq .ctx.Values.logs.sink.provider "s3" }}
            - name: LEOFLOW_LOGS_SINK_REGION
              value: {{ .ctx.Values.logs.sink.region | quote }}
            - name: LEOFLOW_LOGS_SINK_ENDPOINT
              value: {{ .ctx.Values.logs.sink.endpoint | quote }}
            - name: LEOFLOW_LOGS_SINK_FORCE_PATH_STYLE
              value: {{ .ctx.Values.logs.sink.forcePathStyle | quote }}
            {{- if .ctx.Values.logs.sink.existingSecret }}
            - name: LEOFLOW_LOGS_SINK_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.logs.sink.existingSecret }}
                  key: accessKeyId
            - name: LEOFLOW_LOGS_SINK_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.logs.sink.existingSecret }}
                  key: secretAccessKey
            {{- end }}
            {{- end }}
            {{- end }}
            - name: LEOFLOW_SCHEDULER_ENABLED
              value: {{ .ctx.Values.config.scheduler.enabled | quote }}
            - name: LEOFLOW_SCHEDULER_LOOP_INTERVAL_MS
              value: {{ .ctx.Values.config.scheduler.loopIntervalMs | quote }}
            - name: LEOFLOW_DATABASE_MAX_OPEN_CONNS
              value: {{ .ctx.Values.database.maxOpenConns | quote }}
            - name: LEOFLOW_DATABASE_MAX_IDLE_CONNS
              value: {{ .ctx.Values.database.maxIdleConns | quote }}
            - name: LEOFLOW_AUTH_JWT_TOKEN_TTL_SECONDS
              value: {{ .ctx.Values.auth.tokenTtlSeconds | quote }}
            - name: LEOFLOW_OBSERVABILITY_LOG_FORMAT
              value: {{ .ctx.Values.observability.logFormat | quote }}
            - name: LEOFLOW_OBSERVABILITY_LOG_LEVEL
              value: {{ .ctx.Values.observability.logLevel | quote }}
            {{- if .ctx.Values.observability.otel.enabled }}
            - name: LEOFLOW_OBSERVABILITY_OTEL_ENABLED
              value: "true"
            - name: LEOFLOW_OBSERVABILITY_OTEL_ENDPOINT
              value: {{ .ctx.Values.observability.otel.endpoint | quote }}
            {{- end }}
            {{- if .ctx.Values.agentTLS.enabled }}
            # TLS on the agent gRPC channel (issue #58). The server cert is
            # mounted from agentTLS.serverCertSecret; the agent CA is delivered to
            # task pods via agentTLS.caConfigMap (the dispatcher mounts + selects it).
            - name: LEOFLOW_SERVER_GRPC_TLS_CERT
              value: /etc/leoflow/grpc-tls/tls.crt
            - name: LEOFLOW_SERVER_GRPC_TLS_KEY
              value: /etc/leoflow/grpc-tls/tls.key
            - name: LEOFLOW_EXECUTOR_AGENT_TLS_CA_CONFIGMAP
              value: {{ include "leoflow.agentTLS.caConfigMapName" .ctx | quote }}
            {{- end }}
            {{- if .ctx.Values.taskSecret.name }}
            # Mount a Kubernetes Secret read-only into every task pod so a task can
            # read a credential (e.g. a GCP service-account key referenced by a
            # connection's key_path) from the cluster's secret store — Leoflow
            # never stores the key itself (ADR 0035).
            - name: LEOFLOW_EXECUTOR_TASK_SECRET_NAME
              value: {{ .ctx.Values.taskSecret.name | quote }}
            - name: LEOFLOW_EXECUTOR_TASK_SECRET_MOUNT_PATH
              value: {{ .ctx.Values.taskSecret.mountPath | quote }}
            {{- end }}
            # Task-pod hardening. Always stamped, both directions: leaving the
            # secure value implicit would mean a chart upgrade could not turn an
            # opt-out back off without an operator noticing.
            - name: LEOFLOW_EXECUTOR_DEFAULTS_RUN_TASKS_AS_NON_ROOT
              value: {{ .ctx.Values.taskPodSecurity.runAsNonRoot | quote }}
            - name: LEOFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM
              value: {{ .ctx.Values.taskPodSecurity.readOnlyRootFilesystem | quote }}
            # Agent-credential posture (ADR 0055) and warm worker pools (ADR 0058).
            # These three are always stamped, at their defaults too, for the same
            # reason as the task-pod hardening above: they are the security
            # coupling ADR 0058 D2 rests on, so leaving a default implicit would
            # let a hand-applied `kubectl set env` survive a `helm upgrade`
            # unnoticed. Stamping them makes the chart the single source of truth
            # and lets an upgrade reassert the safe value.
            - name: LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT
              value: {{ .ctx.Values.auth.agentTokenTransport | quote }}
            - name: LEOFLOW_AUTH_SECRET_LIVENESS_MODE
              value: {{ .ctx.Values.auth.secretLivenessMode | quote }}
            - name: LEOFLOW_EXECUTION_WARM_POOLS_ENABLED
              value: {{ .ctx.Values.execution.warmPoolsEnabled | quote }}
            {{- if .ctx.Values.execution.warmPoolsEnabled }}
            # The D6/D9/M4 pool bounds. Stamped only with the flag on: the server
            # reads none of them while warm pools are off (its boot validation
            # returns before they are looked at), so an OFF install keeps exactly
            # the env it has today. Durations travel as strings — viper parses
            # them, the same way it does the auth credential ceiling.
            - name: LEOFLOW_EXECUTION_MIN_IDLE_WORKERS
              value: {{ .ctx.Values.execution.minIdleWorkers | quote }}
            - name: LEOFLOW_EXECUTION_MAX_POOL_SIZE
              value: {{ .ctx.Values.execution.maxPoolSize | quote }}
            - name: LEOFLOW_EXECUTION_MAX_ATTEMPTS_PER_WORKER
              value: {{ .ctx.Values.execution.maxAttemptsPerWorker | quote }}
            - name: LEOFLOW_EXECUTION_MAX_WORKER_LIFETIME
              value: {{ .ctx.Values.execution.maxWorkerLifetime | quote }}
            - name: LEOFLOW_EXECUTION_WORKER_IDLE_TTL
              value: {{ .ctx.Values.execution.workerIdleTtl | quote }}
            - name: LEOFLOW_EXECUTION_MAX_WARM_PODS_PER_TENANT
              value: {{ .ctx.Values.execution.maxWarmPodsPerTenant | quote }}
            {{- end }}
            - name: LEOFLOW_DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.database.existingSecret | default (include "leoflow.secretName" .ctx) }}
                  key: databaseUrl
            - name: LEOFLOW_REDIS_URL
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.redis.existingSecret | default (include "leoflow.secretName" .ctx) }}
                  key: redisUrl
            {{- if .ctx.Values.redis.caConfigMap }}
            # #312 — Verified TLS to managed Redis (Memorystore
            # SERVER_AUTHENTICATION, ElastiCache in-transit, Azure Cache).
            # The CA bundle from the named ConfigMap is mounted read-only at
            # /etc/leoflow/redis-ca/ca.crt and the server is told where to
            # find it via LEOFLOW_REDIS_CA_FILE.
            - name: LEOFLOW_REDIS_CA_FILE
              value: /etc/leoflow/redis-ca/ca.crt
            {{- end }}
            - name: LEOFLOW_AUTH_JWT_SECRET
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.auth.existingSecret | default (include "leoflow.secretName" .ctx) }}
                  key: jwtSecret
            {{- if or .ctx.Values.secretKeyExistingSecret .ctx.Values.secretKey }}
            # LEOFLOW_SECRET_KEY (ADR 0019) — Connection password / Extra
            # encryption-at-rest key. Without it, the API refuses Connection
            # writes (Variables still work). Optional so users who only use
            # Variables can omit it; recommended for any real Pro install.
            - name: LEOFLOW_SECRET_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.secretKeyExistingSecret | default (include "leoflow.secretName" .ctx) }}
                  key: secretKey
            {{- end }}
            {{- if or .ctx.Values.bootstrap.existingSecret .ctx.Values.bootstrap.password }}
            - name: LEOFLOW_BOOTSTRAP_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.bootstrap.existingSecret | default (include "leoflow.secretName" .ctx) }}
                  key: bootstrapPassword
            {{- end }}
            {{- if .ctx.Values.extraEnv }}
            # Operator escape hatch for LEOFLOW_* settings the chart does not
            # model as a value. Appended last, and never a way around a guard:
            # deployment.yaml refuses to render an entry that shadows one of the
            # variables whose values the chart validates against each other.
            #
            # Rendered field by field rather than dumped with toYaml so `value` is
            # always quoted: a Kubernetes env value is a string, and a YAML number
            # or bool (which is what `--set extraEnv[0].value=4` produces) is
            # rejected by the apiserver on apply — long after the render looked
            # fine. name/value/valueFrom is the whole of an EnvVar, so nothing is
            # dropped by being explicit.
            {{- range .ctx.Values.extraEnv }}
            - name: {{ .name | quote }}
              {{- if hasKey . "value" }}
              value: {{ .value | quote }}
              {{- end }}
              {{- if hasKey . "valueFrom" }}
              valueFrom:
                {{- toYaml .valueFrom | nindent 16 }}
              {{- end }}
            {{- end }}
            {{- end }}
          {{- $probePort := "http" }}
          {{- if eq .role "scheduler" }}{{ $probePort = "metrics" }}{{ end }}
          readinessProbe:
            httpGet:
              path: /readyz
              port: {{ $probePort }}
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /healthz
              port: {{ $probePort }}
            initialDelaySeconds: 10
            periodSeconds: 20
          resources:
            {{- toYaml .ctx.Values.resources | nindent 12 }}
          volumeMounts:
            - name: logs
              mountPath: {{ .ctx.Values.config.logsDir }}
            {{- if and .ctx.Values.agentTLS.enabled (ne .role "api") }}
            # #726 — the private key is mounted only into the role that runs the
            # agent gRPC server. The api role never builds a gRPC server
            # (startAgentGRPC is reached only from the scheduler side), so mounting
            # tls.key into its internet-facing pod only widens the blast radius
            # ADR 0049 set out to shrink. The env vars above stay on every role:
            # the Pro boot guard (guardTLSForEdition) checks only that both strings
            # are non-empty, never reading the files, so the dangling path is
            # harmless on api while scoping the env would CrashLoopBackOff it.
            - name: grpc-tls
              mountPath: /etc/leoflow/grpc-tls
              readOnly: true
            {{- end }}
            {{- if .ctx.Values.database.caConfigMap }}
            # Managed-Postgres CA bundle (#315). The DSN references this path
            # via `sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt` —
            # pgx reads sslrootcert natively, so no Go-side wiring is needed.
            - name: db-ca
              mountPath: /etc/leoflow/db-ca
              readOnly: true
            {{- end }}
            {{- if .ctx.Values.redis.caConfigMap }}
            - name: redis-ca
              mountPath: /etc/leoflow/redis-ca
              readOnly: true
            {{- end }}
      volumes:
        - name: logs
          {{- if .ctx.Values.logs.persistence.enabled }}
          persistentVolumeClaim:
            claimName: {{ include "leoflow.fullname" .ctx }}-logs
          {{- else }}
          # Dev-only fallback: logs vanish on pod restart. Enable
          # `logs.persistence.enabled` for durable storage (#227).
          emptyDir: {}
          {{- end }}
        {{- if and .ctx.Values.agentTLS.enabled (ne .role "api") }}
        # #726 — see the matching volumeMount guard above: the api role omits the
        # gRPC cert Secret volume entirely so tls.key never reaches its pod.
        - name: grpc-tls
          secret:
            # BYO serverCertSecret, or the chart-generated "<fullname>-agent-tls"
            # when auto-gen is active (#690). The helper resolves which; `required`
            # only trips in the impossible-by-guard case of neither (deployment.yaml
            # fails first with a clearer message).
            secretName: {{ required "agentTLS: no server cert Secret resolved (set agentTLS.serverCertSecret, or keep agentTLS.autoGenerate=true)" (include "leoflow.agentTLS.serverCertSecretName" .ctx) }}
        {{- end }}
        {{- if .ctx.Values.database.caConfigMap }}
        - name: db-ca
          configMap:
            name: {{ .ctx.Values.database.caConfigMap }}
            items:
              - key: ca.crt
                path: ca.crt
        {{- end }}
        {{- if .ctx.Values.redis.caConfigMap }}
        - name: redis-ca
          configMap:
            name: {{ .ctx.Values.redis.caConfigMap }}
        {{- end }}
      {{- with .ctx.Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .ctx.Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .ctx.Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}

{{- end -}}
