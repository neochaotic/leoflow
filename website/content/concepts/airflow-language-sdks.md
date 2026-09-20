---
title: Airflow's Language SDKs
weight: 45
description: "Airflow 3.3 runs task bodies in Go and Java. What that changes, what it does not, and where Leoflow sits."
---

Apache Airflow 3.3.0 (2026-07-06) shipped a **Language Task SDK** for Go and
Java. If you write Go and orchestrate data, the obvious question is whether that
makes a Go control plane redundant.

The short answer is that the two projects moved different layers. Airflow moved
the **task body**. Leoflow moved the **control plane**. This page says where the
line falls, with the sources, because the summaries circulating about this
feature get one important thing backwards.

{{% alert title="Read the version boundary" color="warning" %}}
Almost everything rich about this feature (the conformance specification, the
capability matrix, several ADRs) is on Airflow's `main`, targeting **3.4**. The
Go SDK module publishes as `v1.0.0-beta3` and its own README calls the feature
**experimental**, with APIs, wire protocols and tooling that "may change between
releases without notice". Do not plan against `main` as though it shipped, and
treat everything on this page as a snapshot.
{{% /alert %}}

## What Airflow actually shipped

A task function written in Go, compiled with `airflow-go-pack` into a
self-contained **bundle**: a native executable with a metadata footer listing
the `dag_id`s it serves. At run time an `ExecutableCoordinator` matches an
incoming `dag_id` against each bundle's manifest, verifies its integrity hash,
and forks it.

The bundle format is genuinely good work: a documented, language-agnostic
[Executable Bundle Spec](https://airflow.apache.org/docs/) with a 64-byte
trailer, a SHA-256 over the binary region, and an explicit note that this gives
integrity rather than authenticity. There is a normative conformance
specification for anyone implementing a new language SDK, and a TypeScript SDK
has already appeared. This is a program, not a one-off.

## The thing that is usually reported backwards

The promise people repeat is a lean `distroless` pod running a Go binary, with
Python's startup cost gone from the task path.

**That is not what the architecture does**, and not because it is unfinished.
From the Go SDK's own README:

> The Python runtime is the worker. It proxies every `GetConnection` /
> `GetVariable` / `GetXCom` / `SetXCom` call through to the Execution API. The
> Go binary just runs the task function.

The `ExecutableCoordinator` is a Python class inside the Python supervisor, and
the supervisor **is** the worker. A Kubernetes pod running a Go task contains a
full Airflow Python installation, the supervisor process, and a forked Go child.

There was a path that would have produced a Python-free worker — a standalone Go
Edge Worker, shipped in 3.3.0 — and it was **removed on 2026-08-20**, about six
weeks after release. The accepted ADR that removes it states the consequence
directly: *"The Go SDK no longer carries a direct Execution API client."* A Go
bundle cannot talk to Airflow on its own; it speaks a local protocol to a Python
supervisor that proxies for it.

The reason given is worth reading, because it is a good one: the standalone path
lacked remote task logging, alternate XCom backends, and the complete task-state
lifecycle, all of which the Python supervisor already had. Consolidating on one
execution path is the right call for Airflow. It just means the distroless story
is not the story.

## Where the Python stays

| Component | After the Language SDK |
|---|---|
| Scheduler | Python |
| DAG processor | Python, and it still re-parses the stub every loop |
| Triggerer | Python asyncio |
| Worker / supervisor | Python, and it is the process that forks your binary |
| **Task body** | **Go, Java or TypeScript** |

The DAG itself must still be Python. From the documentation:

> A Python stub Dag is still required. The Execution API does not yet carry Dag
> structure for non-Python languages, so task names and dependencies are
> declared in Python with `@task.stub`.

Note the "yet". Airflow's conformance specification lists native DAG authoring
as a SHOULD and says *"The project intends every Language SDK to reach this
bar."* If that lands, the comparison on this page changes and we will change the
page.

## Where Leoflow sits

Leoflow replaced the layers the Language SDK does not touch. The
[five wounds](/why-leoflow/) this project exists for are scheduler stalls, the
triggerer suffocating, the DAG file re-parsing itself, workers leaking until
they die, and dependency hell. The Language SDK moves exactly one of them, and
only for Go tasks: a self-contained bundle carries its own dependencies.

The one it makes **worse** is DAG parsing, because you now maintain a Python
stub and a compiled binary, and the Python processor still parses the stub on
every loop.

### What Leoflow does not have

Leoflow's task types are `python`, `bash`, `airflow_operator` and `dbt_group`.
**There is no Go task type.** If what you want is to write a task body in Go and
have an orchestrator run it, Airflow 3.3 does that today and Leoflow does not.

We would rather say that plainly than let it be discovered. Whether Leoflow
grows a general "run this image" task type is an open question, and it is a
different question from implementing Airflow's bundle protocol.

## Why there is no benchmark here

A table of numbers would be the natural thing to put at the bottom of this page,
and it would be dishonest.

Leoflow's published measurements are control-plane measurements: what one
scheduler tick costs as history grows, how late a scheduled run is created, how
many task pods a cluster admits without queuing. The Language SDK does not touch
the scheduler, the DAG processor or the triggerer, so there is nothing on the
other side of those numbers to compare against. Putting them in a two-column
table would imply a measurement nobody made.

A comparison that would mean something — end-to-end latency for the same
workload on both, same cluster, same task weights — needs instrumentation on
both sides and has not been run. When it is, it will be published with its
method and its caveats like every other run record in this project, and not
before.

## Sources

- [Airflow 3.3.0 release post](https://airflow.apache.org/blog/airflow-3.3.0/)
- [Go SDK documentation](https://airflow.apache.org/docs/apache-airflow/stable/authoring-and-scheduling/language-sdks/go.html)
- [`apache/airflow` Go SDK module](https://pkg.go.dev/github.com/apache/airflow/go-sdk)
- The Executable Bundle Spec, the language-SDK conformance specification and the
  ADR retiring the Go Edge Worker all live in the `apache/airflow` repository.
