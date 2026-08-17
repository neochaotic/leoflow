# ADR 0055: Secret scoping and token liveness — scope by declaration, exchange the token, bind it to task liveness

**Status:** Proposed
**Date:** 2026-08-17
**Relates:** ADR 0045 (secrets reach a task because it declared it — this ADR is the code that ships its open half), ADR 0021 (exposing variables/connections to pods — the MVP that shipped fetch-all), ADR 0008 (JWT auth — the token this ADR binds to liveness), ADR 0052 (durable task outcome — the same liveness signal, on a different path), ADR 0053 (admission + placement — the warm-worker roadmap this ADR gates), ADR 0054 (shared-cluster coexistence — the namespace split that shrinks, but does not close, B1)
**Issues:** #59 (scope to declared secrets), #388 (declaration in `leoflow.yaml` → storage), #486 (Lite fixed encryption key — bounded here, not fixed), #507 (token liveness / revocation)

> **This is not a new decision. It is the implementation ADR for the half of
> ADR 0045 that never shipped.** ADR 0045 is marked *Accepted — not yet
> implemented*, and it is precise about why: the runtime still hands every task
> its whole tenant vault, and an external audit of v0.2.0 re-confirmed the
> exfiltration live. ADR 0045 is **not stale** — its Context describes today's
> code exactly. What this ADR adds is (a) the concrete mechanism for the token
> half ADR 0045 left as "should additionally be refused once the task instance is
> no longer live", (b) the transport fix (stop shipping the token as a plaintext
> env var) ADR 0045 explicitly deferred, and (c) the hard ordering constraint the
> warm-worker roadmap (N:1, impl #2, ADR 0053 PR-N1) now depends on. ADR 0045's
> Decision stands unchanged; this ADR supersedes only its "tracked separately"
> hand-waves by pinning them to code.

## Context

Two ADRs converge here. ADR 0021 chose on-demand gRPC fetch as the target and
shipped **fetch-all env-export** as its MVP, naming the gap in its own text:
*"Fetch-all at boot = no least-privilege… Tracked as a follow-up."* ADR 0045
accepted the follow-up — deliver a task only the secrets it declares — but no
code ships it. The gap is not theoretical; it was demonstrated end-to-end.

**Current state, verified against the tree (not remembered).** There are three
exfiltration links. One is closed; two are open.

**CLOSED — #476 / PR #478.** Task code can no longer read the agent's bearer
token from its own process environment. The agent runs inside the task's pod
(ADR 0004), so its environment *is* the task's environment unless something
removes the difference; `stripAgentOnly` (`internal/agent/runner.go:756`) is that
something — it strips every `LEOFLOW_`-prefixed variable except a two-name
allowlist (`LEOFLOW_STAGING_DIR`, `LEOFLOW_TASK_INSTANCE_ID`) before user code is
exec'd, so `LEOFLOW_AGENT_TOKEN` never reaches the task. This landed. It is the
reason the environment filter went first: scoping delivery is worthless while the
credential that bypasses the scoping is handed to user code.

**OPEN #1 — the token is still a plaintext env var on the pod spec.**
`podEnv` sets `LEOFLOW_AGENT_TOKEN` as `corev1.EnvVar{Value: req.AgentToken}`
(`internal/executor/kubernetes.go:197`) — a literal value, not a `SecretKeyRef`
or a projected volume. `stripAgentOnly` keeps it out of the *task process*, but
it is still sitting in plaintext on the `Pod` object in etcd. **Any principal
with `get pods` in the task namespace reads it** (`kubectl get pod -o yaml`, the
K8s API, any controller or audit sink that logs pod specs) — exactly the exposure
ADR 0021 named when it rejected env-injection-at-dispatch as the default. The
in-process strip closed the task's own read path; it did nothing for the cluster
read path.

**OPEN #2 — the token is a whole-vault, no-liveness, 24h capability.** Two facts
compound:

- **Whole-vault delivery.** `GetVariables` / `GetConnections`
  (`internal/agentrpc/secrets.go:47,64`) take *empty* request messages and
  resolve on `id.TenantID` alone — `SecretVariables(ctx, id.TenantID)` /
  `SecretConnectionURIs(ctx, id.TenantID)`. They return the entire tenant's
  Variables and Connections, decrypted. A DAG that declares no connections still
  receives, decrypted, every database password and webhook URL the tenant holds.
- **Signature-only auth, no liveness, no revocation, 24h TTL.**
  `AuthenticateAgent` (`internal/auth/agent_token.go:63`) validates the JWT by
  signature, issuer, `leoflow-agent` audience, and `HS256` method — and nothing
  else. It never asks whether the task instance is still running. `identify`
  (`internal/agentrpc/server.go:449`) calls it and returns the identity; there is
  no revocation list and no heartbeat consult on the secret path. The token's TTL
  is `agentTokenTTL = 24 * time.Hour` (issued at dispatch).

Put together: **a token captured from a pod spec (OPEN #1) is a bearer credential
for the whole tenant vault, valid for 24 hours, usable from anywhere that can
reach the control-plane gRPC port, *after the task that carried it has already
finished*.** This is the B1 exfiltration ADR 0045 exists to close, and the
external audit re-confirmed it on v0.2.0. Note the audit's path: it retrieved
every connection URI in the tenant *from outside any task*, with a token
exfiltrated from one, after that task finished. Scoping delivery alone does not
close it — the token bypasses the scoping — which is why the two open links are
one ADR, not two.

**The codebase already does the hard parts correctly elsewhere.** `FetchXCom`
(`internal/agentrpc/server.go:296`) authorizes per task: a task may read XCom
only from an upstream it *declared* (`declaresUpstream`) or a direct dependency.
And the per-TI liveness signal already exists — `RecordHeartbeat`
(`server.go:243-266`, #128) stamps `last_heartbeat_at` and already returns
`ErrStaleReport` / `should_terminate` for a superseded attempt (reaped, or
`try_number` moved past this attempt). Both capabilities are present. The secret
path simply does not use either one.

## Decision

Close both open links, keeping ADR 0045's Decision (a task receives a secret only
if its DAG declared it) intact and adding the token, liveness, and transport fixes
it deferred. Four fixes plus one rejection, tracked by the four open issues. The
transport fix is a **token exchange**, not a raw credential handoff — the key
mechanical change from the earlier draft.

**Fix #1 — scope by declaration (#59, #388). PRIMARY risk reduction.** The DAG
declares `variables:` / `connections:` in `leoflow.yaml` (per DAG, optional
per-task narrowing, per ADR 0045 §Settled). Compiled into `dag.json`,
schema-validated, rejected at registration if a name is unknown. Enforcement is
**server-side**: `GetVariables` / `GetConnections` resolve the caller's task from
its token identity and return only that task's declared subset — never the whole
vault. Concretely:

- The declared set is threaded into the **today-empty** `GetVariablesRequest` /
  `GetConnectionsRequest`, and — because the agent is not trusted to filter for
  itself (it runs inside the task's blast radius) — the authoritative filter is
  applied against what storage returns for the caller's resolved task identity,
  not against what the agent claims.
- The `SecretsStore` interface (`internal/agentrpc/secrets.go:16`) grows from
  `SecretVariables(ctx, tenant)` / `SecretConnectionURIs(ctx, tenant)` to a form
  that takes the declared set (or the task identity from which the declared set is
  resolved), so the scoping is enforced in the query, not post-filtered in the
  handler.
- The `[]` case is explicit and load-bearing: **a DAG that declares no
  connections gets none** — not "all", which is today's behaviour and the bug.

**Scoping is only enforceable because the token reliably identifies the task —
so #59 ships WITH token liveness (Fix #2/#3), never alone.** Server-side scoping
keyed on the JWT's task identity is only meaningful if that identity is trustworthy
and current. A non-identifying or stale token makes the per-task filter bypassable
(the audit's exact path: a still-valid token fetching from outside its task). The
scope filter and the identity/liveness fixes are one shipment, not a sequence.

**Fix #2 — bind the token to task-instance liveness, and add revocation (#507).
No apiserver on this path.** `identify` (`server.go:449`) — or a wrapper the secret
RPCs call — additionally consults the **existing DB liveness predicate** already
stamped by `RecordHeartbeat` / surfaced as `ErrStaleReport`
(`server.go:243-266` — "reaped, or `try_number` moved past this attempt") plus a
revocation set. A token whose task instance is no longer live (terminal,
superseded by a later attempt, or reaped) **stops resolving secrets**, even though
its signature is still valid and its clock has not run out. So an exfiltrated
token expires with the *work*, not with the *clock*. This is the same liveness
signal `RecordHeartbeat` and `ReportState` already use for the `should_terminate`
kill-switch (#474), extended to the secret path where today there is none — a
**pure DB read, no `TokenReview`, no apiserver call**. Revocation covers the "task
still shows live but the operator wants the credential killed" case.

**Fix #3 — transport = token exchange (PRIMARY transport mechanism).** Replace the
plaintext `corev1.EnvVar{Value: req.AgentToken}` at `kubernetes.go:197` with a
**projected ServiceAccount token** the Pro task pod mounts (audience
`leoflow-control-plane`, pod-bound, short-lived, auto-rotated) — **nothing secret
sits on the `Pod` object.** The agent uses it *once*:

- At **`Register`**, the control plane calls Kubernetes **`TokenReview` once per
  pod** to validate the projected SA token. This call happens **inside the window
  the apiserver was already required to create the pod**, so the net added
  apiserver coupling is **≈ 0**.
- The control plane resolves **pod → task-instance** from the pod annotation and
  returns a **task-scoped Leoflow JWT** (the identity Fix #1 filters on and Fix #2
  checks for liveness).
- Steady-state secret RPCs (`GetVariables` / `GetConnections`, XCom, heartbeat)
  authenticate with **that Leoflow JWT** — so the **secret hot path is
  apiserver-free**. The projected SA token is a bootstrap credential exchanged
  exactly once, not a per-fetch capability.
- **`SecretKeyRef` is demoted to a fallback** for clusters that cannot project a
  service-account token; it leans on etcd encryption + RBAC (the hardening path
  ADR 0021 anticipated) and still keeps the credential off the *plaintext* pod
  spec.

The invariant this fixes is unconditional: **no bearer credential is ever a
plaintext field on the `Pod` object.** Both the projected-token path (primary) and
the `SecretKeyRef` path (fallback) satisfy it; the old `EnvVar{Value:...}` did not.

**Fix #4 — per-attempt TTL at dispatch.** The 24h `agentTokenTTL` issued at
`dispatch.go:109` is far longer than any task needs and is the window an
exfiltrated token exploits. Bind the TTL to the attempt's expected lifetime (with
headroom for retries/reschedule) rather than a flat day, replacing the
`agentTokenTTL=24h` value at the dispatch site. A warm worker (see Ordering) needs
a *rotating* per-attempt credential, not a 24h one, so this fix is also a
prerequisite for the pool design, not merely a hardening nicety.

**Rejected — pure `TokenReview`-per-fetch.** The tempting shortcut is to skip the
Leoflow JWT and have every secret RPC present the projected SA token, validated by
a `TokenReview` on each call. Rejected for two reasons: (1) it puts a `TokenReview`
on the **secret hot path**, loading exactly the **APF apiserver budget PR-10 exists
to protect** (ADR 0054 §4) — the reaper LIST storm's sibling; and (2) an SA token
**identifies the ServiceAccount, not the attempt**, so it cannot carry the
per-attempt identity Fix #1's scoping and Fix #2's liveness require. The exchange
(validate once, mint a task-scoped JWT) gets both: apiserver-free steady state and
an attempt-scoped identity.

Fixes 1+2 are the substance; 3+4 remove the transport and time exposure that make
a captured token dangerous. None of the four is a reason to defer the others —
each narrows a different dimension (what a token can fetch, when it works, who can
read it, how long it lives) — and Fix #1 is not independently shippable, because
the token must identify the task for the scope filter to bind (Fix #3).

## Ordering (hard — this gates the warm-worker roadmap)

**This ADR's fixes (scope #59 + token liveness + exchange) are a prerequisite for
the warm-worker regime (N:1, impl #2, ADR 0053 PR-N1 in the shared-cluster spec
§4.3–4.4).** The reason is structural, not stylistic:

- Today's dedicated pod (ADR 0002) holds a token for **one task instance's**
  lifetime. The exfil window is per-task.
- A warm worker holds a credential for the **whole pool's lifetime**, servicing
  many sibling tasks. With B1 open — 24h TTL, whole vault, no liveness — a warm
  pool turns a per-task exfil window into a **per-pool-lifetime** one, over the
  *entire* tenant vault. The regime change *widens* the exact hole this ADR closes.

**Therefore: warm-pool (impl #2) MUST NOT precede these fixes.** The enablers can
land first — the admission/placement layer (ADR 0053), the ADR 0051 state-machine
split, the GC work — but the shared-pod worker cannot ship until secret scoping,
token liveness, and the exchange transport are in.

**Warm-pool interaction — the exchange is exactly the shape a pool needs.** Under
a warm pool the two credentials cleanly separate:

- The **pool holds the projected SA token as a long-lived bootstrap** — audience
  `leoflow-control-plane`, pod-bound, used to authenticate the pool to the control
  plane, not to fetch any tenant's secrets.
- **Each task-attempt gets a fresh, per-attempt task-scoped Leoflow JWT delivered
  in-band** (the ADR 0052 / ADR 0053 impl-#2 transport — the same in-band,
  per-attempt channel the warm worker uses to report outcomes). That JWT **dies
  with the attempt** via the DB liveness predicate (Fix #2), which is precisely the
  property a warm pool requires: a credential that outlives the pool would recreate
  the per-pool-lifetime exposure. This is why the exchange design (not a static
  long-lived tenant token) is what makes the warm pool safe, and it confirms the
  ordering gate above.

## Consequences

- **A DAG that used an undeclared connection breaks.** This is the point, and it
  is breaking — same consequence ADR 0045 recorded. It needs the two-release arc
  ADR 0045 §Settled already specified: warn-and-deliver for one release (recording
  every undeclared runtime use, since static inference cannot see `get_connection`
  inside a `@task`), then flip warn to enforce. Eight of the 22 examples currently
  use a connection without declaring one; they are declared as part of the warn
  release so CI stays green.
- **The token can now be rejected mid-life.** A liveness/revocation consult means
  a valid-signature token can be refused — a new failure mode on the secret path.
  It must fail closed and legibly (the agent already tolerates a secret-fetch
  refusal: `secretsEnv` logs and skips, so a task that uses no secrets still runs),
  but a task that *needs* a secret after its TI is marked non-live now fails where
  before it silently succeeded from a stale token. That is the intended trade.
- **Transport change touches the pod spec, the agent bootstrap, and adds one
  apiserver call at Register.** Moving to the projected-token exchange (Fix #3)
  changes how the in-pod agent obtains its credential (`LEOFLOW_AGENT_TOKEN` becomes
  a mounted projected token exchanged for a Leoflow JWT at `Register`); the
  `SecretKeyRef` fallback is still an env var but sourced from a Secret. The one
  added apiserver `TokenReview` per pod lands inside the pod-creation window, so net
  added coupling is ≈ 0, and the secret hot path stays apiserver-free.
  `stripAgentOnly`'s allowlist stays as defence-in-depth regardless.
- **It does not fix Lite's fixed encryption key (#486).** Scoping reduces what one
  leak yields; it does not remove the at-rest key problem. Tracked separately; not
  a reason to defer this. (See Lite note.)
- **Two more places to keep in sync.** A connection renamed in the UI but not in
  `leoflow.yaml` now fails at registration rather than at run time — better, but
  new friction, and the error must name both sides. (Carried from ADR 0045.)

### Lite note (single-tenant, in-process delivery)

Lite runs the agent as a subprocess of the server, not in a pod, and delivery is
in-process. The **identity, liveness, and TTL fixes are uniform across editions** —
they are shared control-plane code and reuse the same `RecordHeartbeat` signal Lite
already stamps, so Fix #1 (scope by declaration), Fix #2 (liveness/revocation), and
Fix #4 (per-attempt TTL) apply to Lite unchanged in shape. **Only the transport
forks:** the projected-SA-token exchange (Fix #3) is **Pro-executor-only**, behind
the executor interface — Lite has no pod, no pod spec, and no `podEnv`, so there is
no plaintext-on-the-Pod-object exposure to fix and no `TokenReview` to call; Lite's
in-process credential handoff is unchanged. The **whole-vault issue also does not
bite the same way** on Lite: it is single-tenant, so "the whole tenant vault" is
"this operator's own vault" — no other team's credentials to leak across — and the
dangerous cross-namespace `get pods` read (OPEN #1) has no Kubernetes pod object to
read from at all. The design goal is that a Lite install sees **no behavioural
change** from this ADR: scoping to declared names is a tightening (not a break) for
a single tenant, liveness reuses a signal Lite already has, and the transport fork
never runs on the Lite path. Note the *separate* Lite exposure — the subprocess
executor appends to `os.Environ()`, so the agent inherits `LEOFLOW_SECRET_KEY` /
`LEOFLOW_AUTH_JWT_SECRET` — is handled by `stripAgentOnly`'s allowlist (already
landed, #476), not by this ADR.

## Alternatives

- **Ship scoping (Fix #1) alone, defer the token fixes.** Rejected — it is the trap
  ADR 0045 already named, and it is now *structurally* impossible: server-side
  scoping is keyed on the token's task identity, so a non-identifying or stale token
  makes the filter bypassable. Whole-vault delivery restricted to declared names is
  worthless while a captured, still-live token fetches the vault directly from
  outside any task. The audit proved this exact path. Scope and liveness/identity
  ship together or the scope is decorative.
- **Pure `TokenReview`-per-fetch (no Leoflow JWT).** Rejected — see the Decision's
  rejection: it loads the APF budget PR-10 protects (a `TokenReview` on the secret
  hot path) and an SA token identifies the SA, not the attempt, so it cannot carry
  the per-attempt identity scoping and liveness need. The exchange validates once
  and mints an attempt-scoped JWT, getting both properties.
- **Filter in the agent.** Cheaper and worthless: the agent runs inside the
  container it is filtering for, so anything it can fetch, user code can fetch
  (ADR 0004). Enforcement is server-side. (Carried from ADR 0045.)
- **Argo-style `secretKeyRef` for the secrets themselves.** Good and wrong for us —
  it requires the DAG author to know Kubernetes Secret names and keys, which breaks
  the Airflow connection abstraction Leoflow exists to preserve. We take the
  *declaration* idea and keep our own resolution. (Note: `secretKeyRef` remains the
  right *fallback* for the agent-token transport in Fix #3 — a Kubernetes
  credential, not a user-facing connection — which is a different object.)
- **Namespace-per-tenant as the boundary.** Kubeflow's posture; presumes
  multi-tenancy that is future here, and the comparative study showed it delivers no
  isolation its API appears to promise (the pipeline SA gets `secrets:
  [get,list,watch]` on the whole namespace). Rejected for the same reason ADR 0045
  rejected it. (ADR 0054's namespace split *shrinks* the B1 blast radius but does
  not pretend to close it — that is this ADR's job.)
- **Leave the 24h TTL, rely on signature + audience.** Rejected — audience
  separation (an agent token cannot be replayed as an API token) is real and
  confirmed, but it does not bound *time* or *scope*. A 24h whole-vault token that
  outlives its task is the finding, not a mitigation of it.
- **Do warm-pool first, harden later.** Rejected explicitly — it inverts the
  ordering above and converts a per-task window into a per-pool-lifetime one. The
  hardening is the gate, not a follow-up.

## Verify at implementation

- Exact `TokenReview` bound-token `extra` keys
  (`authentication.kubernetes.io/pod-name`, `authentication.kubernetes.io/pod-uid`)
  for the target apiserver version, used to resolve pod → task-instance.
- Projected-token minimum-TTL floor (~10 min) versus the shortest expected
  attempt, so a very short task's bootstrap token has not already expired at
  `Register`.
