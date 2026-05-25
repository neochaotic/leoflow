# Editions

!!! warning "Pre-alpha"
    Leoflow is **pre-alpha**. Only the **Lite** edition exists today, and it is for
    local iteration on a trusted network — not durable or production use. The
    Production (enterprise) edition is not released yet.

Leoflow is planned in two editions that share the same engine, the same
Airflow-3.2.x UI, and the same DAG format (`dag.py` + `leoflow.yaml`). You author a
DAG once and it runs on either.

| | **Lite** | **Production** |
|---|---|---|
| Status | **Available now** (pre-alpha) | Enterprise — *coming* |
| Install | one command (`curl … \| sh`) on one machine | Helm chart on your cluster |
| Command | `leoflow lite` | the deployed control plane |
| Auth | a single local **admin** login (password shown once at setup) | enterprise: SSO/OIDC, full RBAC, multi-tenant |
| Executors | **subprocess** (local) or a local **k3d** mini-cluster | Kubernetes at scale |
| Deploy | edit + hot-reload | GitOps: `leoflow compile` in CI → immutable image + `dag.json` |
| Intended use | local & small projects, on a **trusted/internal network** | teams and production workloads |
| Datastores | local Postgres + Redis (via Docker or pointed at your own) | your managed Postgres + Redis |

## Leoflow Lite

Lite is the whole control plane on your machine, scoped down for local use. One
command installs it, [`leoflow setup`](installation.md#what-leoflow-setup-does)
provisions a managed Python and a single admin, and `leoflow lite <project>`
serves the UI with hot-reload.

It is deliberately simple, which has security implications:

!!! warning "Run Lite on a trusted network only"
    Lite's admin password is short and human-friendly (easy to type), and it is a
    single local admin — there is no SSO/RBAC. Run Lite on **localhost, an
    internal network, or a VPN**. Do **not** expose a Lite instance to the public
    internet. For team/production use, that is what the Production edition is for.

Lite also includes a small built-in **[web editor](lite-web-editor.md)** (Monaco,
with Python/YAML highlighting) so you can edit DAG projects from the browser — a
Lite-only convenience; Production teams use their own editor and the GitOps flow.

Pre-alpha Lite builds also **expire** (~90 days) and refuse to run past it — a
reminder that Lite is for iterating locally, not for durable deployments.

See the [Installation](installation.md) guide and the
[`leoflow lite` workflow](dev-workflow.md).

## Leoflow Production (coming)

Production is the enterprise control plane: enterprise authentication (SSO/OIDC),
full role-based access control, multi-tenant isolation, the Kubernetes executor
at scale, first-class observability, and the GitOps deploy flow (DAGs as immutable
images + `dag.json`, shipped from CI). It is not yet released; this site documents
Lite today, and the production surfaces (the `/api/v2/` Airflow-compatible API,
the executor, observability) are built with that target in mind.

## Which one?

Today there is only one thing to run: **Lite**, and it's **pre-alpha** — for
local iteration on a trusted network, not durable or production use. Production
(the enterprise edition) is not released yet.

Because both editions share the DAG format, the engine, and the UI, DAGs authored
on Lite will carry straight over to Production when it ships — but there is
**nothing to migrate today** (nobody is in production on Lite). The migration path
will be documented when Production is available.
