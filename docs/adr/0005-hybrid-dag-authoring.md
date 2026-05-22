# ADR 0005: Hybrid DAG Authoring with Compile-Time Parsing

**Status:** Accepted
**Date:** 2026-05-21

## Context

DAGs can be authored two main ways:

1. **As Python code.** Familiar to Airflow users, expressive, supports decorators and dynamic constructs.
2. **As declarative configuration (YAML/JSON).** Simpler to validate, language-agnostic, GitOps-friendly.

Airflow chose Python and was punished by it: every scheduler re-parses every DAG file from disk, constantly. This is one of Airflow's worst performance bottlenecks.

## Decision

Leoflow supports **both** authoring formats, but with one canonical internal representation: `dag.json`.

The flow:

```
Python source                 YAML source
     │                             │
     ▼                             ▼
  Python parser                YAML loader
  (sidecar or CLI)             (built into CLI)
     │                             │
     └──────────► dag.json ◄───────┘
                     │
                     ▼
            Leoflow Control Plane (Go)
            never reads .py or .yaml
```

The Go Control Plane only ever consumes `dag.json`. It has no Python interpreter, no YAML parser for user content. This is non-negotiable for performance.

## Two Compilation Modes

### Mode B — CI/CD compilation (the recommended path)

Developer writes `dag.py` (or `dag.yaml`). CI runs `leoflow compile`. Output is `dag.json` plus the Docker image. CI calls `leoflow push` to register the artifact in the Control Plane.

This is the **default and recommended** workflow. It treats DAGs as versioned immutable artifacts.

### Mode A — Watcher sidecar (the legacy-friendly path)

A Python sidecar watches a directory. When `dag.py` changes, it re-parses and writes `dag.json` directly into the Control Plane's database. Familiar UX for Airflow users; no CI required.

This mode is available but **not recommended for production**. It reintroduces the "parsing in a loop" problem Leoflow exists to solve, although bounded to a single sidecar instead of the whole scheduler.

## Why Both

- Power users and enterprise teams prefer Mode B. GitOps, immutable artifacts, rollback via registry.
- Solo developers and teams migrating from Airflow prefer Mode A. Edit file, save, see result.
- The Go core is identical for both. The choice is purely a frontend concern.

## What `dag.json` Looks Like

A trimmed example:

```json
{
  "schema_version": "1.0",
  "dag_id": "etl_vendas",
  "dag_version": "v1.2.3",
  "image": "myrepo/etl-vendas:v1.2.3",
  "schedule": "0 5 * * *",
  "default_args": {
    "retries": 3,
    "retry_delay_seconds": 300
  },
  "tasks": [
    {
      "task_id": "extract",
      "type": "python",
      "entrypoint": "tasks.extract:run",
      "trigger_rule": "all_success",
      "resources": {
        "requests": { "cpu": "500m", "memory": "1Gi" },
        "limits":   { "cpu": "2",    "memory": "4Gi" }
      }
    },
    {
      "task_id": "transform",
      "type": "python",
      "entrypoint": "tasks.transform:run",
      "depends_on": ["extract"],
      "xcom_input": { "raw": "extract" }
    }
  ]
}
```

The full schema lives in `docs/api/dag-schema.json`.

## Dynamic DAGs

The MVP **does not** support dynamic task mapping (tasks generated at runtime based on upstream data). The DAG topology declared at compile time is the topology that will run.

This is a conscious limitation. Dynamic mapping is a v2 feature. Documented explicitly in the README so users are not surprised.

## Consequences

- A Python sidecar must exist and be maintained. It imports the user's `dag.py` using the official Airflow SDK to extract the graph, then serializes it.
- The `leoflow.yaml` schema and `dag.json` schema are both stable contracts. Breaking changes require a `schema_version` bump.
- YAML authoring is supported but is **not** a 1:1 translation of Python features. Some constructs (decorators, callable references) only work via the Python path.
