# ADR 0049: Split the API/UI and scheduler into separate roles of one binary

**Status:** Accepted
**Accepted:** 2026-08-03 — maintainer greenlit the split for enterprise scale, a conscious override of the KFP study's "keep the monolith" (correct at small scale). Implementation is phased and must be well-tested before it rides a release.
**Date:** 2026-08-03
**Relates:** ADR 0001 (one Go control plane), ADR 0007 + 0017 (single binary, embedded SPA), ADR 0028 (one tag = both editions), ADR 0031 (DB-as-truth scheduler), ADR 0048 (no user code in the control plane), ADR 0009 (leader election)
**Supersedes (partial):** the implicit "one process serves everything" shape of ADR 0007/0017 — the binary and the embedded SPA are unchanged; only the process topology in Pro changes.

## Context

`leoflow-server` runs everything in one process: the HTTP API + embedded SPA, the
scheduler reconciliation loop + reapers, the dispatch worker pool, the pod
reconciler/staging GC, and the agent gRPC endpoint. That monolith is the right
call at small scale (a comparative Kubeflow study concluded exactly this — "do not
decompose the binary; the monolith is the competitive position" — against KFP's
15-Deployment sprawl).

Two forces push against it as the target moves to enterprise scale:

1. **Scalability.** The API is active-active (every replica serves HTTP; only the
   leader schedules — ADR 0009). But the leader pod serves API traffic *and* runs
   the scheduler tick, so hundreds of logged-in users hammering the API contend
   with scheduling on that one pod; and every replica carries idle scheduler
   machinery. Argo splits `argo-server` (API, active-active) from
   `workflow-controller` (single) precisely to isolate these.

2. **Security (ADR 0048).** The API is the internet-facing surface, yet it shares
   the control plane's privileged network identity (apiserver reach, datastore
   credentials, cluster network). A compromised or SSRF'd API process inherits all
   of it. The API does not need apiserver reach; the scheduler does. One identity
   for both is the widest possible blast radius.

This is a deliberate, forward-looking override of the "keep the monolith"
recommendation: it was correct for current scale, and the split earns its keep at
the enterprise scale now targeted.

## Decision

**One binary, selectable roles.** `leoflow-server` gains a role
(`LEOFLOW_ROLE` / `--role`):

- **`api`** — HTTP API + UI (the embedded Airflow SPA, ADR 0007/0017 unchanged) +
  `/metrics`. Serves users. **Active-active** (HPA). Given a **restricted network
  identity**: it reaches Postgres/Redis, not the apiserver or metadata.

  *Embedded now, not rigidified.* The SPA stays `go:embed`ded in this role today
  (the right call — §Alternatives). But the static-serving stays a **distinct,
  replaceable layer** (its own route group + `WorkspaceFS`-style seam, not fused
  into the API handler) and the origin/CORS/cookie config stays **configurable,
  not hardcoded same-origin**. So the ADR 0018 north star — a custom Leoflow UI,
  and/or a CDN in front of the static path — is a later swap, not a rewrite. This
  ADR picks embedding for the first cut without closing that door.
- **`scheduler`** — the reconciliation loop, reapers, dispatch pool, pod
  reconciler + staging GC, and the **agent gRPC** endpoint (agents dial in to
  report state). **Active-passive** (one leader, ADR 0009). Keeps the privileged
  identity (creates pods → needs the apiserver).
- **`all`** — everything in one process. The **default**, and what Lite always
  runs (single host, one process). Backward-compatible: existing deployments are
  unaffected until they opt into split roles.

**The split is possible because the API already talks to the scheduler through
Postgres, not in-process** (DB-as-truth, ADR 0031). The API reads/writes the DB;
the scheduler reconciles from it. Two in-process couplings must become DB reads:

1. `api.Heartbeater` / `SchedulerHealth` — the API's health endpoint reads the
   scheduler's in-memory liveness. The scheduler already writes its heartbeat to
   the DB; the API reads it from there (or from a scheduler `/healthz` it polls).
2. `api.ExecutorInfo` — namespace + control-plane address, today an in-process
   struct; becomes config the API reads directly.

