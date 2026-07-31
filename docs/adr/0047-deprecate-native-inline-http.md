# ADR 0047: Deprecate the native inline http_api; run HTTP through the generic pod executor

**Status:** Accepted
**Date:** 2026-07-31
**Amends:** ADR 0040 (native fast path — removes HTTP from it), ADR 0002 (pod-per-task — removes the inline exception for HTTP)
**Companions:** ADR 0021 (secret delivery), ADR 0038 (`connectors:` sugar), the task-pod NetworkPolicy work
**Issues:** H5 (audit finding), #504 (the app-level guard this supersedes)

## Context

ADR 0040 placed `http_api` on the **native fast path**: an `HttpOperator` compiles
to a lightweight `http_api` task that the control plane runs **inline** — as a
goroutine *in the control-plane process*, issuing the HTTP request itself, no pod.
It was an optimization: no pod startup for a simple GET.

That inline execution is the mechanism behind a server-side request forgery
(audit finding **H5**). Because the request runs in the control-plane process, it
carries the **control plane's network position**: a `write:dag` author (not
necessarily an admin) can point the URL at the cloud metadata endpoint
(`169.254.169.254`), the kube-apiserver, or any in-cluster service, and read the
response body back as XCom. The control plane's egress **cannot be
network-policied shut** — it must reach the apiserver and the datastores — so
there is no clean network-layer fix while the request originates there.

A first attempt (#504) guarded the inline executor with an application-level IP
blocklist (deny loopback/link-local/private). It was the **wrong layer**: it
broke the alerting E2E (a legitimate http_api task to a loopback receiver) and,
worse, it breaks a core orchestration pattern — a DAG calling an internal service
is normal, and the app cannot tell the kube-apiserver from a legitimate internal
microservice by IP.

Argo and Kubeflow do not have this surface: **neither runs user HTTP in the
controller.** Argo's `http` template runs in an **agent pod**; Kubeflow runs every
step in a pod. The request gets the pod's own identity and is bounded by
NetworkPolicy — the k8s-native control. Both comparative studies recommend a
task-pod default-deny-egress NetworkPolicy as the answer, not an app blocklist.

## Decision

**Deprecate the native inline `http_api` execution. `HttpOperator` runs through the
generic pod executor (ADR 0040 Phase A), like any other provider operator.**

1. **Parser.** Stop mapping "Http in the operator name" to the native `http_api`
   type. `HttpOperator` falls through to the generic path
   (`__leoflow_operator_class__`), where the real `airflow.providers.http`
   operator's `.execute(context)` runs in the **task pod**.

2. **Deprecate the inline path.** The `http_api` task type and its inline executor
   (`internal/executor/inline_http.go`, `inline_runner.go`, the scheduler's
   `s.inline`/`runInline` wiring) are deprecated: kept one release behind a compile
   warning, then removed.

3. **Remove the app-level SSRF guard.** The inline dial-control (#504) goes with
   the inline executor. It is unnecessary once HTTP runs in a pod.

4. **Metadata/SSRF becomes a network-layer, all-tasks concern.** The residual
   concern — a task pod reaching `169.254.169.254` or the apiserver — is **not
   HTTP-specific** (every task pod, bash/python/operator, can reach it) and is
   addressed by the **task-pod default-deny-egress NetworkPolicy** (deny
   `169.254.0.0/16` and the apiserver range) plus Workload Identity / metadata
   concealment where the cloud provides it. Same control for all task types,
   tracked separately.

**Interim, deliberately.** "Use Airflow's HttpOperator for now": the native fast
path for HTTP may return later, done correctly — running in a pod like Argo's
agent, or with a real sandbox — once there is a proven reason the pod cost is
unacceptable and a design that does not reintroduce the control-plane surface.

### 5. The invariant this establishes (so the design does not come back)

The concrete fix is "HTTP runs in a pod." The **durable rule underneath it**,
which all future work must honor, is more general:

> **No user-influenced network request or user code executes in the
> control-plane process.** A URL, header, body, entrypoint, or code path an
> author (or a run's `conf`) can influence must run in a **task pod** — its own
> identity, subject to NetworkPolicy — or a real sandbox. **Never inline in the
> control plane**, whose network position (apiserver, datastores, cloud
> metadata) and secret access cannot be constrained per-request.

The inline `http_api` was one instance of breaking this rule. Stating the rule,
not just fixing the instance, is what catches the *next* temptation — a
"lightweight inline SQL executor", a template renderer that fetches a URL, an
"inline" anything justified by avoiding pod startup. Those are the same bug class
and this ADR rejects them in advance.

**Fences on re-introducing a native/fast HTTP (or any inline user-execution)
path:**

1. It runs in a **pod or a sandbox** — the "done right" above. **Inline-in-the-
   control-plane is not a permitted option, ever.** A PR proposing it must
   *supersede this ADR* with an argument for why the control plane's network
   position is safe for author-controlled requests — which it is not.
2. The guard is **structural, not a flag.** Once the inline executor is removed
   (phase 2 of this ADR), there is no inline path to re-enable behind a config
   value. Bringing user execution back into the control plane requires adding a
   new capability in a visible, reviewable diff — not flipping a default — and
   that diff trips rule 1.

## Consequences

**The control-plane SSRF is eliminated.** The HTTP request no longer originates
from the control plane. What remains is the general "untrusted code in a pod can
make network requests" situation, containable by the task-pod NetworkPolicy — the
standard, cloud-native shape, not a design flaw.

**HTTP tasks cost a pod now.** Inline avoided pod startup (~ms) for a simple
request; pod-based adds seconds of startup and pod resources per HTTP task. A DAG
with many HTTP calls is slower and heavier. This is the accepted trade — security
and a thinner control plane over the inline optimization.

**The task image needs `apache-airflow-providers-http`.** The `connectors:` sugar
(ADR 0038) supplies it; existing HTTP DAG images must add the provider. The
compile error names it.

**Less control-plane code.** The inline HTTP executor, its runner, the scheduler
wiring, and the SSRF guard are removed — the control plane runs less
untrusted-input-shaped code in-process.

**The alerting E2E changes.** `lite-alerts.sh` drove a failure with an inline
http_api task hitting a loopback receiver; a task pod cannot reach the host's
loopback, so the failure trigger is reworked (a pod-reachable receiver, or a
different failure trigger).

**One thing to prove, not assume.** The generic path runs operators via
`.execute(context)` with a synthesized context (ADR 0040); operators using
`self.defer`/`on_kill`/`context['dag_run']` misbehave. `HttpOperator` is
synchronous and simple and *should* run cleanly — but this is verified with a real
pod E2E **before** the native path is removed, not asserted (the "both ends is not
the chain" discipline).

## Alternatives rejected

**Keep inline, guard with an app-level IP blocklist (#504).** The wrong layer: it
cannot distinguish the apiserver from a legitimate internal service by IP, it
breaks legitimate internal calls and the E2E, and it needs per-deployment tuning
the app cannot do. The control plane's egress cannot be locked down anyway.

**Keep inline, block only the metadata endpoint in the app.** Better — metadata is
the one unambiguous target — and a reasonable stopgap. But it leaves the apiserver
and other internal services reachable from the control plane's position, and it
keeps the control-plane HTTP surface and its code. Moving to a pod removes the
surface entirely and makes metadata the general task-pod concern, not an HTTP
special case.

**Run inline HTTP in an agent pod (Argo's model) now.** The correct long-term
native answer, but more work than routing to the generic path that already exists
(ADR 0040 Phase A). Deferred as the "done right" re-introduction.
