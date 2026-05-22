# ADR 0004: Thin Static Go Agent in the Worker Container

**Status:** Accepted
**Date:** 2026-05-21

## Context

The worker container must run two things:

- The user's task code (Python or Bash).
- A small piece of Leoflow code that communicates with the Control Plane, reports state and logs, and handles XCom.

The second piece is the **agent**. It needs a design.

## Decision

The `leoflow-agent` is a **statically linked Go binary**, approximately 15 MB, that ships inside every official base image. It acts as the `ENTRYPOINT` of the container.

It does exactly this:

1. Connects to the Control Plane over **gRPC** using credentials injected as environment variables.
2. Receives the task spec (entrypoint, args, environment, XCom dependencies).
3. Fetches required XCom values from Redis via the Control Plane.
4. Spawns the user's process (`python -m`, `bash -c`, etc.) with the injected environment.
5. Streams `stdout` and `stderr` back over gRPC (and optionally to a log sink).
6. Captures the return value and pushes it as XCom if declared.
7. Reports final state and exits.

## Rationale

- **Static binary.** No runtime dependencies. Drops into any base image. Works on glibc and musl alike.
- **Tiny.** 15 MB is negligible compared to a Python image. Does not affect pull time.
- **Single-purpose.** The agent is a runner, not a framework. There is no "leoflow framework" to import inside the user's Python.
- **gRPC.** Binary protocol, bidirectional streaming, much lower overhead than the HTTP+SQLAlchemy round-trips that Airflow does.
- **Process model.** The agent is `PID 1` in the pod. It handles SIGTERM gracefully and propagates to the child process, allowing K8s to terminate cleanly.

## Anti-Decisions

The agent **does not**:

- Parse DAG files. The DAG is already parsed; the agent receives a task spec.
- Have a CLI for users. Users never invoke `leoflow-agent` directly.
- Embed a Python interpreter. The base image provides Python; the agent spawns it.
- Cache anything beyond a single task execution. State lives in the Control Plane.
- Talk to the Postgres metadata database. All state goes through gRPC.

## Consequences

- The gRPC protocol between Control Plane and Agent is a stable, versioned contract. Defined in `proto/agent.proto`.
- Authentication uses **short-lived JWTs** injected as environment variables at pod creation time. The agent presents this JWT on every gRPC call.
- The agent must handle Control Plane unavailability gracefully: retry with exponential backoff, then fail the task with a clear error after a deadline.
- The agent emits structured logs to `stdout`, which the log shipper (Fluentbit sidecar or node-level agent) forwards. The agent itself does not push to S3/GCS — that decoupling keeps it tiny.

## Comparison to Airflow

Airflow's worker pod runs the full Airflow framework, which:

- Re-parses the DAG file on every task (CPU and memory cost).
- Imports every installed provider (potentially gigabytes of code).
- Maintains an SQLAlchemy session pool to Postgres.
- Uses Celery internals even in K8s mode.

Leoflow's agent does none of this. The container is **the user's code plus a 15 MB sidecar**, and nothing more.
