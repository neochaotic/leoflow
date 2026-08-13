# Editions

!!! info "Both editions are supported"
    **Lite** is the local single-host edition; **Pro** is the Helm/Kubernetes
    deployment. Both are supported today, and the README says the same.

    This page previously warned that Leoflow was pre-alpha and that Pro was
    "not cleared for official use until the v0.1.0-alpha cut". That milestone
    does not exist — ADR 0037 removed alpha and beta from the version scheme
    entirely, and `v0.1.0` stable shipped on 2026-06-28. The warning outlived
    the plan it referenced and contradicted the README for weeks.

    Pro is pre-1.0, which under SemVer means breaking changes ship between
    minor versions with a migration note (ADR 0037). Read the release notes
    before upgrading; that is the caveat, not an unsupported edition.

> See also [Operating modes](operating-modes.md) for the Lite/Pro/Demo
> runtime view (this page is the per-edition distribution + posture view).

Leoflow is planned in two editions that share the same engine, the same
Airflow-3.2.x UI, and the same DAG format (`dag.py` + `leoflow.yaml`). You author a
DAG once and it runs on either.

| | **Lite** | **Pro** |
|---|---|---|
| Status | **Supported** — single host | **In validation** — Helm on Kubernetes (tested against GKE; pin a tag) |
| Install | one command (`curl … \| sh`) on one machine | [Helm chart](https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/README.md) on your cluster |
| Command | `leoflow lite` | the deployed control plane |
| Auth | a single local **admin** login (password shown once at setup) | enterprise: SSO/OIDC, full RBAC, multi-tenant |
| Executors | a local **k3d** mini-cluster (real pods, **requires Docker** to host the cluster) or **subprocess** (dev-only, unsandboxed, no Docker) | **Kubernetes only**, at scale |
| Deploy | edit + hot-reload | GitOps: `leoflow compile` in CI → immutable image + `dag.json` |
| Intended use | local, small, or **light production** projects on a **trusted/internal network** | teams and production workloads at scale |
| Datastores | **Postgres, auto-selected**: the Docker `postgres:16` when Docker is present, else an **embedded managed** Postgres (downloaded under `~/.leoflow`, no Docker); **no Redis** | **external** managed Postgres + Redis (see [chart docs](https://github.com/neochaotic/leoflow/blob/main/helm/leoflow/README.md#datastore-compatibility) for versions) |

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
    internet. For team/production use, that is what the Pro edition is for.

Lite also includes a small built-in **[web editor](lite-web-editor.md)** (Monaco,
with Python/YAML highlighting) so you can edit DAG projects from the browser — a
Lite-only convenience; Pro teams use their own editor and the GitOps flow.

### Datastore (auto-selected), no Redis

Lite's datastore is **Postgres, chosen automatically for the host**: when Docker is
present it uses the `postgres:16` container; when it is not, it falls back to an
**embedded managed Postgres** (a pinned, checksum-verified relocatable build
downloaded under `~/.leoflow`, on a local Unix socket) — so `leoflow lite` runs on
a **Docker-free host with nothing to install**, the same way `leoflow setup` already
provisions a managed Python. (Force either with `--postgres docker|managed`; on a
minimal host such as Alpine/musl the managed build may lack system libraries and
fails loud, pointing at Docker.)

Either way Lite needs **no Redis** — scheduler locks use Postgres advisory locks and
XCom is stored in Postgres. The Postgres-backed XCom is *durable* (it survives a
restart), which suits light production, and a single datastore is simpler to
operate; Redis is a production-scale concern (multi-node locking, high XCom
throughput). (See [ADR 0026](adr/0026-lite-datastore-no-redis.md).)

!!! info "Task dispatch is serial on Lite (by design)"
    Lite's [BufferedDispatcher](adr/0031-scheduler-architecture.md) uses
    `BufferSize=0` — task dispatch runs synchronously on the scheduler tick, one
    task at a time. A fan-out DAG with N parallel branches still **executes** in
    parallel inside the cluster (real pods, or subprocesses), but the **launch**
    of those tasks is serialized through one goroutine. This is what makes Lite
    cheap to operate on a single machine. For higher launch throughput, Pro
    uses `BufferSize>0` + `Workers>1` (a bounded pool).
    See [#203](https://github.com/neochaotic/leoflow/issues/203) for the rationale.

For task execution, the **k3d** path runs real pods and therefore **requires Docker
to host the cluster** — but Docker is only the *substrate* of k3d, never an executor:
Lite talks to the Kubernetes API, and a Docker-socket executor was rejected because
it is **equivalent to host root** (a DAG could escape to the machine). `subprocess`
runs tasks directly on the host (no Docker, no isolation) as an explicitly dev-only
escape hatch. (See [ADR 0027](adr/0027-product-editions-executors-delivery.md).)

See the [Installation](installation.md) guide and the
[`leoflow lite` workflow](dev-workflow.md).

## Leoflow Pro (chart-installable)

Pro is the enterprise control plane: enterprise authentication (SSO/OIDC),
full role-based access control, multi-tenant isolation, the Kubernetes executor
at scale, first-class observability, and the GitOps deploy flow (DAGs as immutable
images + `dag.json`, shipped from CI). The Helm chart is installable and in
validation (tested against GKE; not yet cleared for official use); this site
documents Lite today, and the production surfaces (the `/api/v2/`
Airflow-compatible API, the executor, observability) are built with that target
in mind.

## Which one? (recommendation)

**Choose by deployment, not by feature checklist:**

- **Choose Lite** when you run on **one machine** (laptop, a small VM, an
  internal box), want a **one-command, Docker-free install**, and your workload
  is local development, a small project, or **light production** on a
  trusted/internal network. Lite goes from zero-dependency (`subprocess`) to
  real pod-per-task (`k3d`) on the same binary, with a durable embedded Postgres.
- **Choose Pro** when you need **Kubernetes at scale**, a team
  (SSO/OIDC + RBAC + multi-tenant), high XCom throughput, external managed
  datastores, and the GitOps deploy flow — delivered as the **Helm chart**.

Rule of thumb: if it fits on one host and the network is trusted, Lite is enough;
when you need a cluster, multiple users, or scale, that's Pro.

Because both editions share the DAG format, the engine, and the UI, DAGs authored
on Lite will carry straight over to Pro when it ships — but there is
**nothing to migrate today** (nobody is in production on Lite). The migration path
will be documented when Pro is available.
