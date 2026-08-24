---
title: "ADR 0045: Secrets reach a task because it declared them"
linkTitle: 0045 · Secrets reach a task because it declared them
weight: 450
description: "ADR 0045: Secrets reach a task because it declared them"
---

**Status:** Accepted — not yet implemented. The decision (deliver a task only
the secrets it declares) stands; **no code ships it yet.** The runtime still
hands every task its whole tenant vault — `GetVariables`/`GetConnections` pass
only the tenant id (`internal/agentrpc/secrets.go`), exactly the behaviour the
Context section describes. An external audit of v0.2.0 re-confirmed the C1
exfiltration this ADR exists to close is live. Do not read "Accepted" as "a task
only receives what it declares" — that guarantee does not exist until #59 lands.
**Date:** 2026-07-30
**Accepted:** 2026-07-31
**Relates:** ADR 0019 (encryption at rest), ADR 0021 (exposing variables/connections to pods), ADR 0004 (thin agent), ADR 0035 (Leoflow is not a key manager)
**Issues:** #59, #388, #476, #486

> **This completes the follow-up ADR 0021 scheduled**, not a defect it missed.
> ADR 0021 (Accepted) chose on-demand fetch, shipped fetch-all as its MVP, and
> named the gap in its own text: *"Fetch-all at boot = no least-privilege… Tracked
> as a follow-up (scope to referenced secrets)"*, under the stated trust model
> *"do not run untrusted code/images"*. What changed is the target — multiple
> teams in one Pro install cannot keep "trusted code" as the model — not a
> discovery that the old code was wrong. This ADR supersedes only ADR 0021's
> scoping clause; the fetch mechanism and TLS prerequisite stand.

## Context

Today every task receives every secret in its tenant. `GetVariables` and
`GetConnections` take empty request messages; the server passes only the
tenant id to storage (`internal/agentrpc/secrets.go:55,72`), and the agent
exports the lot into the task's environment as `AIRFLOW_VAR_*` and
`AIRFLOW_CONN_*`. A DAG that declares no connections still receives, decrypted,
every database password and webhook URL the tenant holds.

That was a deliberate simplification in ADR 0021 and it is recorded as a known
gap in #59. Three things have changed since:

