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
| Executors | a local **k3d** mini-cluster (real pods) or **subprocess** (dev-only, unsandboxed) | **Kubernetes only**, at scale |
| Deploy | edit + hot-reload | GitOps: `leoflow compile` in CI → immutable image + `dag.json` |
| Intended use | local, small, or **light production** projects on a **trusted/internal network** | teams and production workloads at scale |
| Datastores | **embedded** managed Postgres; **no Redis, no Docker** | **external** managed Postgres + Redis |

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

### No Docker, no Redis

Lite runs its whole control plane on an **embedded managed Postgres** (a pinned,
checksum-verified relocatable build under `~/.leoflow`, on a local Unix socket).
It needs **no Redis** — scheduler locks use Postgres advisory locks and XCom is
stored in Postgres — and **no Docker** for the datastore. The Postgres-backed
XCom is *durable* (it survives a restart), which suits light production; very
high XCom throughput is where you would move to the Production edition. (See
[ADR 0026](adr/0026-lite-datastore-no-redis.md).)

For task isolation, Lite's container path is a local **k3d** mini-cluster, not
the Docker socket: a Docker-socket executor is **equivalent to host root** (a DAG
could escape to the machine), so it was rejected on security grounds. `subprocess`
exists only as an explicitly unsandboxed, dev-only escape hatch. (See
[ADR 0027](adr/0027-product-editions-executors-delivery.md).)

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

## Which one? (recommendation)

**Today:** there is only one thing to run — **Lite**, and it's **pre-alpha**, for
local iteration on a trusted network. Production (the enterprise edition) is not
released yet.

**When both ship, choose by deployment, not by feature checklist:**

- **Choose Lite** when you run on **one machine** (laptop, a small VM, an
  internal box), want a **one-command, Docker-free install**, and your workload
  is local development, a small project, or **light production** on a
  trusted/internal network. Lite goes from zero-dependency (`subprocess`) to
  real pod-per-task (`k3d`) on the same binary, with a durable embedded Postgres.
- **Choose Production** when you need **Kubernetes at scale**, a team
  (SSO/OIDC + RBAC + multi-tenant), high XCom throughput, external managed
  datastores, and the GitOps deploy flow — delivered as the **Helm chart**.

Rule of thumb: if it fits on one host and the network is trusted, Lite is enough;
when you need a cluster, multiple users, or scale, that's Production.

Because both editions share the DAG format, the engine, and the UI, DAGs authored
on Lite will carry straight over to Production when it ships — but there is
**nothing to migrate today** (nobody is in production on Lite). The migration path
will be documented when Production is available.
