# Configuration reference

Two surfaces: **`leoflow.yaml`** (per-DAG, authoring) and **server environment**
(`LEOFLOW_*`, the control plane). The canonical `leoflow.yaml` schema is
[`docs/api/leoflow-yaml-schema.json`](https://github.com/neochaotic/leoflow/blob/main/docs/api/leoflow-yaml-schema.json).

## leoflow.yaml

| Key | Type | Notes |
|---|---|---|
| `dag_id` *(required)* | string | Unique DAG id (`^[A-Za-z0-9_][A-Za-z0-9_-]{0,199}$`). |
| `description`, `owner`, `tags` | string / string / list | Metadata. |
| `python_version` | `3.10`\|`3.11`\|`3.12` | Base image Python (default 3.11). |
| `base_image` | string | Override the runtime base image. |
| `dependencies` | list | pip specifiers baked into the image. |
| `connectors` | list | Short connector names (`postgres`, `http`, …) expanded at compile to their `apache-airflow-providers-*` packages. Sugar over `dependencies` — see [Installing a connector's provider](connections/index.md#installing-a-connectors-provider). |
| `system_packages` | list | apt packages. |
| `dag_source` | string | DAG file (default `dag.py`). |
| `build`, `registry` | object | Image build + push settings. |
| `defaults` | object | DAG-level `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `resources`. |
| `staging` | object | Opt-in per-run RWX volume: `enabled`, `size`, `storage_class` (ADR 0022). |
| `tasks.<task_id>` | object | Per-task overrides (ADR 0023): `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `env`, `resources`, `execution`. |

See [DAG authoring](dag-authoring.md) for the override layers.

### Defaults

Every field in `leoflow.yaml` is optional. Zero-valued fields are filled by
`LeoflowConfig.ApplyDefaults()` (`internal/domain/config.go`) from the values
declared in [`leoflow-yaml-schema.json`](https://github.com/neochaotic/leoflow/blob/main/docs/api/leoflow-yaml-schema.json).
Defaults are hardcoded for v1; making them workspace-configurable is a v2
roadmap item.

| Field | Default | Notes |
|---|---|---|
| `schema_version` | `"1.0"` | Stamps every artifact for forward-compat. |
| `dag_id` | *subdir basename* | If `leoflow.yaml` is absent, the parent directory name is used. Two subdirs resolving to the same `dag_id` is a hard error — see [Discovery rules](dag-authoring.md#discovery-rules). |
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

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `LEOFLOW_SERVER_ROLE` | `all` | Pro | Which components this process runs ([ADR 0049](adr/0049-split-api-and-scheduler-roles.md)): `all` (default — the Lite monolith; every component in one process), `api` (HTTP + UI only, restricted identity), or `scheduler` (reconciler + dispatch + agent gRPC, privileged). Empty defaults to `all`, which is behavior-identical to the pre-split monolith; splitting is a Pro-only topology. |
| `LEOFLOW_SERVER_HTTP_ADDR` | `0.0.0.0:8080` | both | HTTP/UI listener. |
| `LEOFLOW_SERVER_GRPC_ADDR` | `0.0.0.0:9091` | both | Agent gRPC listener. |
| `LEOFLOW_SERVER_METRICS_ADDR` | `0.0.0.0:9090` | both | Prometheus metrics. |
| `LEOFLOW_SERVER_TRUSTED_PROXIES` | *(empty — trust none)* | both | Proxy IPs/CIDRs whose `X-Forwarded-For` is honored for the client IP (`server.trusted_proxies`, a list). See note below. |
| `LEOFLOW_DATABASE_URL` | `postgres://…/leoflow` | both | Postgres DSN. |
| `LEOFLOW_REDIS_URL` | `redis://…/0` | **Pro only** | Redis (XCom + locks). Lite stores both in Postgres ([ADR 0026](adr/0026-lite-datastore-no-redis.md)). |
| `LEOFLOW_AUTH_JWT_SECRET` | — *(required for jwt)* | both | Signs API/agent tokens. |
| `LEOFLOW_SECRET_KEY` | — | both | 32-byte key encrypting connection secrets ([ADR 0019](adr/0019-secret-encryption-at-rest.md)). |
| `LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT` | `envvar` | Pro (K8s) | How the in-pod agent obtains its bearer credential ([ADR 0055](adr/0055-secret-scoping-and-token-liveness.md)): `envvar` (plaintext `LEOFLOW_AGENT_TOKEN` on the pod spec — today's behavior, byte-identical) or `exchange` (projected ServiceAccount token exchanged once via a control-plane `TokenReview` for a task-scoped JWT — nothing secret on the pod object). Operator-scoped. Prerequisite for warm pools. See [Agent credential transport](agent-credential-transport.md). Ignored by the subprocess (Lite) executor. |
| `LEOFLOW_AUTH_SECRET_LIVENESS_MODE` | `observe` | both | Gates secret delivery on task-instance liveness ([ADR 0055](adr/0055-secret-scoping-and-token-liveness.md)): `observe` (logs + audits a would-have-denied when the caller's task instance is not live, but still delivers) or `enforce` (denies). Liveness renewal is always on regardless of mode; this only chooses whether a not-live token is refused. Required to be `enforce` when warm pools are on. |
| `LEOFLOW_AUTH_SECRET_SCOPING` | `permissive` | both | Scope-by-declaration policy ([ADR 0055](adr/0055-secret-scoping-and-token-liveness.md)): `permissive` (delivers the whole tenant vault; warns when a DAG declares a narrower set), `enforce` (delivers only the declared subset — empty declaration ⇒ nothing), or `off` (no scoping). Operator-scoped, never author-settable. |
| `LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME` | `24h` | both | Duration ceiling on how long one attempt's agent credential may be kept alive by heartbeat renewal ([ADR 0055](adr/0055-secret-scoping-and-token-liveness.md)). A runaway-task backstop — the short per-attempt TTL is what bounds a stolen token. A non-positive value disables the ceiling. |
| `LEOFLOW_EXECUTOR_TYPE` | `kubernetes` (Pro), `subprocess` (Lite) | both | `kubernetes` or `subprocess`. |
| `LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` | grpc_addr | both | Address task pods dial back. |
| `LEOFLOW_EXECUTOR_AGENT_TLS_CA_CONFIGMAP` | — | Pro | CA ConfigMap for agent TLS (#58). |
| `LEOFLOW_EXECUTOR_DEFAULTS_*` | — | Pro | L0 platform defaults (staging size/class, resources). |
| `LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT` | `envvar` | Pro | How the in-pod agent gets its control-plane credential ([ADR 0055](adr/0055-secret-scoping-and-token-liveness.md)): `envvar` puts the task-scoped JWT in a plaintext `LEOFLOW_AGENT_TOKEN` env var on the Pod object; `exchange` mounts a projected ServiceAccount token the agent trades once — via a control-plane TokenReview — for the JWT. **Server-wide**, and `exchange` requires cluster-scoped `create` on `authentication.k8s.io/tokenreviews` (the Helm chart renders it with the value). Helm: `auth.agentTokenTransport`. |
| `LEOFLOW_AUTH_SECRET_LIVENESS_MODE` | `observe` | Pro | Whether secret delivery is gated on task-instance liveness ([ADR 0055](adr/0055-secret-scoping-and-token-liveness.md)): `observe` logs and audits a would-have-denied but still delivers; `enforce` denies. Helm: `auth.secretLivenessMode`. |
| `LEOFLOW_EXECUTION_WARM_POOLS_ENABLED` | `false` | Pro | Reuse one task pod across many attempts of the same DAG version ([ADR 0058](adr/0058-warm-worker-pools.md)). Off = dedicated pod-per-task. Validated fail-closed at boot: requires `agent_token_transport=exchange` **and** `secret_liveness_mode=enforce`. Helm: `execution.warmPoolsEnabled`. |
| `LEOFLOW_EXECUTION_MIN_IDLE_WORKERS` | `0` | Pro | Warm workers kept ready per DAG version when the DAG declares no warmth of its own ([ADR 0058](adr/0058-warm-worker-pools.md) D6). `0` is scale-to-zero. Read only while warm pools are on. |
| `LEOFLOW_EXECUTION_MAX_POOL_SIZE` | `8` | Pro | Cap on the warm workers one DAG version may hold, and the ceiling a DAG author's warmth request is clamped to ([ADR 0058](adr/0058-warm-worker-pools.md) D6). |
| `LEOFLOW_EXECUTION_MAX_ATTEMPTS_PER_WORKER` | `50` | Pro | Attempts a warm worker serves before it drains and recycles ([ADR 0058](adr/0058-warm-worker-pools.md) D9). |
| `LEOFLOW_EXECUTION_MAX_WORKER_LIFETIME` | `1h` | Pro | Wall-clock lifetime of a warm worker before it drains and recycles, independent of the attempt count ([ADR 0058](adr/0058-warm-worker-pools.md) D9). A duration string. |
| `LEOFLOW_EXECUTION_WORKER_IDLE_TTL` | `5m` | Pro | How long an idle warm worker is kept before it is recycled ([ADR 0058](adr/0058-warm-worker-pools.md) D6). A duration string. |
| `LEOFLOW_EXECUTION_MAX_WARM_PODS_PER_TENANT` | `100` | Pro | Cap on the total warm pods one tenant may hold across all its DAG versions ([ADR 0058](adr/0058-warm-worker-pools.md) M4), so one team cannot pin idle pods and starve neighbours on a shared cluster. |
| `LEOFLOW_AUTH_DEV_NO_AUTH` | `false` | dev-only | Legacy escape hatch — bypasses auth (loopback-only). Modern Lite uses a real admin login generated by `leoflow setup`; set this only for ephemeral test scaffolds. |
| `LEOFLOW_UI_INSTANCE_NAME` | `Leoflow` | both | UI navbar label (`leoflow lite` sets it to `Leoflow Lite`). |
| `LEOFLOW_LOGS_DIR` | `/var/log/leoflow` | both | Task-log sink directory (used by the default `disk` backend). |
| `LEOFLOW_LOGS_BACKEND` | `disk` | Pro | Durable task-log store: `disk` (default — the on-disk sink, unchanged; the only backend Lite uses), `s3` (AWS S3, MinIO, Ceph RGW), or `gcs` (Google Cloud Storage, native SDK). See [ADR 0056](adr/0056-task-log-object-sink.md). |
| `LEOFLOW_LOGS_SINK_BUCKET` | _(empty)_ | Pro | Target bucket. Required when the backend is `s3` or `gcs` (boot fails otherwise). |
| `LEOFLOW_LOGS_SINK_PREFIX` | _(empty)_ | Pro | Optional key prefix; objects are laid out at `{prefix}/{tenant}/{dag}/{run}/{task}/{try}.log`. |
| `LEOFLOW_LOGS_SINK_REGION` | _(empty)_ | Pro | **s3-only.** Store region (e.g. `us-east-1`). Required by AWS S3; ignored by some S3-compatible stores. |
| `LEOFLOW_LOGS_SINK_ENDPOINT` | _(empty)_ | Pro | **s3-only.** Endpoint override for S3-compatible stores (MinIO, Ceph RGW). Empty uses the AWS default. Not a path to GCS — use `gcs`. |
| `LEOFLOW_LOGS_SINK_FORCE_PATH_STYLE` | `false` | Pro | **s3-only.** Use path-style addressing (bucket in the path, not the host). Required by MinIO and some S3-compatible stores. |
| `LEOFLOW_LOGS_SINK_ACCESS_KEY_ID` / `LEOFLOW_LOGS_SINK_SECRET_ACCESS_KEY` | _(empty)_ | Pro | **s3-only.** Static credentials — **discouraged**. Leave empty (recommended) to use the keyless chain (IRSA / instance profile), per [ADR 0035](adr/0035-cloud-connector-auth-keyless-first.md). |
| `LEOFLOW_LOGS_SINK_CREDENTIALS_FILE` | _(empty)_ | Pro | **gcs-only.** Path to a service-account JSON key — **discouraged**. Leave empty (recommended) to use Application Default Credentials (GKE Workload Identity). |
| `LEOFLOW_OBSERVABILITY_LOG_LEVEL` | `info` | both | Control-plane log verbosity: `debug`, `info`, `warn` (alias `warning`), or `error`. Unknown values fall back to `info`. |
| `LEOFLOW_OBSERVABILITY_LOG_FORMAT` | `json` | both | Control-plane log format: `json` (default) or `text`. |
| `LEOFLOW_OBSERVABILITY_OTEL_ENABLED` | `true` | both | Enable OpenTelemetry trace export. |
| `LEOFLOW_OBSERVABILITY_OTEL_ENDPOINT` | `localhost:4317` | both | OTLP collector endpoint (when OTel is enabled). |

`leoflow lite` sets the dev-appropriate values automatically (isolated DB, port 8088, admin login on, no Redis).

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
