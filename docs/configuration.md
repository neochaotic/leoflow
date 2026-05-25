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
| `system_packages` | list | apt packages. |
| `dag_source` | string | DAG file (default `dag.py`). |
| `build`, `registry` | object | Image build + push settings. |
| `defaults` | object | DAG-level `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `resources`. |
| `staging` | object | Opt-in per-run RWX volume: `enabled`, `size`, `storage_class` (ADR 0022). |
| `tasks.<task_id>` | object | Per-task overrides (ADR 0023): `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `env`, `resources`, `execution`. |

See [DAG authoring](dag-authoring.md) for the override layers.

## Server environment (`LEOFLOW_*`)

| Variable | Default | Purpose |
|---|---|---|
| `LEOFLOW_SERVER_HTTP_ADDR` | `0.0.0.0:8080` | HTTP/UI listener. |
| `LEOFLOW_SERVER_GRPC_ADDR` | `0.0.0.0:9091` | Agent gRPC listener. |
| `LEOFLOW_SERVER_METRICS_ADDR` | `0.0.0.0:9090` | Prometheus metrics. |
| `LEOFLOW_DATABASE_URL` | `postgres://…/leoflow` | Postgres DSN. |
| `LEOFLOW_REDIS_URL` | `redis://…/0` | Redis (XCom + locks). |
| `LEOFLOW_AUTH_JWT_SECRET` | — *(required for jwt)* | Signs API/agent tokens. |
| `LEOFLOW_SECRET_KEY` | — | 32-byte key encrypting connection secrets (ADR 0019). |
| `LEOFLOW_EXECUTOR_TYPE` | `kubernetes` | `kubernetes` or `subprocess` (dev). |
| `LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` | grpc_addr | Address task pods dial back. |
| `LEOFLOW_EXECUTOR_AGENT_TLS_CA_CONFIGMAP` | — | CA ConfigMap for agent TLS (#58). |
| `LEOFLOW_EXECUTOR_DEFAULTS_*` | — | L0 platform defaults (staging size/class, resources). |
| `LEOFLOW_AUTH_DEV_NO_AUTH` | `false` | **Dev only** — disables auth (loopback-only; see [modes](operating-modes.md)). |
| `LEOFLOW_UI_INSTANCE_NAME` | `Leoflow` | UI navbar label. |
| `LEOFLOW_LOGS_DIR` | `/var/log/leoflow` | Task-log sink directory. |
| `LEOFLOW_OBSERVABILITY_*` | — | OTel endpoint, log level/format. |

`leoflow lite` sets the dev-appropriate values automatically (isolated DB, port 8088, no-auth on loopback).
