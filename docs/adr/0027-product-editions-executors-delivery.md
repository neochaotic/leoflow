# ADR 0027: Product Editions — Executors and Delivery

**Status:** Accepted
**Date:** 2026-05-27
**Deciders:** Project founder
**Relates:** ADR 0015 (Kubernetes as the sole container execution path), ADR 0026 (Lite datastore)

## Context

Leoflow ships two editions, but the contract for *what each edition runs and how
it is delivered* lived only implicitly — scattered across the `--executor` flag
help, `leoflow doctor`, ADR 0026, and tribal knowledge. The editions differ on
two axes that several other decisions depend on (datastore in ADR 0026,
packaging, supported executors), so they deserve one canonical statement.

The codebase has four executors behind the Router: `kubernetes`, `docker`,
`subprocess`, and `inline` (HTTP). `leoflow doctor` already states the operating
philosophy: **Kubernetes (local k3d or a real cluster) is the sole container
isolation path; subprocess is the dev-only, unisolated escape hatch.**

## Decision

### Pro (the main product)

- **Delivery:** **only** the Helm chart on Kubernetes. There is no binary/Docker
  distribution of Pro.
- **Executor:** **only** the Kubernetes executor — real pod-per-task on the
  target cluster.
- **Datastore:** external managed Postgres + external managed Redis, wired by the
  chart (ADR 0026).

### Lite

- **Delivery:** a self-contained, **installable binary** (`install.sh` /
  `leoflow setup`), no Docker required for the control plane or datastore.
- **Executors (selectable via `--executor`):**
  - `k8s` (default) — a dedicated local **k3d** mini-cluster, real pod-per-task,
    highest fidelity. Needs Docker + k3d, fetched on demand.
  - `subprocess` — runs the agent on the host with **no isolation**; the
    dev-only escape hatch, zero external dependencies.
- **Datastore:** embedded managed Postgres, **no Redis**, no Docker (ADR 0026).

### Why not a Docker-socket executor (security reinforces ADR 0015)

A Docker (daemon-socket) executor looks like the obvious local container path,
since Lite users often already have Docker. **ADR 0015 already rejected it** —
for supply chain (the Docker SDK drags in the Moby tree with an unfixable
advisory that fails the security gate) and architectural coherence (one
container API, not two). This ADR records the **security** reason that reinforces
that, and that drove keeping it rejected specifically for Lite:

> Launching task containers through the Docker daemon means giving the control
> plane the Docker socket, and **access to the Docker socket is equivalent to
> root on the host** — a container can bind-mount `/` or run `--privileged` and
> escape. Because Lite runs the user's own DAG code, a Docker-socket executor
> would turn "author a DAG" into "own the machine," with no isolation boundary
> between orchestrator and workload.

So Lite's container path is a local **k3d** mini-cluster (per ADR 0015): tasks
run as pods with real namespace/cgroup isolation, reached via the Kubernetes API
— the orchestrator and task code never hold the host's Docker socket. (k3d uses
Docker only as the *cluster runtime*; that is not a task-launch socket exposed to
DAG code.) `subprocess` remains only as the explicitly unsandboxed, dev-only
escape hatch, labeled as such at startup.

The `docker` executor still present in the Router is therefore **legacy/internal**
— not a promoted or supported tier of either edition — and a candidate for
removal.

## Consequences

- **Pro is Kubernetes-native end to end** — no subprocess/docker executor and no
  binary delivery in production. This keeps the supported surface small.
- **Lite spans the full local range on one binary:** zero-dependency
  (`subprocess`) up to high-fidelity local pods (`k8s`/k3d), with an embedded,
  Docker-free datastore.
- **Two delivery pipelines:** the Helm chart (Pro) and the binary installer
  (Lite). Release tooling and docs treat them separately.
- The Docker executor's non-promotion should be reflected in `doctor`/docs, and
  its eventual deprecation tracked.
- ADR 0026's datastore split lines up exactly with this edition split (Redis
  present ⇒ Pro; absent ⇒ Lite).
