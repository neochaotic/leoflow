# ADR 0021: Exposing Variables and Connections to Task Pods

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Project founder

## Context

The Admin UI manages **Variables** (config key/values) and **Connections**
(credentials + endpoints; sensitive fields encrypted at rest per ADR 0019).
Today these are stored, served over `/api/v2`, and shown in the UI — but they are
**not reachable by a running task**. The `AgentService` gRPC (pod ↔ control
plane) exposes `Register`, `GetTaskSpec`, `FetchXCom`, `PushXCom`, `StreamLogs`,
`ReportState`, `Heartbeat` and nothing for variables/connections, and dispatch
injects no related env. So user code calling `Variable.get("x")` or
`BaseHook.get_connection("y")` does not resolve Leoflow's stored values — the
Admin panel is decorative for execution. See issue #54.

We need tasks to consume Variables/Connections the way Airflow code expects,
without weakening the secret-at-rest guarantees of ADR 0019. Two shapes were on
the table:

1. **Env injection at dispatch** — set `AIRFLOW_VAR_<KEY>` and
   `AIRFLOW_CONN_<ID>` on the pod. The Airflow SDK reads these natively. Smallest
   change, but every secret is decrypted at dispatch and written into the pod
   spec env, visible to anyone who can read the pod (`kubectl get pod -o yaml`,
   the K8s API, audit/event logs), and persists for the pod's lifetime.
2. **On-demand gRPC** — add `GetVariable`/`GetConnection` to `AgentService`; the
   agent fetches a value only when the task asks, over the existing
   per-task-instance authenticated gRPC channel. Secrets never enter the pod
   spec; the control plane can authorize and audit each access.

## Decision

Adopt **approach 2: on-demand fetch over `AgentService`**, as the target design.
**MVP implementation (now): gRPC fetch + agent env-export.** The agent, before
running user code, fetches the tenant's variables and connections over the
authenticated gRPC channel and exports them into its **process environment** as
`AIRFLOW_VAR_<KEY>` / `AIRFLOW_CONN_<ID>`. Airflow's built-in env-var secrets
backend then resolves `Variable.get` / `BaseHook.get_connection` natively (and
they are also readable as plain OS env vars) — no Python secrets-backend shim
required. Secrets live only in the pod's process env, never in the pod spec / K8s
API / etcd.

- Add `GetVariables` / `GetConnections` RPCs to `proto/agent.proto`,
  authenticated by the same per-task-instance agent token.
- The control plane **decrypts connection secrets only in-process at fetch time**
  (ADR 0019); plaintext travels only over the gRPC channel to the authorized
  agent.
- **Prerequisite: the agent↔core gRPC must use TLS.** Today the server runs the
  gRPC plaintext; TLS (ideally mTLS) is enabled first, before any secret flows.
- Scope: Variables and Connections are **global** (tenant-wide), matching
  Airflow. No per-DAG scoping in the MVP.

**Design for evolution.** Keep the secret-delivery mechanism behind a seam so we
can move, without rewriting consumers, to:
- **K8s Secret projection (Argo/KFP style):** materialize a connection as a
  Kubernetes Secret and reference it via `secretKeyRef` — leans on etcd
  encryption + RBAC + audit; value never crosses our gRPC. (Hardening step.)
- **Cloud workload identity (#56):** for cloud-storage credentials, prefer
  keyless Workload Identity (GKE) / IRSA (EKS) — no secret at all. This is what
  Argo Workflows and Kubeflow Pipelines do for cloud.

### Known tradeoffs of the MVP path (accepted)

- Secrets end up in the pod's **process environment**, so any code in the pod
  (the task and its dependencies) can read all exported values. Expected for the
  task's own use; the trust model is "do not run untrusted code/images". Do not
  log the environment.
- **Fetch-all at boot = no least-privilege**: a task sees every tenant secret,
  not just what it uses. Tracked as a follow-up (scope to referenced secrets).
- A connection URI embeds its password (Airflow's `AIRFLOW_CONN_` format) — that
  is inherent to Airflow compatibility.

## Consequences

- Secrets stay out of the pod spec / K8s API / etcd; they reach the pod only over
  the authenticated gRPC channel and live in the process env.
- `Variable.get` / `BaseHook.get_connection` work natively (Airflow env backend),
  and the values are also plain OS env vars — no Python shim.
- Requires gRPC TLS (enabled as the first slice) and accepts the process-env and
  fetch-all tradeoffs above; both have clear evolution paths (K8s Secret
  projection, Workload Identity, per-secret scoping).
- The agent contract grows by two read RPCs, authorized by the per-task-instance
  token.

## Alternatives considered

- **Env injection at dispatch (approach 1).** Rejected as the default: decrypts
  every selected secret at dispatch and writes it into the pod spec env, where it
  is broadly readable and long-lived — at odds with ADR 0019's intent. Kept only
  as a documented fallback.
- **Shared secrets volume / projected files.** Same exposure problem as env, plus
  a mount lifecycle to manage.
- **Direct DB access from the pod.** Rejected: pods must not hold DB credentials
  or reach Postgres; all pod ↔ core traffic goes through the authenticated agent
  gRPC (consistent with the execution model).
