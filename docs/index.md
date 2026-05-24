# Leoflow

**GitOps-first, container-native workflow orchestrator** — a Go control plane
that keeps Airflow's pod-per-task model and UI, without the Python control plane.

- **DAGs are immutable artifacts**: a `dag.json` + a container image, versioned together.
- **One image per DAG**: no shared `/dags` filesystem, no monolithic worker.
- **Airflow 3.2.x UI compatibility** at `/api/v2/*` and `/ui/*`.

## Start here
- **[Operating modes](operating-modes.md)** — Demo · Dev · Production (coming soon).
- **[DAG authoring](dag-authoring.md)** — how a data engineer writes and ships a DAG.
- **[HTTP API reference](api-reference.md)** — the live Airflow-compatible API (Scalar).
- **[Go packages](go-api.md)** — the control-plane/agent/CLI GoDocs.

## The dev loop
```bash
leoflow dev setup            # check + provision host deps (dev-only)
leoflow init dags/my_dag     # scaffold a project
leoflow dev dags/my_dag      # hot-reload at http://localhost:8088 (marked DEV)
```
The product proves itself in **Dev** first; **Production** is a near-term goal.
