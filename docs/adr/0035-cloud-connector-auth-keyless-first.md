# ADR 0035: Cloud connector auth — keyless-first; Leoflow is not a key manager

**Status:** Accepted
**Date:** 2026-06-02
**Supersedes:** none (realizes the `google_cloud_platform` part of #77 and the workload-identity intent of #56; companions #312, #315)

## Context

Leoflow needs cloud connectors (starting with `google_cloud_platform`) so DAG
tasks can reach managed services (GCS, BigQuery, Pub/Sub, …). Airflow's provider
model is the obvious reference — operators expect an `AIRFLOW_CONN_*` connection
and a `google_cloud_platform` shape — and we want existing Airflow connections to
drop in. But Airflow's model also carries history we do **not** want to inherit
wholesale: the `extra__google_cloud_platform__*` field-name prefix, a key-centric
default, and a long tail of credential fields.

Two facts shaped this decision during the GKE Pro validation:

1. **Keyless is the secure default in practice.** The test project's org enforced
   `constraints/iam.disableServiceAccountKeyCreation` — service-account JSON keys
   could not be created at all. Many enterprises do the same. A connector whose
   default is "paste a key" is dead on arrival there.
2. **The delivery path is already key-agnostic.** Connections are encrypted at
   rest (ADR 0019) and delivered to tasks as `AIRFLOW_CONN_*` over the TLS agent
   channel (ADR 0021); the Go control plane runs no provider hooks (ADR 0014).
   Credential *resolution* belongs in the task (Python), not in core.

We validated end-to-end on GKE: a real DAG (`gcp_gcs_load`) wrote and read a GCS
object via **keyless Workload Identity** — the task pod ran as a KSA bound to a
GCP service account, no key anywhere.

## Decision

**Leoflow is not a secrets/key manager.** It orchestrates; it does not aspire to
own credential material. For cloud connectors this is a hard stance: credentials
come from the **runtime identity** (keyless) or from a **secret the platform
manages**, which the connection only *references* — Leoflow does not store the
cloud key.

1. **Keyless is the default and the recommendation.** Empty key fields →
   Application Default Credentials. On Pro/GKE that is **Workload Identity** (the
   task pod's KSA bound to a GCP service account — Leoflow already supports a
   per-task `execution.service_account`, and the chart exposes a default KSA); on
   Lite it is host ADC under the subprocess executor.

2. **Otherwise, reference a platform-managed secret — don't store the key in
   Leoflow:**
   - **`key_path` + a mounted Kubernetes Secret** — the key lives in the
     cluster's secret store; the connection holds only the path. The key never
     enters Leoflow's DB, API, or UI.
   - **`key_secret_name` + Secret Manager** (deferred) — the connection holds a
     reference; the task fetches at runtime. (Reading it needs an identity, which
     is the keyless bootstrap problem — so prefer keyless directly.)

3. **`keyfile_dict` (the key stored in the connection) is accepted for Airflow
   compatibility but explicitly discouraged.** It makes Leoflow hold the key,
   which contradicts the stance above. It is the cloud-key analog of how
   connections today store a database **user/password** encrypted at rest (ADR
   0019): pragmatic and Airflow-compatible, but **not the desirable posture**. We
   keep it as a documented escape hatch (dev / low-criticality), never the
   recommended path — and org policy often forbids creating such keys anyway.

4. **Resolution order in the task:** `keyfile_dict` → `key_path` →
   `key_secret_name` → ADC. Field names are clean short names (`keyfile_dict`,
   `key_path`, `key_secret_name`, `project`, `scopes`, `num_retries`); the legacy
   `extra__google_cloud_platform__<name>` names are accepted as a migration
   fallback only. `scopes` takes a list **or** a comma string (Airflow takes only
   the string).

5. **No cloud SDK in the Go control plane.** Connection validation is
   **structural only** (check the key shape, or report keyless); the token
   exchange happens in the task. Keeps core connector-agnostic and avoids a Go
   cloud-SDK supply-chain surface (consistent with ADR 0014). *Scope: this rule
   is about **connector credential resolution**. ADR 0056 carves a narrow
   exception for the **task-log object sink** — operator infrastructure that may
   use a cloud SDK in core with the pod's keyless identity, since it is not
   connector auth and does not make core a key manager.*

6. **v1 scope.** Handle `keyfile_dict`, `key_path` (mounted K8s Secret via the
   chart's `taskSecret`), `key_secret_name` (GCP Secret Manager, fetched in the
   task via ADC), `project`/`project_id`, `scopes`/`scope`, `num_retries`. Defer
   `key_secret_project_id`, `credential_config_file`, `impersonation_chain`,
   `quota_project_id`.

7. **Generalizes to future cloud connectors** (AWS, Azure): same stance —
   platform-native keyless first (Workload Identity / IRSA / Azure Workload
   Identity), a secret-store **reference** next, an in-connection key only as a
   discouraged compat fallback; resolve in the task, never in core.

## Consequences

- **Portable + secure by default.** Works where keys are forbidden (the common
  enterprise posture); the recommended paths store no cloud key in Leoflow.
- **Honest about the existing exception.** Database connections still store
  user/password encrypted (ADR 0019); this ADR names that as the not-desirable
  pattern we explicitly do **not** extend to cloud keys.
- **Familiar.** Airflow GCP connections and `AIRFLOW_CONN_*` consumers keep
  working; field names are a strict superset of Airflow's short names.
- **Edition split is explicit.** Keyless on Lite is subprocess-only (k3d has no
  metadata server → reference a key there); Pro/GKE gets full Workload Identity.
- **Open follow-ups:** verified TLS to managed datastores (Redis #312, Postgres
  #315); `key_secret_name` (Secret Manager) and live (token-minting) probes are
  deferred; a future ADR may move database credentials toward the same
  reference-a-secret model.

## Alternatives considered

- **Faithfully mirror Airflow** (legacy field prefix, key-first default, store
  the key). Rejected: inherits cruft and makes Leoflow a key store, against the
  stance above, for no migration benefit beyond the compatible superset.
- **Resolve credentials in the Go control plane** (pull a Go cloud SDK into core,
  probe live). Rejected: violates ADR 0014's no-provider-hooks-in-core posture,
  adds supply-chain surface, and token checks belong where the code runs — the task.
