# ADR 0024: DAG Parsing via a Structural Shim (No Airflow SDK Dependency)

**Status:** Accepted
**Date:** 2026-05-25
**Deciders:** Project founder
**Refines:** ADR 0005 (Hybrid DAG Authoring)

## Context

ADR 0005 established the Python sidecar that turns `dag.py` into `dag.json`, and
stated in its consequences that the sidecar "imports the user's `dag.py` using
the **official Airflow SDK** to extract the graph, then serializes it."

Measuring that path showed its real cost. Installing the parser with Apache
Airflow (`pip install ./parser apache-airflow-task-sdk`, Python 3.12):

- **262 MB**, **136 packages** — dominated by `apache-airflow-core`'s transitive
  tree (grpc 38 M, babel 33 M, cryptography 24 M, sqlalchemy 18 M, libcst,
  pydantic, opentelemetry, aiohttp, …).

None of that is used to *parse*: the compiler only constructs DAG/operator
objects and reads attributes (`dag_id`, `tags`, `task_dict`, and per-task
`task_id / upstream_task_ids / trigger_rule / python_callable / op_args /
op_kwargs / bash_command / endpoint / method`). It runs no scheduler, no
database, no web/API server. The weight cannot be trimmed while importing real
Airflow: `apache-airflow-task-sdk → apache-airflow-core`, and
`apache-airflow-providers-standard/http → apache-airflow (meta) → core`.

Leoflow also supports only three task types (ADR 0005 / the compiler): Python
(including TaskFlow `@task`), Bash, and HTTP. Anything else is already rejected
at compile time — but only *after* the heavy install and an import of the full
stack.

A proof of concept (issue #83, `experiments/parser-shim/`) showed that a tiny,
dependency-free **structural shim** of `airflow` — providing just the classes a
supported DAG imports and recording structure as the file is exec'd — reproduces
the compiler's output. Golden tests against the real Airflow-based compiler pass
for all shipped examples (they also caught two real fidelity gaps — duplicate
`task_id` auto-suffixing and list fan-in — which the shim now handles).

## Decision

**The parser extracts DAG structure by executing `dag.py` against a bundled,
standard-library-only structural shim of `airflow`, scoped to the supported
operators — not the official Airflow SDK.**

- The shim provides `airflow.sdk.DAG` / `@task`, and the `BashOperator`,
  `HttpOperator`, `PythonOperator`, `EmptyOperator` classes, mirroring exactly the
  attribute surface the compiler reads. It runs no task bodies (TaskFlow `@task`
  calls only build structure, as with Airflow's lazy operators).
- **Unsupported operators are absent from the shim**, so importing one
  (`from airflow.providers.amazon… import …`) raises `ModuleNotFoundError`, which
  the loader turns into a clear "Leoflow does not support …" error. Restricting to
  the supported set is intentional and surfaced early.
- The decision applies to the **parser only**. The real Airflow **Task SDK stays
  in the task runtime** (the image/venv that executes user code), where user task
  bodies may legitimately use `airflow.sdk` helpers. The parser and the runtime
  have different needs; only the parser drops Airflow.
- Fidelity is guarded by **golden tests**: the shim's structural output is
  asserted equal to the real Airflow-based compiler's output for every example,
  so drift is caught in CI without installing Airflow.

This refines ADR 0005: the sidecar still imports `dag.py` to extract the graph,
but against the shim rather than the official SDK.

## Rationale

- **Footprint & install.** 262 MB / 136 third-party packages → ~44 KB / **zero**
  third-party dependencies. The parser becomes pure Python and embeddable in the
  binary (like the runtime), which removes the heavy `leoflow setup` parser venv
  and the only reason `pip`/Airflow is needed to parse.
- **Supply chain (ADR 0014).** Eliminating 136 transitive packages collapses the
  parser's vulnerability surface and dependency-maintenance burden.
- **No Airflow version coupling.** The parser is no longer pinned to a specific
  `apache-airflow` release; it tracks only the small `airflow.sdk` authoring
  surface.
- **Clearer UX.** Unsupported operators fail fast with a precise message instead
  of installing the full stack and erroring late.

## Consequences

- **The parser no longer depends on `apache-airflow`.** An opt-in fallback to the
  real `DagBag` may be retained behind an environment seam
  (`LEOFLOW_PARSER_BACKEND=airflow`) for diffing/escape, but the default and
  supported path is the shim, and `apache-airflow` moves to an optional extra.
- **Fidelity must be maintained deliberately.** The golden corpus is the contract;
  it is regenerated from the real compiler when the supported surface changes, and
  the shim must track the `airflow.sdk` API (a small, slow-moving surface).
- **Advanced or unsupported Airflow features are rejected**, not silently
  mis-parsed — consistent with Leoflow supporting a deliberate operator subset.
- DAGs that call real Airflow at module import time (e.g. provider hooks at
  top-level) will not parse — but those are already unsupported.
- Enables a later step (issue #83, Phase 3): **embed the pure-Python parser in the
  binary and drop the parser venv**, making installation light.

## Alternatives Rejected

- **Keep the official Airflow SDK (ADR 0005 as written):** rejected for the
  262 MB / 136-dependency footprint and supply-chain surface, none of which the
  parser uses.
- **Depend on `apache-airflow-core` + only the needed providers:** does not help —
  `task-sdk` pulls core, and `providers-standard/http` require the `apache-airflow`
  meta-package, which pulls core anyway.
- **A Go-native AST parser (no Python):** would abandon import-based fidelity for
  arbitrary user Python and require re-implementing Airflow DAG/TaskFlow semantics;
  far larger effort and lower fidelity. The shim keeps real Python execution of the
  DAG while shedding only the dependency weight.