1. **It is worse than "the task can read them".** The agent's bearer token was
   readable from the task's own environment, and the server authenticates it by
   signature alone — no check that the task instance is still live. A validation
   run demonstrated retrieving every connection URI in the tenant from *outside
   any task*, with a token exfiltrated from one, **after that task had finished**
   (#476). Scoping delivery is worthless while the credential that bypasses the
   scoping is itself handed to user code, so the environment filter lands first.

2. **We now know what the alternatives cost**, from comparative studies of Argo
   Workflows and Kubeflow Pipelines against this codebase (documents kept
   outside the repo, not committed).

3. **The codebase already does this correctly elsewhere.** `FetchXCom`
   (`internal/agentrpc/server.go:271`) authorizes per task. The capability is
   present; secrets simply did not use it.

## Prior art, and what each actually delivers

**Argo Workflows** — the workflow spec names each secret as an explicit
`secretKeyRef` / volume reference. Enforcement is Kubernetes': the pod only ever
receives what the manifest names. Strong, and structurally simple.

**Kubeflow Pipelines** — reads as declarative (`use_secret_as_env`) but is not
enforcement. `kubeflow-kubernetes-edit` grants `secrets: [get,list,watch]` to the
`default-editor` ServiceAccount that runs pipeline steps, so any step can read
every Secret in its namespace. The declaration is intent; the boundary is the
namespace. **Structurally the same posture Leoflow has today**, at the cost of a
CRD, a controller, a service mesh and a webhook. This is the finding that most
changes our plan: "port Kubeflow's model" would have bought complexity and no
isolation.

**Airflow AIP-72** — connections are declared and resolved through an API that
enforces at request time, rather than pre-loaded into the environment. This is
both the strongest model of the three *and* the one Leoflow is already
compatibility-bound to.

## Decision

**A task receives a variable or connection only if its DAG declared it.**

1. **Declaration in `leoflow.yaml`**, per DAG and optionally per task:
   `connections: [warehouse]`, `variables: [greeting]`. Compiled into `dag.json`,
   validated by the schema, and rejected at registration if a name is unknown.

2. **Enforcement server-side, not agent-side.** `GetVariables` and
   `GetConnections` resolve the caller's task from its token and return only that
   task's declared set. An agent asking for more receives less; the agent is not
   trusted to filter, because it runs inside the task's blast radius (ADR 0004).

3. **The agent's token stops being a general-purpose capability.** It is already
   removed from the task environment (#476). It should additionally be refused
   once the task instance is no longer live, so an exfiltrated token expires with
   the work rather than with the clock.

4. **Not `secretKeyRef`.** Argo's model is good and wrong for us: it requires the
   author to know Kubernetes Secret names and keys, which breaks the Airflow
   connection abstraction Leoflow exists to preserve. We take the *declaration*
   idea and keep our own resolution.

5. **Not namespace-per-tenant.** Kubeflow's isolation boundary presumes
   multi-tenancy, which is a future vision here and not a current reality. It
   also does not deliver what its API appears to promise.

## Consequences

**A DAG that used an undeclared connection breaks.** This is the point, and it is
a breaking change for any DAG relying on ambient availability. It needs a
migration: warn-and-deliver for one release, listing what a DAG used without
declaring, before enforcing.

**Two more places to keep in sync.** A connection renamed in the UI and not in
`leoflow.yaml` fails at registration rather than at run time — better, but it is
new friction, and the error has to name both sides.

**It does not fix Lite's fixed encryption key** (#486) or the pod-spec token
exposure. Scoping reduces what one leak yields; it does not remove the leaks.
Those are tracked separately and neither is a reason to defer this.

**The `[]` case must be explicit.** A DAG declaring no connections gets none —
not "all", which is today's behaviour and the bug.

## Alternatives rejected

**Leave it, and rely on the tenant boundary.** What Kubeflow effectively does.
Rejected: the tenant boundary does not exist yet here (one tenant), so it reduces
to no boundary at all, and the failure mode is silent — a task reads a credential
it was never meant to see and nothing records it.

**Filter in the agent.** Cheaper, and worthless: the agent runs inside the
container it is filtering for, so anything it can fetch, user code can fetch.

**Encrypt per task with a task-scoped key.** Moves the problem — the task needs
its key, and the key is delivered the same way the secrets were.

## Settled (2026-07-31)

The two questions left open above are decided, plus four points that surfaced
when the codebase was traced against this ADR (`leoflow-research/adr-0045-readiness.md`):

1. **Per DAG, with optional per-task narrowing.** The blast radius that matters
   is "another team's credentials", which DAG-level declaration closes. Per-task
   is available for authors who want it tighter. Note: the existing
   `TaskSpec.Secrets` field (`{name, source, reference}`) is the `secretKeyRef`
   shape this ADR §4 rejects — it is dead code and is deleted here, not reused. A
   new per-task field is added if per-task narrowing is exercised.

2. **Both compile and registration reject an undeclared use** — different checks,
   not a choice: compile knows whether a name is declared in `leoflow.yaml`;
   only the server knows whether that connection exists.

3. **Two-release arc, because this is breaking.** Eight of the 22 examples
   (`http_load`, `postgres_load`, `mysql_load`, `mssql_load`, `redis_load`,
   `sqlite_load`, `gcp_gcs_load`, `postgres_hook_load`) use a connection without
   declaring one. Release N ships the declaration field + **warn-and-deliver**
   (behaviour unchanged, but every undeclared use is recorded and surfaced) and
   declares those examples. Release N+1 flips warn to enforce. Shipping
   enforcement in one step would red the project's own CI.

4. **The migration warning records real runtime use, not static inference.**
   `get_connection` / `Variable.get` inside a TaskFlow `@task` is arbitrary
   Python the parser cannot see (verified: no handling in `parser/`), so static
   inference cannot carry the migration. The warn phase instruments the runtime's
   resolution path so the author is told which connections a task actually read.
   This recording is net-new runtime work and lands with the declaration field.

5. **Naming: keep `connections:` / `variables:`.** They are the Airflow-native
   words. `leoflow.yaml` already has `connectors:` (pip provider packages, ADR
   0038) one letter away — the compile fails loud when a known provider name
   appears under `connections`, turning the collision into an actionable error
   rather than a silent mistake.
