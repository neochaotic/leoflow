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
**Deferred** — not implemented now; this ADR fixes the direction so the agent
contract and secret model are not designed into a corner.

When implemented:

- Add `GetVariable(key) -> {value, found}` and
  `GetConnection(conn_id) -> {conn fields}` RPCs to `proto/agent.proto`,
  authenticated by the same per-task-instance agent token as the other calls.
- The control plane resolves them against the repository, **decrypting connection
  secrets only in-process at fetch time** (ADR 0019); plaintext is sent only over
  the gRPC channel to the authorized agent, never persisted to the pod.
- The agent exposes them to user code via an Airflow **secrets backend** shim
  (or equivalent SDK hook), so `Variable.get` / `BaseHook.get_connection` resolve
  transparently — preserving Airflow API compatibility.
- Each access is auditable at the control plane (who/what fetched which key).
- Scope: a task may read any variable/connection in its tenant for the MVP;
  per-DAG scoping is a later refinement.

**Env injection (approach 1) is the explicit fallback** only if a faster MVP is
required before the gRPC path lands; if used, it must be documented as a known
secret-exposure tradeoff and limited to non-sensitive variables.

## Consequences

- Secrets stay out of the pod spec and the K8s API; exposure is limited to the
  task that asked, over an already-authenticated channel, and is auditable.
- The agent gains a small SDK-integration surface (the secrets-backend shim) and
  one network round trip per lookup (cacheable within a task run).
- Until this is built, DAGs cannot rely on Admin Variables/Connections; tasks
  must carry their own config. This is a known MVP limitation tracked in #54.
- The agent contract stays minimal and authenticated; no broad "dump all secrets
  to env" step is introduced.

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
