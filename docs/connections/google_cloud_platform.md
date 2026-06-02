# `google_cloud_platform` — Google Cloud connection

Connect tasks to Google Cloud (GCS, BigQuery, Pub/Sub, …) with a managed
Connection, in **two auth modes**: **keyless** (Workload Identity / ADC —
recommended) and **service-account key** (encrypted at rest).

The connection follows Airflow's `google_cloud_platform` shape — an existing
Airflow GCP connection drops in unchanged — with cleaner short field names and
keyless as the default.

## URI shape

GCP carries no host/login/password — everything lives in **Extra**. The control
plane delivers it as `AIRFLOW_CONN_GOOGLE_CLOUD_DEFAULT`:

```
google-cloud-platform://?__extra__=<url-encoded JSON>
```

## Extra fields

Short names are canonical; the legacy `extra__google_cloud_platform__<name>`
form is also accepted.

| Field | Meaning |
|---|---|
| `keyfile_dict` | Service-account JSON, inline (key mode). **Encrypted at rest** (ADR 0019). |
| `key_path` | Path to a service-account JSON file mounted in the task (key mode). |
| `project` / `project_id` | GCP project (optional). |
| `scopes` / `scope` | OAuth scopes — a list **or** a comma-separated string. |
| `num_retries` | Pass-through to your GCP client (optional). |

**Resolution order (first match wins):** `keyfile_dict` → `key_path` →
**ADC (keyless)**. Leave all key fields empty for keyless.

Not handled in v1: `key_secret_name`, `key_secret_project_id`,
`credential_config_file`, `impersonation_chain`, `quota_project_id`.

## Auth modes

### Keyless (recommended)
No key in the Connection. Credentials come from Application Default Credentials:

- **Pro (GKE):** **Workload Identity** — the task pod runs as a Kubernetes SA
  bound to a GCP service account; no key ever touches the cluster. See the
  chart's task-ServiceAccount knob and issue #56.
- **Lite (subprocess):** your host ADC (`gcloud auth application-default login`
  or `GOOGLE_APPLICATION_CREDENTIALS`).
- **Lite (k3d):** no metadata server → keyless unavailable; use key mode.

### Key (service-account JSON)
Paste the SA JSON into Extra as `keyfile_dict` (encrypted at rest), or point
`key_path` at a file mounted in the task. Works on every executor.

## Lite vs Pro

| | Lite (subprocess) | Lite (k3d) | Pro (GKE) |
|---|---|---|---|
| Keyless (ADC) | ✅ host ADC | ❌ (no metadata server) | ✅ Workload Identity |
| Key (`keyfile_dict` / `key_path`) | ✅ | ✅ | ✅ |

## Security

Prefer **keyless** in Pro — no long-lived key to store or rotate. When you must
use a key, `keyfile_dict` is encrypted at rest with the control plane's
`LEOFLOW_SECRET_KEY` (ADR 0019) and delivered only over the TLS agent channel
(ADR 0021).

## Example DAG + test

- Example: [examples/gcp_gcs_load](https://github.com/neochaotic/leoflow/tree/main/examples/gcp_gcs_load)
  — writes + reads a GCS object in both modes, with a clean `gcp_credentials()`
  helper.
- Delivery (chain-of-custody) is covered by an automated test that round-trips a
  synthetic key through encryption + `__extra__` (no real cloud needed); a real
  end-to-end run against GCS is documented as manual in the example README.

See also: [variables-connections.md](../variables-connections.md), ADR 0019, ADR 0021.
