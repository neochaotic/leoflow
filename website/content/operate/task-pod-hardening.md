---
title: Task pod hardening
description: What the executor puts in every task pod's securityContext, what readOnlyRootFilesystem changes, and how to run task pods in a Pod Security Admission `restricted` namespace.
weight: 78
---

Every task runs in its own pod. This page covers the security posture those pods
get by default, the one knob that tightens it further, and what to expect when a
cluster policy audits them.

## What you get without configuring anything

The executor builds each task container with:

| field | value | always emitted? |
|---|---|---|
| `allowPrivilegeEscalation` | `false` | yes |
| `capabilities.drop` | `["ALL"]` | yes |
| `seccompProfile` | `RuntimeDefault` | yes |
| `automountServiceAccountToken` | `false` (pod level) | yes |
| `runAsNonRoot` | `true` | follows `taskPodSecurity.runAsNonRoot` (default `true`) |
| `fsGroup` | `65532` (pod level) | only when `runAsNonRoot` is on |

The first four cost an ordinary task nothing and do not depend on the UID, so
the executor sets them whatever the image runs as. The last two move together:
turn `taskPodSecurity.runAsNonRoot` off for a fleet whose images legitimately
run as root, and the executor drops `runAsNonRoot` *and* the pod-level `fsGroup`
— a root task already writes its volumes as root, so there is nothing to fix,
and leaving the pod context unset skips the kubelet's recursive volume chown.

Two of these are worth understanding rather than just reading.

**`runAsNonRoot` without `runAsUser`.** Leoflow deliberately does not pin a UID.
The kubelet resolves the image's own `USER` and refuses the pod if it resolves
to root. That means a **numeric** `USER` in your DAG image — `USER 65532:65532`,
which the Leoflow base image uses — is required: a symbolic `USER myuser` cannot
be resolved by the kubelet before the container starts, and the pod fails
admission with `CreateContainerConfigError`. If you build a DAG image from
something other than our base, make the final `USER` numeric.

**No ServiceAccount token.** Task pods authenticate to the control plane with
their own per-task token over gRPC and never call the Kubernetes API, so
mounting a SA token would hand a credential to untrusted code for no reason.

## `readOnlyRootFilesystem`

```yaml
taskPodSecurity:
  readOnlyRootFilesystem: true   # default: false
```

With this on, the task container's root filesystem is mounted read-only and the
executor mounts an `emptyDir` at `/tmp` so anything that needs scratch space
still has it. The Leoflow base image already points dbt's target, log and
profiles paths at `/tmp`, so a dbt task needs no further configuration.

**It is off by default** because a DAG image that writes anywhere outside `/tmp`
— a pip install at runtime, a library caching into `$HOME`, a tool writing next
to its input — stops working the moment you turn it on. Turn it on deliberately,
and run your DAGs once after.

Two things to know before you rely on it:

- **The `/tmp` emptyDir has no `sizeLimit`.** The volume itself is unbounded, but
  the DAG author already controls this per task: set
  `resources.limits.ephemeral_storage` in `leoflow.yaml` (ADR 0054), which the
  kubelet enforces against the task container. A namespace `LimitRange` default,
  or failing that the node's own eviction threshold, is the cluster-side backstop
  — reach for it only when you cannot change the DAG.
- **Airflow logs a warning in every task.** With a read-only root, Airflow cannot
  create `/home/leoflow/airflow/logs` and says so:
  `Could not create log folder … Read-only file system … Airflow will continue`.
  It is harmless — Leoflow captures task output over its own channel, unaffected
  — but it appears at the top of every task log.

## Running in a `restricted` namespace

The defaults above satisfy Pod Security Admission's `restricted` profile, so you
can label the task namespace and task pods will be admitted:

```bash
kubectl label ns <taskNamespace> pod-security.kubernetes.io/enforce=restricted
```

Nothing else is required. `restricted` does **not** require
`readOnlyRootFilesystem`; the two are independent, and you can enable either
without the other.

## Verifying it

On a running task pod:

```bash
kubectl get pod <task-pod> -n <taskNamespace> -o jsonpath='{.spec.containers[0].securityContext}{"\n"}{range .spec.volumes[*]}{.name} {end}{"\n"}'
```

With `readOnlyRootFilesystem: true` you should see `readOnlyRootFilesystem=true`,
`runAsNonRoot=true`, and a `leoflow-tmp` volume among the pod's volumes. Task
pods are short-lived, so read this while one is running, or from a pod that has
finished but not yet been garbage-collected.

**Verified on GKE 1.35 (Standard):** task pods admitted by a
`restricted`-enforcing namespace with `readOnlyRootFilesystem: true`, running a
hybrid DAG whose dbt models materialized normally.

## Related

- [Control-plane HA and disruption posture](/operate/control-plane-ha/)
- [Agent credential transport](/operate/agent-credential-transport/)
