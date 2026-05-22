# Phase 3 Prompt — Executor and Agent

## Goal

Make tasks actually run. Implement the K8s executor, the standalone executor (Docker + subprocess), the `leoflow-agent` binary, the gRPC channel between Control Plane and Agent, and the official `leoflow/python-runtime` base images.

By the end of this phase, a `dag.json` pushed in Phase 2 actually executes to completion. Logs are captured to local disk (S3/GCS comes in Phase 4). XCom is still stubbed (Phase 4).

## Prerequisites

- Phases 1 and 2 complete.
- Local Kubernetes cluster available (k3d, kind, or minikube) for K8s mode testing.

## Constraints

- The agent is a **statically linked Go binary**. Build with `CGO_ENABLED=0`.
- gRPC code is generated from `proto/agent.proto` (already exists). Use `buf` for generation.
- Base images are **slim**: Debian slim + the specific Python version + the agent binary. No extra layers.
- The agent must handle SIGTERM gracefully and propagate to the user's child process.
- **ADRs 0011 (TDD), 0012 (A+), 0014 (Supply Chain) govern every change.** Coverage floor: **75% per package**. A+ maintained, GoDocs on all exports, govulncheck clean.

## TDD Workflow Notes for This Phase

The executor and agent involve external systems (Docker, Kubernetes, gRPC). TDD applies, but with attention to test layering:

- **Unit tests** for pure logic: pod spec construction, env var injection, retry logic, state transitions in the agent. Mock the K8s/Docker clients with gomock.
- **Integration tests** (`//go:build integration`) for actual interactions: real Docker daemon for `DockerExecutor`, kind/k3d cluster for `KubernetesExecutor`. testcontainers-go bootstraps both.
- **End-to-end test** for the full smoke path described below.

Mandate: every executor implementation has a unit test suite that hits the mock client and verifies the right calls in the right order. Then a small integration test confirms the mock-tested behavior actually works against the real system.

Test the agent's spec parsing, XCom env injection, and retry/backoff logic with unit tests BEFORE writing the gRPC client code.

## Deliverables

### 1. gRPC code generation

- `buf.yaml`, `buf.gen.yaml` in the repo root.
- `make proto` generates Go code into `proto/agent/v1/`.
- The generated code is committed to the repo (do not require buf at runtime).

### 2. The Agent binary

`cmd/leoflow-agent/main.go`:

Startup sequence:

1. Read environment:
   - `LEOFLOW_CONTROL_PLANE_ADDR` — gRPC endpoint of the Control Plane.
   - `LEOFLOW_AGENT_TOKEN` — short-lived JWT identifying this task instance.
   - `LEOFLOW_TASK_INSTANCE_ID` — UUID of the task instance.
2. Establish gRPC connection (TLS in K8s mode, plaintext in standalone dev).
3. Call `Register`. Validate session.
4. Call `GetTaskSpec`. Receive operator type, entrypoint, env.
5. For each declared XCom input, call `FetchXCom` and inject as env (`LEOFLOW_XCOM_<param_name>` = JSON value).
6. Spawn the user process:
   - `python` for python operator: `python -c "import importlib; m, f = '<entrypoint>'.split(':'); getattr(importlib.import_module(m), f)()"`
   - `bash` for bash operator: `bash -c "<entrypoint>"`
   - http_api operator is handled by the Control Plane, not the Agent.
7. Stream stdout/stderr line by line via `StreamLogs`.
8. On process exit:
   - If exit code 0: capture optional return value (next bullet), call `PushXCom`, then `ReportState(SUCCESS)`.
   - Otherwise: `ReportState(FAILED, exit_code, error_message)`.
9. Exit with the same code as the user process.

Return value capture: the agent reads from a known file path `/tmp/leoflow_return_value.json` if it exists. The Python helper library (shipped in the base image) provides a `leoflow.set_return_value(obj)` function that writes to this path.

Signal handling:

- SIGTERM received → propagate to user process, wait up to 30s for graceful exit, then SIGKILL.
- Connection to Control Plane lost → retry with exponential backoff (1s, 2s, 4s, 8s, 16s), max 5 attempts, then fail the task.

### 3. The Executor router

`internal/executor/router.go`:

```go
type Executor interface {
    Execute(ctx context.Context, ti *TaskInstance, spec *TaskSpec) error
    // Watch reports terminal events (pod completion, container exit) back to the scheduler.
    Watch(ctx context.Context, events chan<- ExecutorEvent) error
}
```

Implementations:

- `KubernetesExecutor` — uses client-go.
- `DockerExecutor` — uses the Docker socket.
- `SubprocessExecutor` — for `--mode=dev` only; emits a runtime warning.
- `InlineHTTPExecutor` — for `type=http_api`; runs in-process as a goroutine. No agent.

The router picks an executor per task based on:

1. `type=http_api` → `InlineHTTPExecutor`.
2. Else, deploy mode (`k8s` or `standalone`) decides.

### 4. KubernetesExecutor

For each `Execute`:

1. Build a `corev1.Pod` spec:
   - `metadata.name`: `leoflow-{dag_id}-{task_id}-{try}-{rand}` (sanitized)
   - `metadata.labels`: `leoflow.io/dag-id`, `leoflow.io/task-id`, `leoflow.io/run-id`, `leoflow.io/try-number`, `leoflow.io/tenant-id`
   - `metadata.annotations`: `leoflow.io/task-instance-id`
   - `spec.containers[0]`:
     - `image`: from DAG spec
     - `imagePullPolicy`: from DAG spec
     - `env`: includes `LEOFLOW_*` (control plane addr, token, task instance ID)
     - `resources`: from DAG spec
   - `spec.nodeSelector`: from DAG spec
   - `spec.tolerations`: from DAG spec
   - `spec.restartPolicy`: `Never`
   - `spec.activeDeadlineSeconds`: from execution timeout
