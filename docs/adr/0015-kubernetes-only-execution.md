# ADR 0015: Kubernetes as the Sole Container Execution Path (No Docker SDK)

**Status:** Accepted
**Date:** 2026-05-22
**Deciders:** Project founder

## Context

ADR 0002 (pod-per-task) established ephemeral containers as the only execution
model and noted that, in standalone mode, a task would run "either a Docker
container (default) or a subprocess." Phase 3 implemented a `DockerExecutor`
using the official Docker SDK (`github.com/docker/docker`) to realize the
standalone-Docker path.

That introduced two concrete problems:

1. **Supply chain.** The Docker SDK pulls the entire Moby dependency tree and,
   at the time of writing, carries an advisory with no fix available
   (GO-2026-4887, Moby AuthZ plugin bypass). `govulncheck` flags it as reachable
   through the package's `init`, so importing the SDK into the control plane
   binary fails the CI security gate (ADR 0014) with no remediation path.
2. **Architectural coherence.** Depending on the Docker SDK means the control
   plane speaks *two* container APIs (Kubernetes and Docker), each with its own
   lifecycle, watch, and cleanup semantics — exactly the kind of dual surface
   ADR 0001 set out to avoid.

The question is how to keep "run each task in an isolated container locally"
without importing the Docker SDK into the control plane.

## Decision

**Kubernetes is the sole container execution path.** The control plane creates
ephemeral pods via `client-go` (which it already depends on) for both production
and local development. Local "Docker" execution becomes **local Kubernetes** on
a single-node cluster (k3d / kind / minikube / Docker Desktop's Kubernetes),
which itself runs on the developer's Docker.

The control plane does **not** import the Docker SDK (`github.com/docker/docker`)
or any container-engine client other than `client-go`.

A `SubprocessExecutor` remains as an explicit, dev-only escape hatch that runs
the agent directly on the host with **no isolation** (and a loud warning). It is
not a container path; it exists for fast local iteration without any cluster.

This refines the standalone-mode wording of ADR 0002: standalone *container*
execution is local Kubernetes, not a direct Docker integration.

## Rationale

- **Clean supply chain.** Removing the Docker SDK eliminates the unfixable
  GO-2026-4887 reachable vulnerability and a large transitive dependency tree,
  keeping the CI security gate (ADR 0014) green and the server binary small.
- **One execution path.** The same `KubernetesExecutor`, pod spec, watcher, and
  cleanup logic serve both local and production. Less code, fewer edge cases,
  identical behavior across environments.
- **Ecosystem precedent.** This mirrors how the cloud-native ecosystem evolved:
  Argo Workflows deprecated and removed its Docker executor in favor of
  Kubernetes-native executors, and Kubeflow Pipelines runs on Kubernetes
  (Argo/Tekton) without embedding the Docker SDK in its control plane, delegating
  image builds to out-of-process tools (Kaniko/BuildKit).
- **`govulncheck` reachability fits.** Depending only on `client-go` lets the
  call-graph analysis suppress the inevitable unreachable transitive advisories,
  rather than failing on a directly-reachable, unfixable one.

## Consequences

- **Local dev requires a local Kubernetes cluster.** The developer guide
  recommends `k3d cluster create` (or kind). This raises the local setup bar
  versus a bare Docker daemon, but unifies the execution model.
- **The `DockerExecutor` and the Docker SDK dependency are removed.** If a
  SDK-free Docker mode is ever desired (e.g., shelling out to the `docker` CLI or
  talking to the daemon socket over `net/http`), it can be added later as a
  separate, optional executor without reintroducing the SDK; this ADR does not
  forbid that, only the in-process SDK dependency.
- **Image build stays out-of-process.** Consistent with this decision, the
  `leoflow compile` image build (ADR 0003, issue #7) should use an out-of-process
  builder (Kaniko/BuildKit/`docker build` shell-out), not the Docker SDK.
- **Subprocess mode is dev-only and unisolated**, gated behind an explicit
  opt-in and a runtime warning.

## Alternatives Rejected

- **Docker SDK in the control plane (the Phase 3 `DockerExecutor`):** rejected
  for the supply-chain and coherence reasons above.
- **Shell out to the `docker` CLI:** viable and SDK-free, but adds a second
  container API surface and requires the `docker` CLI on the host; deferred as an
  optional future executor, not the default.
- **A separate out-of-process executor binary that imports the SDK:** isolates
  the dependency from the server binary but still ships the unfixable
  vulnerability somewhere; not worth the added moving part for a dev convenience.
