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
            - name: LEOFLOW_LOGS_DIR
              value: {{ .ctx.Values.config.logsDir | quote }}
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
              value: {{ .ctx.Values.agentTLS.caConfigMap | quote }}
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
            {{- if .ctx.Values.agentTLS.enabled }}
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
        {{- if .ctx.Values.agentTLS.enabled }}
        - name: grpc-tls
          secret:
            secretName: {{ required "agentTLS.serverCertSecret is required when agentTLS.enabled" .ctx.Values.agentTLS.serverCertSecret }}
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