The agent gRPC stays with the `scheduler` role (it is about task state, which the
scheduler owns). The dispatch pool, reconciler, and staging GC are already
leader-gated (ADR 0031 + the recent leader-gating fix), so they belong to the
`scheduler` role unchanged.

**This aligns with removing in-control-plane execution.** ADR 0047/0048 remove
user-influenced execution from the control plane; the residual inline `http_api`
executor (still reachable via a crafted `dag.json`) is removed as part of this
work — the `api` role must not execute anything author-influenced, and the
`scheduler` role runs no user HTTP either (it dispatches pods).

## Consequences

**Pro gains a second Deployment.** The Helm chart renders `api` (N replicas + HPA)
and `scheduler` (1 replica + leader election + PDB) when split is enabled; a single
`all` Deployment otherwise. Each role gets its own ServiceAccount and
NetworkPolicy — the `api` SA has no pod-create/apiserver RBAC, the `scheduler` SA
keeps it. This is the security payoff: a compromised API cannot reach the
apiserver.

**One binary, one tag (ADR 0028 preserved).** No second artifact to build, sign,
or version. The SPA stays embedded in the Go server.

**Lite is unaffected — this is a Pro-only topology.** Lite is single-host: there
is no cluster to split across, no second pod, no cross-replica leadership to gain.
Lite runs the `all` role, which is **behaviour-identical to today's monolith** —
the same in-process wiring, including the direct in-process `SchedulerHealth`
handle and `ExecutorInfo` struct (the DB-backed indirection is only needed when
the roles are in separate processes, so `all` keeps the direct path). The split's
new surface (role selection, the DB-backed health read, the second Deployment)
lives entirely on the `api`/`scheduler` paths. The guard is the existing Lite e2e:
it runs under `all` and must stay green unchanged, so a refactor that touched
Lite's behaviour fails loudly. `leoflow lite` / `leoflow dev` never set a role, so
they get `all` by default and never see the split.

**More moving parts to operate in Pro.** Two Deployments, two Services (the api
HTTP/UI Service; the scheduler's agent-gRPC Service that task pods dial). Argo's
two-process shape is the reference; KFP's 15-Deployment sprawl is the anti-pattern
to avoid — this adds exactly one split, not a fleet.

**Health/observability shifts.** The API's readiness must reflect "can I serve"
(DB reachable), and scheduler health becomes a value the API reports from the DB,
not from memory — a small behaviour change to name in the upgrade notes.

## Test plan (this must be well-tested before it rides a release)

1. **Unit** — role parsing/defaulting (`all` default; unknown role fails loud);
   the DB-backed health read replacing the in-process `Heartbeater`.
2. **Integration (real Postgres)** — an `api`-role process with **no scheduler in
   the process**: registration, connection/variable CRUD, run trigger, and
   `/healthz`/scheduler-health all work purely through the DB.
3. **E2E (k3d, two processes)** — deploy `api` + `scheduler` as separate pods:
   trigger a run on the api pod, assert the scheduler pod dispatches it, a task pod
   runs, the agent reports back over gRPC to the scheduler, and the UI on the api
   pod shows the terminal state. The existing single-process e2e stays green under
   the `all` role (backward-compat).
4. **NetworkPolicy** — assert the `api` role cannot reach the apiserver (the
   security invariant), and the `scheduler` role can.
5. **Upgrade** — a monolith (`all`) deployment upgraded to split roles keeps
   serving (no data migration; it is a topology change).

## Alternatives rejected

**Two separate binaries** (Argo's literal shape). More build/release/signing
surface, and it breaks ADR 0028's one-tag contract. A role flag on one binary
gives the same process isolation without the second artifact.

**Keep the monolith.** The KFP study's recommendation, correct for small scale.
Rejected here only because the target is enterprise scale, where API load
isolation and a restricted API identity both matter — stated as a conscious
override, revisited if the scale assumption changes.

**A separate UI service (KFP's `ml-pipeline-ui`).** Unnecessary: the SPA is
embedded in the Go server (ADR 0017) and served by the `api` role. A separate
frontend process would add a component for no benefit.
