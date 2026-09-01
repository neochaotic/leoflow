---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /configuration.html
# --- end AUTO redirect aliases ---
title: Configuration
weight: 60
description: "The LEOFLOW_* environment variables and config keys for the server."
---

Two surfaces: **`leoflow.yaml`** (per-DAG, authoring) and **server environment**
(`LEOFLOW_*`, the control plane). The canonical `leoflow.yaml` schema is
[`docs/api/leoflow-yaml-schema.json`](https://github.com/neochaotic/leoflow/blob/main/docs/api/leoflow-yaml-schema.json).

{{% alert title="Deploying Pro on Kubernetes?" color="info" %}}
These `LEOFLOW_*` variables are what the Helm chart sets under the hood. For the
chart's own values (image, replicas, ingress, Postgres/Redis wiring), see the
[Helm chart](/operate/helm-chart/) page and its full values reference.
{{% /alert %}}

## leoflow.yaml

| Key | Type | Notes |
|---|---|---|
| `dag_id` *(required)* | string | Unique DAG id (`^[A-Za-z0-9_][A-Za-z0-9_-]{0,199}$`). |
| `description`, `owner`, `tags` | string / string / list | Metadata. |
| `python_version` | `3.10`\|`3.11`\|`3.12` | Base image Python (default 3.11). |
| `base_image` | string | Override the runtime base image. |
| `dependencies` | list | pip specifiers baked into the image. |
| `connectors` | list | Short connector names (`postgres`, `http`, …) expanded at compile to their `apache-airflow-providers-*` packages. Sugar over `dependencies` — see [Installing a connector's provider](/connections/#installing-a-connectors-provider). |
| `system_packages` | list | apt packages. |
| `dag_source` | string | DAG file (default `dag.py`). |
| `build`, `registry` | object | Image build + push settings. |
| `defaults` | object | DAG-level `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `resources`. |
| `staging` | object | Opt-in per-run RWX volume: `enabled`, `size`, `storage_class` (ADR 0022). |
| `tasks.<task_id>` | object | Per-task overrides (ADR 0023): `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `env`, `resources`, `execution`. |

See [DAG authoring](/author-dags/dag-authoring/) for the override layers.

### Defaults

Every field in `leoflow.yaml` is optional. Zero-valued fields are filled by
`LeoflowConfig.ApplyDefaults()` (`internal/domain/config.go`) from the values
declared in [`leoflow-yaml-schema.json`](https://github.com/neochaotic/leoflow/blob/main/docs/api/leoflow-yaml-schema.json).
Defaults are hardcoded for v1; making them workspace-configurable is a v2
roadmap item.

| Field | Default | Notes |
|---|---|---|
| `schema_version` | `"1.0"` | Stamps every artifact for forward-compat. |
| `dag_id` | *subdir basename* | If `leoflow.yaml` is absent, the parent directory name is used. Two subdirs resolving to the same `dag_id` is a hard error — see [Discovery rules](/author-dags/dag-authoring/#discovery-rules). |
| `python_version` | `"3.11"` | Pick `3.10`, `3.11`, or `3.12`. |
| `dag_source` | `"dag.py"` | DAG file relative to the project. |
| `dependencies` | `[]` | pip specifiers baked into the image. |
| `connectors` | `[]` | Short connector names expanded to provider packages at compile (ADR 0038). |
| `system_packages` | `[]` | apt packages. |
| `include_paths` | `["."]` | Files copied into the image. |
| `exclude_paths` | `[".git", "__pycache__", "*.pyc", ".venv", "venv"]` | Skipped both in image build **and** workspace discovery. Hidden directories (`.*`) are skipped as well. |
| `build.context` | `"."` | Docker build context. |
| `build.platforms` | `["linux/amd64"]` | Multi-arch via `["linux/amd64","linux/arm64"]`. |
| `registry.auth_method` | `"docker_config"` | Credential source for `compile --push`. |
| `registry.tag_strategy` | `"version"` | How `dag_version` is mapped to image tag. |
| `staging.enabled` | `false` | Opt-in per-run RWX volume — ADR 0022. |
| `defaults.*` | *unset* | DAG-level task defaults; layered under task overrides — ADR 0023. |
| `tasks.<id>` | *unset* | Per-task overrides; must reference a `task_id` present in the compiled DAG. |

## Server environment (`LEOFLOW_*`)

This page is hand-maintained against the server's configuration struct and
default map in
[`internal/config/server.go`](https://github.com/neochaotic/leoflow/blob/main/internal/config/server.go)
— treat that source as the final authority. Every `LEOFLOW_*` variable maps to a
config key by upper-casing it and replacing `.` (and `-`) with `_`: e.g.
`auth.oidc.client_id` → `LEOFLOW_AUTH_OIDC_CLIENT_ID`. The same keys can be set
in a YAML config file or the Helm chart's `values.yaml`.

Values resolve in increasing order of precedence — a later source overrides an
earlier one:

```mermaid
flowchart LR
  D["Built-in defaults<br/>(serverDefaults)"] --> C["Config file<br/>(YAML)"]
  C --> E["LEOFLOW_* env vars"]
  E --> F["CLI flags"]
```

The **Edition** column reads `both` (Lite and Pro), `Pro` (Pro / Kubernetes
topologies only), or `dev-only`. `leoflow lite` sets the dev-appropriate values
automatically (isolated DB, port 8088, admin login on, no Redis).

Map- and list-valued keys (CORS origins, OIDC role/tenant maps, allowed email
domains, …) are set from a config file or Helm values, not from a single env
var — viper does not split one env var into a map or list.

### Server (`server.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_SERVER_ROLE` | `all` | Pro | Which components this process runs ([ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/)): `all` (default — the Lite monolith; every component in one process), `api` (HTTP + UI only, restricted identity), or `scheduler` (reconciler + dispatch + agent gRPC, privileged). Empty defaults to `all`, which is behavior-identical to the pre-split monolith; splitting is a Pro-only topology. |
| `LEOFLOW_SERVER_HTTP_ADDR` | `0.0.0.0:8080` | both | HTTP/UI listener. |
| `LEOFLOW_SERVER_GRPC_ADDR` | `0.0.0.0:9091` | both | Agent gRPC listener. |
| `LEOFLOW_SERVER_METRICS_ADDR` | `0.0.0.0:9090` | both | Prometheus metrics. |
| `LEOFLOW_SERVER_GRPC_TLS_CERT` | _(empty)_ | Pro | PEM cert enabling TLS on the agent gRPC listener (#58). Set with `_KEY`; empty means plaintext (dev). The Pro Helm chart requires both (see [Pro TLS](/operate/pro-tls/)). |
| `LEOFLOW_SERVER_GRPC_TLS_KEY` | _(empty)_ | Pro | PEM private key paired with `LEOFLOW_SERVER_GRPC_TLS_CERT`. Both must be set together to encrypt the agent channel. |
| `LEOFLOW_SERVER_CORS_ALLOWED_ORIGINS` | `http://localhost:8080` | both | List of browser origins allowed to call the API (`server.cors.allowed_origins`). Set via config file / Helm values (a list). |
| `LEOFLOW_SERVER_TRUSTED_PROXIES` | *(empty — trust none)* | both | Proxy IPs/CIDRs whose `X-Forwarded-For` is honored for the client IP (`server.trusted_proxies`, a list). See note below. |

### Database (`database.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_DATABASE_URL` | `postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable` | both | Postgres DSN. |
| `LEOFLOW_DATABASE_MAX_OPEN_CONNS` | `25` | both | Max open connections in the Postgres pool. |
| `LEOFLOW_DATABASE_MAX_IDLE_CONNS` | `5` | both | Max idle connections retained in the pool. |

### Redis (`redis.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_REDIS_URL` | _(empty)_ | **Pro only** | Redis URL (XCom + locks). Empty selects the embedded Lite edition — XCom on Postgres, in-process log tailer ([ADR 0026](/project/adrs/0026-lite-datastore-no-redis/)). Pro sets it via the Helm chart. |
| `LEOFLOW_REDIS_CA_FILE` | _(empty)_ | Pro | Absolute path to a PEM CA bundle trusted when negotiating TLS to a `rediss://` URL (#312). Needed for managed Redis (Memorystore, ElastiCache in-transit encryption, Azure Cache) whose server cert is signed by a provider/per-instance CA not in the container's system roots. Empty falls back to system roots only. The Helm chart sets it when `redis.caConfigMap` is configured. |

### Auth (`auth.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_AUTH_PROVIDER` | `jwt` | both | Credential authenticator: `jwt` (default — username/password issues an HS256 token) or `oidc` (adds the OIDC/SSO login flow on top; the JWT authenticator stays the request-path verifier in both modes). `oidc` is Pro-gated and fails boot closed unless its prerequisites are met (see [OIDC / SSO](#oidc--sso-authoidc)). |
| `LEOFLOW_AUTH_JWT_SECRET` | — *(required)* | both | Signs API/agent tokens. Required for both `jwt` and `oidc` (both mint the app's own HS256 token). |
| `LEOFLOW_AUTH_JWT_TOKEN_TTL_SECONDS` | `3600` | both | Lifetime, in seconds, of an issued API token. |
| `LEOFLOW_AUTH_LOGIN_RATE_LIMIT_PER_MINUTE` | `5` | both | Cap on **failed** `/auth/token` attempts per client IP per minute (anti-brute-force). A successful login consumes no budget. `leoflow lite` raises this well above the default (local single-user tool). |
| `LEOFLOW_SECRET_KEY` | — | both | 32-byte key encrypting connection secrets at rest ([ADR 0019](/project/adrs/0019-secret-encryption-at-rest/)). Raw 32 chars, 64-char hex, or base64. Empty disables connection writes. |
| `LEOFLOW_AUTH_SECRET_SCOPING` | `permissive` | both | Scope-by-declaration policy ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `permissive` (delivers the whole tenant vault; warns when a DAG declares a narrower set), `enforce` (delivers only the declared subset — empty declaration ⇒ nothing), or `off` (no scoping). Operator-scoped, never author-settable. |
| `LEOFLOW_AUTH_SECRET_LIVENESS_MODE` | `observe` | both | Gates secret delivery on task-instance liveness ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `observe` (logs + audits a would-have-denied when the caller's task instance is not live, but still delivers) or `enforce` (denies). Liveness renewal is always on regardless of mode; this only chooses whether a not-live token is refused. Required to be `enforce` when warm pools are on. Helm: `auth.secretLivenessMode`. |
| `LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT` | `envvar` | Pro (K8s) | How the in-pod agent obtains its control-plane bearer credential ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `envvar` (plaintext `LEOFLOW_AGENT_TOKEN` on the pod spec — today's behavior, byte-identical) or `exchange` (projected ServiceAccount token exchanged once via a control-plane `TokenReview` for a task-scoped JWT — nothing secret on the pod object; requires cluster-scoped `create` on `authentication.k8s.io/tokenreviews`). Operator-scoped. Prerequisite for warm pools. Ignored by the subprocess (Lite) executor. See [Agent credential transport](/operate/agent-credential-transport/). Helm: `auth.agentTokenTransport`. |
| `LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME` | `24h` | both | Duration ceiling on how long one attempt's agent credential may be kept alive by heartbeat renewal ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)). A runaway-task backstop — the short per-attempt TTL is what bounds a stolen token. A non-positive value disables the ceiling. |
| `LEOFLOW_AUTH_DEV_NO_AUTH` | `false` | dev-only | Legacy escape hatch — bypasses auth entirely, treating every request as admin. Permitted only on a loopback `http_addr` (boot fails otherwise). Modern Lite uses a real admin login generated by `leoflow setup`; set this only for ephemeral test scaffolds. |

### OIDC / SSO (`auth.oidc.*`)

Read only when `LEOFLOW_AUTH_PROVIDER=oidc`, which is Pro-gated (`ui.edition:
pro`) and fails boot closed unless `issuer`, `client_id`, and `redirect_url` are
all set. Verification is keyless — the ID token is validated against the
issuer's public JWKS — so no secret is stored for the verify path.

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_AUTH_OIDC_ISSUER` | _(empty)_ | Pro | The org's single-tenant issuer URL (must be `https://`). Pinned: any ID token whose `iss` differs is rejected. |
| `LEOFLOW_AUTH_OIDC_CLIENT_ID` | _(empty)_ | Pro | Registered application (client) id; the expected audience of every ID token. |
| `LEOFLOW_AUTH_OIDC_CLIENT_SECRET` | _(empty)_ | Pro | Used only for the authorization-code exchange. Inject via env; never persist it in a config file, never logged. |
| `LEOFLOW_AUTH_OIDC_REDIRECT_URL` | _(empty)_ | Pro | This server's callback URL registered with the IdP (`…/api/v2/auth/oidc/callback`). Must be `https://` (http allowed only for loopback hosts). |
| `LEOFLOW_AUTH_OIDC_SCOPES` | `openid, email, profile` | Pro | OAuth scopes requested (a list; set via config file / Helm values). Add the IdP's groups scope when group→role mapping is used. |
| `LEOFLOW_AUTH_OIDC_GROUPS_CLAIM` | `groups` | Pro | The ID-token claim carrying the user's IdP groups; its values drive `role_mappings`. |
| `auth.oidc.role_mappings` | _(empty map)_ | Pro | Maps an IdP group value → an existing Leoflow role name. **Default-DENY** — an unmapped group grants no role. Config file / Helm values only (a map does not bind from one env var). |
| `LEOFLOW_AUTH_OIDC_DEFAULT_ROLE` | _(empty)_ | Pro | When an authenticated user resolves to zero mapped roles and this is set, grants this single role (advised: a read-only role such as `viewer`). Empty keeps strict default-deny. Must name an existing DB role for the resolved tenant. |
| `LEOFLOW_AUTH_OIDC_TENANT_CLAIM` | _(empty)_ | Pro | Which IdP claim identifies the tenant: `tid` (Entra) or `hd` (Google Workspace). |
| `auth.oidc.tenant_claims` | _(empty map)_ | Pro | Maps a `tenant_claim` value → a Leoflow tenant name. A value not present is rejected (403); the login never falls back to `default`. Config file / Helm values only (a map). |
| `auth.oidc.allowed_email_domains` | _(empty)_ | Pro | Login-level allowlist layered on TOP of the `tid`/`hd` tenant pin (not the pin itself). Empty imposes no domain restriction. Non-empty admits a login only when the verified email's domain is in the list. Config file / Helm values (a list). |
| `auth.oidc.break_glass_emails` | _(empty)_ | Pro | Allowlist of local password logins permitted while provider is `oidc`; every other password login is rejected (SSO-only). Config file / Helm values (a list). |
| `LEOFLOW_AUTH_OIDC_JIT_PROVISIONING` | `false` | Pro | Create a user row on first OIDC login when none matches. Off by default (pre-provisioned user required); when on, the new row is granted the roles from `role_mappings`. |
| `LEOFLOW_AUTH_OIDC_CLOCK_SKEW_SECONDS` | `60` | Pro | Tolerance (seconds) on the ID token's `exp`/`iat`/`nbf` checks to absorb clock differences between the IdP and this server. |

### Scheduler (`scheduler.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_SCHEDULER_ENABLED` | `true` | both | Whether this process runs the scheduler loop. |
| `LEOFLOW_SCHEDULER_LOOP_INTERVAL_MS` | `1000` | both | Scheduler tick interval, in milliseconds. |
| `LEOFLOW_SCHEDULER_DISPATCH_BUFFER_SIZE` | `0` | both | Depth of the queued-dispatches channel ([ADR 0031](/project/adrs/0031-scheduler-architecture/), #127). `0` keeps dispatch synchronous with the tick (right for Lite); `>0` enables the worker pool (right for Pro, where K8s API calls add latency). |
| `LEOFLOW_SCHEDULER_DISPATCH_WORKERS` | `0` | both | Goroutines draining the dispatch queue. Ignored when buffer size ≤ 0; otherwise floored to 1. |

### Executor (`executor.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_EXECUTOR_TYPE` | `kubernetes` | both | Pod-path executor: `kubernetes` (default, pod-per-task) or `subprocess` (dev only — runs the agent on the host without isolation; `leoflow lite`/`leoflow dev` set it). |
| `LEOFLOW_EXECUTOR_TASK_NAMESPACE` | `leoflow` | Pro | Kubernetes namespace the server creates task pods and per-run staging PVCs in. MUST match the namespace the Helm chart grants the executor Role in (chart `taskNamespace`); a mismatch 403s every dispatch (#480). |
| `LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` | _(empty → `server.grpc_addr`)_ | both | gRPC address task pods dial back to. In a local k3d/kind cluster set it to a host-reachable address such as `host.k3d.internal:9091`. |
| `LEOFLOW_EXECUTOR_AGENT_TLS_CA_CONFIGMAP` | _(empty)_ | Pro | Names a ConfigMap (key `ca.crt`) mounted into task pods so the agent verifies the control plane's gRPC TLS cert (#58). Empty = agents use the insecure channel (dev). |
| `LEOFLOW_EXECUTOR_TASK_SECRET_NAME` | _(empty)_ | Pro | Names a Kubernetes Secret mounted read-only into every task pod, so a task can read a cluster-stored credential (e.g. a GCP SA key) referenced by a connection's `key_path` ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). Empty = no secret mounted. |
| `LEOFLOW_EXECUTOR_TASK_SECRET_MOUNT_PATH` | `/etc/leoflow/secrets` | Pro | Where `LEOFLOW_EXECUTOR_TASK_SECRET_NAME` is mounted in the task pod. |
| `LEOFLOW_EXECUTOR_TASK_SERVICE_ACCOUNT` | _(empty)_ | Pro | ServiceAccount task pods run as when a DAG does not set `execution.service_account`. The Helm chart wires this from `taskServiceAccount.name` when `taskServiceAccount.create: true`, so creating the task SA is enough for keyless secret access — no per-DAG opt-in. An explicit per-task `execution.service_account` still wins; empty leaves pods on the namespace default SA. |
| `LEOFLOW_EXECUTOR_AGENT_PATH` | `leoflow-agent` | dev-only | The leoflow-agent binary the subprocess executor runs. |
| `LEOFLOW_EXECUTOR_SUBPROCESS_WORKDIR` | _(empty)_ | dev-only | Working directory the subprocess executor runs the agent in (so it can import the project's `dag.py`). Empty keeps the server's working directory. |
| `LEOFLOW_EXECUTOR_HTTP_USER_AGENT` | `leoflow/0.1` | both | Default `User-Agent` header for HTTP requests a task image may make on the platform's behalf. |

### Executor task defaults (`executor.defaults.*`)

Lowest-precedence (L0) per-cluster task defaults, applied at dispatch to fill
gaps the DAG artifact left empty ([ADR 0023](/project/adrs/0023-dag-authoring-config-binding/)).
They never override a value baked into `dag.json`, keeping the artifact portable
across clusters.

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_EXECUTOR_DEFAULTS_RUN_TASKS_AS_NON_ROOT` | `true` | Pro | **Refuses to start a task container whose image resolves to UID 0**, completing Pod Security Admission's `restricted` set. On by default — the images this repo ships carry a numeric non-root UID (`USER 65532:65532`) and the executor pairs it with a pod-level `fsGroup` so the staging PVC stays writable. Turn it off only for a cluster whose task images legitimately run as root. Operator-scoped (never a DAG field). |
| `LEOFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM` | `false` | Pro | Mounts every task container's root filesystem read-only. Off by default (`restricted` does not require it and it breaks ordinary Python tasks — pip cache, `/tmp`, matplotlib config). Turn on for a fleet of tasks known not to write outside their volumes. |
| `LEOFLOW_EXECUTOR_DEFAULTS_STAGING_ACCESS_MODE` | `ReadWriteMany` | Pro | PVC access mode for the per-run staging volume. Default `ReadWriteMany` (multi-node prod); single-node dev (k3d local-path, no RWX) sets `ReadWriteOnce`. |
| `LEOFLOW_EXECUTOR_DEFAULTS_STAGING_SIZE` | _(empty)_ | Pro | Default size of the per-run staging volume when the DAG enabled staging without pinning it (a Kubernetes quantity, e.g. `10Gi`). Empty leaves the size unset. Helm: `executor.defaults.staging.size`. |
| `LEOFLOW_EXECUTOR_DEFAULTS_STAGING_STORAGE_CLASS` | _(empty)_ | Pro | Default StorageClass for the staging volume (e.g. the cluster's RWX class). Empty falls back to the cluster's default StorageClass. Helm: `executor.defaults.staging.storageClass`. |
| `LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU` | _(empty)_ | Pro | Default CPU for a task that declares none of its own (a Kubernetes quantity, e.g. `250m`). Applied as **both request and limit**, so the task lands in Guaranteed QoS; empty leaves it BestEffort unless the DAG sets its own. Helm: `executor.defaults.resources.cpu`. |
| `LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_MEMORY` | _(empty)_ | Pro | Default memory for a task that declares none of its own (e.g. `256Mi`). Applied as **both request and limit** (Guaranteed QoS). Helm: `executor.defaults.resources.memory`. |

### Warm worker pools (`execution.*`)

Pro-gated N:1 pod reuse ([ADR 0058](/project/adrs/0058-warm-worker-pools/)). Every field
is operator-set. The default is a byte-for-byte no-op — warm pools OFF means a
dedicated pod per task attempt.

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_EXECUTION_WARM_POOLS_ENABLED` | `false` | Pro | Reuse one task pod across many attempts of the same DAG version. Off = dedicated pod-per-task. Validated fail-closed at boot: requires `agent_token_transport=exchange` **and** `secret_liveness_mode=enforce`. Helm: `execution.warmPoolsEnabled`. |
| `LEOFLOW_EXECUTION_MIN_IDLE_WORKERS` | `0` | Pro | Warm workers kept ready per DAG version when the DAG declares no warmth of its own (D6). `0` is scale-to-zero. Read only while warm pools are on. |
| `LEOFLOW_EXECUTION_MAX_POOL_SIZE` | `8` | Pro | Cap on the warm workers one DAG version may hold, and the ceiling a DAG author's warmth request is clamped to (D6). |
| `LEOFLOW_EXECUTION_MAX_ATTEMPTS_PER_WORKER` | `50` | Pro | Attempts a warm worker serves before it drains and recycles (D9). |
| `LEOFLOW_EXECUTION_MAX_WORKER_LIFETIME` | `1h` | Pro | Wall-clock lifetime of a warm worker before it drains and recycles, independent of the attempt count (D9). A duration string. |
| `LEOFLOW_EXECUTION_WORKER_IDLE_TTL` | `5m` | Pro | How long an idle warm worker is kept before it is recycled (D6). A duration string. |
| `LEOFLOW_EXECUTION_MAX_WARM_PODS_PER_TENANT` | `100` | Pro | Cap on the total warm pods one tenant may hold across all its DAG versions (M4), so one team cannot pin idle pods and starve neighbours on a shared cluster. |

### Logs (`logs.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_LOGS_DIR` | `/var/log/leoflow` | both | Task-log sink directory (used by the default `disk` backend). |
| `LEOFLOW_LOGS_BACKEND` | `disk` | Pro | Durable task-log store: `disk` (default — the on-disk sink, unchanged; the only backend Lite uses), `s3` (AWS S3, MinIO, Ceph RGW), or `gcs` (Google Cloud Storage, native SDK). See [ADR 0056](/project/adrs/0056-task-log-object-sink/). |
| `LEOFLOW_LOGS_SINK_BUCKET` | _(empty)_ | Pro | Target bucket. Required when the backend is `s3` or `gcs` (boot fails otherwise). |
| `LEOFLOW_LOGS_SINK_PREFIX` | _(empty)_ | Pro | Optional key prefix; objects are laid out at `{prefix}/{tenant}/{dag}/{run}/{task}/{try}.log`. |
| `LEOFLOW_LOGS_SINK_REGION` | _(empty)_ | Pro | **s3-only.** Store region (e.g. `us-east-1`). Required by AWS S3; ignored by some S3-compatible stores. |
| `LEOFLOW_LOGS_SINK_ENDPOINT` | _(empty)_ | Pro | **s3-only.** Endpoint override for S3-compatible stores (MinIO, Ceph RGW). Empty uses the AWS default. Not a path to GCS — use `gcs`. |
| `LEOFLOW_LOGS_SINK_FORCE_PATH_STYLE` | `false` | Pro | **s3-only.** Use path-style addressing (bucket in the path, not the host). Required by MinIO and some S3-compatible stores. |
| `LEOFLOW_LOGS_SINK_ACCESS_KEY_ID` / `LEOFLOW_LOGS_SINK_SECRET_ACCESS_KEY` | _(empty)_ | Pro | **s3-only.** Static credentials — **discouraged**. Leave empty (recommended) to use the keyless chain (IRSA / instance profile), per [ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/). |
| `LEOFLOW_LOGS_SINK_CREDENTIALS_FILE` | _(empty)_ | Pro | **gcs-only.** Path to a service-account JSON key — **discouraged**. Leave empty (recommended) to use Application Default Credentials (GKE Workload Identity). |

### Observability (`observability.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_OBSERVABILITY_LOG_LEVEL` | `info` | both | Control-plane log verbosity: `debug`, `info`, `warn` (alias `warning`), or `error`. Unknown values fall back to `info`. |
| `LEOFLOW_OBSERVABILITY_LOG_FORMAT` | `json` | both | Control-plane log format: `json` (default) or `text`. |
| `LEOFLOW_OBSERVABILITY_OTEL_ENABLED` | `true` | both | Enable OpenTelemetry trace export. |
| `LEOFLOW_OBSERVABILITY_OTEL_ENDPOINT` | `localhost:4317` | both | OTLP collector endpoint (when OTel is enabled). |

### UI (`ui.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_UI_INSTANCE_NAME` | `Leoflow` | both | UI navbar label (`leoflow lite` sets it to mark the environment). |
| `LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS` | `0` | both | SPA polling cadence for DAG / DagRun / task-instance state. `0` falls back to the production-safe 30s default; `leoflow lite` sets 1s for a snappy inner loop. |
| `LEOFLOW_UI_EDITION` | _(empty)_ | both | Edition badge in the UI shell: `lite` shows the silver LITE badge, `pro` the gold PRO badge (independent of the auth mode; also gates `auth.provider: oidc`). Empty/other shows no badge. |
| `LEOFLOW_UI_WORKSPACE` | _(empty)_ | both | DAG project directory the Lite web editor edits ([ADR 0025](/project/adrs/0025-lite-embedded-web-editor/)). Empty disables the editor. |
| `LEOFLOW_UI_MONACO_DIR` | _(empty)_ | both | Where the pinned Monaco bundle was fetched by `leoflow setup`; the editor page is served Monaco from it. Empty shows a setup hint. |

### Trusted proxies and the client IP

By default Leoflow trusts **no** proxy: `X-Forwarded-For` is ignored and the
client IP (used by the login rate-limiter and the audit log) is the direct peer.
This is the safe default — it stops a spoofed `X-Forwarded-For` from forging the
client IP — and is correct for Lite (exposed directly) and for any deployment
reached without a reverse proxy.

When the API runs **behind a reverse proxy or ingress**, set
`server.trusted_proxies` (env `LEOFLOW_SERVER_TRUSTED_PROXIES`) to the proxy's
IP or CIDR — e.g. your ingress controller's pod CIDR. Only then is the left-most
`X-Forwarded-For` entry honored, so rate-limiting and audit see the real client
instead of the proxy. **Do not** set this to a broad private range (e.g. all of
`10.0.0.0/8`) in a cluster where task pods run: a task pod inside that range
could then spoof the client IP. Scope it to the ingress. An invalid value fails
secure (trust none) with a logged error.

The value is a list. Via the env var (the Helm chart's only override path — it
ships no server config file) set it **comma-separated**, e.g.
`LEOFLOW_SERVER_TRUSTED_PROXIES=10.0.0.0/8,192.168.1.1`; viper splits it back
into a list. In the chart set the `config.trustedProxies` value (a YAML list) and
it is rendered comma-joined for you. In a config file it is an ordinary YAML list.
