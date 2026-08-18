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
| `LEOFLOW_EXECUTOR_TYPE` | `kubernetes` (Pro), `subprocess` (Lite) | both | `kubernetes` or `subprocess`. |
| `LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` | grpc_addr | both | Address task pods dial back. |
| `LEOFLOW_EXECUTOR_AGENT_TLS_CA_CONFIGMAP` | — | Pro | CA ConfigMap for agent TLS (#58). |
| `LEOFLOW_EXECUTOR_DEFAULTS_*` | — | Pro | L0 platform defaults (staging size/class, resources). |
| `LEOFLOW_AUTH_DEV_NO_AUTH` | `false` | dev-only | Legacy escape hatch — bypasses auth (loopback-only). Modern Lite uses a real admin login generated by `leoflow setup`; set this only for ephemeral test scaffolds. |
| `LEOFLOW_UI_INSTANCE_NAME` | `Leoflow` | both | UI navbar label (`leoflow lite` sets it to `Leoflow Lite`). |
| `LEOFLOW_LOGS_DIR` | `/var/log/leoflow` | both | Task-log sink directory (used by the default `disk` backend). |
| `LEOFLOW_LOGS_BACKEND` | `disk` | Pro | Durable task-log store: `disk` (default — the on-disk sink, unchanged; the only backend Lite uses) or `object` (opt-in S3-compatible bucket). |
| `LEOFLOW_LOGS_OBJECT_BUCKET` | _(empty)_ | Pro | Target bucket. Required when the backend is `object` (boot fails otherwise). |
| `LEOFLOW_LOGS_OBJECT_PREFIX` | _(empty)_ | Pro | Optional key prefix; objects are laid out at `{prefix}/{tenant}/{dag}/{run}/{task}/{try}.log`. |
| `LEOFLOW_LOGS_OBJECT_REGION` | _(empty)_ | Pro | Store region (e.g. `us-east-1`). Required by AWS S3; ignored by some S3-compatible stores. |
| `LEOFLOW_LOGS_OBJECT_ENDPOINT` | _(empty)_ | Pro | S3 endpoint override for S3-compatible stores: Google Cloud Storage interop (`https://storage.googleapis.com`) or MinIO. Empty uses the AWS default. |
| `LEOFLOW_LOGS_OBJECT_FORCE_PATH_STYLE` | `false` | Pro | Use path-style addressing (bucket in the path, not the host). Required by MinIO and some S3-compatible stores. |
| `LEOFLOW_LOGS_OBJECT_ACCESS_KEY_ID` / `LEOFLOW_LOGS_OBJECT_SECRET_ACCESS_KEY` | _(empty)_ | Pro | Static credentials — **discouraged**. Leave empty (recommended) to use the keyless credential chain (IRSA / instance profile / GKE Workload Identity), per [ADR 0035](adr/0035-cloud-connector-auth-keyless-first.md). |
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
