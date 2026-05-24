# Concepts & glossary

Leoflow keeps Airflow's vocabulary so the UI and mental model are familiar.

| Term | Meaning |
|---|---|
| **DAG** | Directed Acyclic Graph of tasks. An **immutable artifact**: a `dag.json` + a container image, versioned together (ADR 0003). |
| **Task** | A unit of work in a DAG — a `task_id`, a type (`python`/`bash`/`http_api`), and config. |
| **TaskInstance** | One execution of a Task within a DagRun. Has state. |
| **DagRun** | One execution of a DAG, identified by `dag_id` + `logical_date`. |
| **Logical date** | The "business" date of a run (Airflow 3's rename of `execution_date`). |
| **Trigger rule** | When a task runs based on upstream states (`all_success`, `one_failed`, …). |
| **XCom** | Small (≤256 KB) typed value passed between tasks. Stored in Redis. |
| **Executor** | Runs a task physically: Kubernetes (pod-per-task), subprocess (dev), or inline. |
| **Agent** | Small Go binary (PID 1) inside the task container that talks gRPC to the control plane. |

## Why "DAG = image"
Airflow's pod-per-task model is right; its Python control plane is the bottleneck.
Leoflow keeps the model, rewrites the control plane in Go, and makes **each DAG its
own container image** — no shared `/dags` filesystem, no dependency hell. See
[ADR 0001](adr/0001-why-leoflow.md) and [ADR 0003](adr/0003-dag-as-image.md).

See also: [Architecture](architecture.md) · [DAG authoring](dag-authoring.md).
