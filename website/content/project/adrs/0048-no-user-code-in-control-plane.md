---
title: "ADR 0048: The control plane executes no user-influenced code or network requests"
linkTitle: 0048 · The control plane executes no user-influenced code or network requests
weight: 480
description: "ADR 0048: The control plane executes no user-influenced code or network requests"
---

**Status:** Accepted
**Date:** 2026-07-31
**Generalizes:** ADR 0047 (inline http_api was one instance)
**Relates:** ADR 0002 (pod-per-task), ADR 0004 (thin agent), ADR 0021 (secret delivery), ADR 0040 (operator support)

## Context

Leoflow's control plane holds the keys to the cluster: it reaches the
kube-apiserver (to create pods), the datastores, the tenant's decrypted secrets,
and — from inside the cluster network — the cloud metadata endpoint and every
in-cluster service. Its network position and privilege **cannot be constrained
per-request**: a NetworkPolicy that denied its egress would stop it scheduling.

So anything the control plane executes runs with that full reach. When the thing
it executes is **influenced by a DAG author or a run's `conf`**, an author turns
"I can define a workflow" into "I can act with the orchestrator's privilege."
ADR 0047 found one instance: the inline `http_api` executor fetched an
author-supplied URL *in the control-plane process* and returned the body as XCom
(SSRF, finding H5). The pattern is not specific to HTTP.

Argo and Kubeflow do not have this class of problem because **neither runs user
work in the controller** — Argo uses an agent pod, Kubeflow runs every step in a
pod. The request or code gets the pod's own identity and is bounded by
NetworkPolicy. That is the shape Leoflow already commits to for tasks (ADR 0002)
and for the agent (ADR 0004, "all state goes through gRPC; no Kubernetes API
calls").

## Decision

**No user-influenced code path or network request executes in the control-plane
process. It runs in a task pod (its own ServiceAccount, subject to NetworkPolicy)
or a real sandbox — never inline in the control plane.**

"User-influenced" means any value or code an author or a run can determine: a
URL, header, body, query, template, entrypoint, image, operator, or the code of a
`@task`. Isolation is **structural** — a pod boundary or a sandbox — not an
application-level check on the value (a blocklist cannot tell a legitimate
internal service from the apiserver, and it is the wrong layer, ADR 0047).

This is the general rule ADR 0047 applied to HTTP. It also forecloses the *next*
temptation of the same shape:

- an "inline" SQL executor that runs a query in the control plane,
- a template/expression renderer that fetches a URL or imports a module,
- any "run X in-process to skip pod startup" optimization over author input.

All of them are this bug class and are rejected here in advance.

## What this does not forbid

- **Fixed, operator-configured work in the control plane.** The scheduler,
  reapers, dispatch, alert delivery to an operator-managed connection — these act
  on operator/system input, not author input, and stay in-process.
- **Author code in a pod.** That is the normal, intended path (ADR 0002). Its
  residual risk (a pod reaching metadata or a private service) is contained at the
  network layer by the task-pod default-deny-egress NetworkPolicy — the same
  control for every task type — plus Workload Identity / metadata concealment.

## Consequences

**A design that would run author-influenced work in the control plane is rejected
without re-litigation.** A PR proposing one must supersede this ADR with an
argument for why the control plane's network position and secret access are safe
for that input — which, by construction, they are not.

**The guard is structural, not a flag.** Bringing user execution into the control
plane requires adding a capability in a visible, reviewable diff, not flipping a
default. Reviewers cite this ADR.

**Cost is paid in pods.** Isolation via a pod costs startup latency and resources
that an in-process path would not. That cost is accepted as the price of a control
plane that cannot be turned against the cluster by a workflow definition. Where
the cost is genuinely unacceptable, the answer is a sandbox or an agent pod (ADR
0047's "done right"), not a return to in-process execution.

## Alternatives rejected

**Guard the in-process path with input validation (allow/deny lists).** The wrong
layer, and incomplete: it cannot classify author input safely (ADR 0047's IP
blocklist could not tell the apiserver from a legitimate microservice), it breaks
legitimate use, and it leaves the privileged execution context in place. The
boundary must be the process, not the value.

**Case-by-case.** Deciding per feature invites the same mistake repeatedly
(inline http was shipped, then found). A stated invariant is cheaper than
re-discovering the class each time.