2. Create the pod via `client-go`.
3. Update task_instance: `state=queued`, `pod_name=<name>`.

A separate **Watcher** goroutine uses K8s informers to:

- Detect pod `Running` → set task_instance `state=running`.
- Detect pod terminal phase (`Succeeded`/`Failed`) → reconcile with `ReportState` calls already made by the agent. If the agent never reported (e.g., pod OOMKilled before agent connected), set the task to `failed` with reason from pod status.

Special handling:

- `Pending` for more than 5 minutes (or configurable): emit a warning metric. Do not fail.
- `ImagePullBackOff` / `ErrImagePull`: fail the task with the K8s reason as `error_message`.
- `OOMKilled`: fail with `error_message="oom_killed"`.

Pod cleanup: by default, pods are deleted 1 hour after terminal phase (configurable). This is set via `ttlSecondsAfterFinished` on a Job wrapper — actually use a `Pod` directly with a cleanup goroutine, since `ttlSecondsAfterFinished` only works on Jobs.

### 5. DockerExecutor

Same shape as KubernetesExecutor but using the Docker SDK:

- `containerCreate` with the same env vars.
- Stream the container logs.
- On exit, treat the exit code identically.

### 6. SubprocessExecutor

`os/exec` to run the entrypoint directly **on the host machine**, no isolation. Used only when:

- `mode=dev`
- The user explicitly opts in via `--allow-subprocess`

On startup, the executor logs a prominent warning:

```
WARN: subprocess executor active; user code runs without isolation. Do NOT use in production.
```

### 7. InlineHTTPExecutor

For `type=http_api`:

- Goroutine in the Control Plane.
- Uses `net/http` with a custom transport: 3 retries, exponential backoff, configurable timeout.
- On success codes (default 2xx), pushes the response body as XCom (Phase 4 stub: log it instead).
- On failure, marks task `failed`.

Does **not** create a pod. Does not run the agent.

### 8. Base images

`runtime/python-3.10/Dockerfile`, `runtime/python-3.11/Dockerfile`, `runtime/python-3.12/Dockerfile`:

```dockerfile
ARG PYTHON_VERSION=3.11
FROM python:${PYTHON_VERSION}-slim-bookworm AS runtime

# Install minimal system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install the leoflow Python helper library
COPY ./helper /opt/leoflow-helper
RUN pip install --no-cache-dir /opt/leoflow-helper

# Copy the statically built agent binary
COPY --from=agent-builder /agent /usr/local/bin/leoflow-agent

# Non-root user
RUN useradd --create-home --shell /bin/bash leoflow
USER leoflow
WORKDIR /home/leoflow

ENTRYPOINT ["/usr/local/bin/leoflow-agent"]
```

The helper library (`runtime/helper/`) provides:

```python
# leoflow/__init__.py
from leoflow.runtime import set_return_value, get_xcom, get_logger
```

- `set_return_value(obj)` — JSON-encodes and writes to `/tmp/leoflow_return_value.json`.
- `get_xcom(name)` — reads from `LEOFLOW_XCOM_<name>` env var, JSON-decodes.
- `get_logger(name)` — returns a Python logger that emits JSON-formatted records to stdout.

### 9. `leoflow compile` integration

In Phase 1, `compile` produced a `dag.json` and stubbed image build. In Phase 3:

- If `leoflow.yaml` has no `build.dockerfile`, autogenerate a Dockerfile based on the matching base image plus `dependencies` and `system_packages`.
- Invoke `docker build -t <registry>/<name>:<tag>`.
- If `--push` flag is set, run `docker push`.
- Update the `dag.json` `image` field with the resulting reference.

### 10. End-to-end smoke test

A test script `scripts/smoke.sh`:

1. Builds the agent and base images locally.
2. Starts `docker compose up` (Postgres + Redis + Leoflow server).
3. Pushes a fixture DAG (`testdata/dags/hello-world/`).
4. Triggers a run via the API.
5. Polls until the run reaches `success` or times out at 60s.
6. Verifies logs in the local log directory.

## Acceptance Criteria

- A fixture DAG with one Python task that prints "hello world" runs to success in under 30 seconds (Docker mode).
- The same DAG runs in K8s mode (k3d cluster) under 30 seconds.
- Killing the agent mid-execution causes the task to fail with a clear error.
- A task that exceeds its `execution_timeout_seconds` is terminated and marked failed.
- The HTTP API operator works without creating a pod.
- Pod cleanup happens 1 hour after terminal state.
- `leoflow-agent` binary is under 20 MB.

## Hints

- For client-go informers, use `cache.NewSharedIndexInformer`. Don't poll the API in a loop.
- Always set `OwnerReferences` on created pods so K8s GC can clean orphans if Leoflow misses them.
- Use a `clientcmd.NewDefaultClientConfigLoadingRules()` so the executor works both in-cluster (via service account) and out-of-cluster (via kubeconfig).
- Wrap docker API errors carefully; `ImagePullBackOff` returns specific structured info.

## Out of Scope

- XCom Redis backend (Phase 4) — stub the gRPC calls to return placeholder values.
- Log shipping to S3/GCS (Phase 4) — write to local disk only.
- Custom UI (post-MVP).
