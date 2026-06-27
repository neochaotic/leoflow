# End-to-end tests

This directory holds the E2E suite — scripts that boot real Leoflow components
and exercise the user's path, asserting behavior end-to-end. They are
developer/CI tools — **not** part of `go test`. They are the gates that catch
the kind of regression unit tests miss (the parser→spec→executor wiring that
shipped broken in `v0.0.1-prealpha.21`, for instance). See
[ADR 0033](../../docs/adr/0033-release-flow-rc-tags-and-e2e-gates.md) for the
gate-and-release policy these tests implement.

## Scripts

| Script | What it gates | Runtime | Where it runs |
|---|---|---|---|
| `lite-login.sh` | Lite happy path: `leoflow setup` → control plane → admin login → JWT → web editor | ~15 s | `ci.yaml` job `e2e-lite` on every PR + push |
| `lite-multidag.sh` | The multi-DAG materialization contract: subdir DAG → `dag.json.source` carries `dag.py` verbatim (the property the subprocess executor depends on to materialize per-TI work dirs) | ~5 s | `ci.yaml` job `e2e-lite-multidag` on every PR + push |
| `e2e.sh` | Pod-path E2E on k3d: build images, k3d import, agent-over-gRPC, real pod-per-task. Regression guards for the ADR 0040 features in the real pod path: generic operator/sensor execution, `ti.xcom_pull` chaining, `@task` run-context (`ds`), Admin Variable delivery, **multi-key XCom** (`LEOFLOW_PUSHES_PATH`), **native bash Jinja templating** (`{{ ds }}`, #382), and **cloud connection delivery** — a user-pasted credential (GCP `keyfile_dict`, AWS access keys, Azure client secret) created via the API survives encrypted-at-rest storage (ADR 0019) and is recovered intact inside the pod task, and **reschedule-mode sensors** (#380) — a `DateTimeSensor(mode='reschedule')` releases its pod on each not-ready poke (passes through `up_for_reschedule`) and is re-dispatched to success | minutes | `ci.yaml` job `e2e-operators` (k3d) on every PR + push |
| `deploy-e2e.sh` | The real `leoflow deploy` path (ADR 0041): `auth login` → one `leoflow deploy` that builds for the cluster arch, **pushes to a registry the cluster pulls from**, captures the digest, re-pins `dag.json`, registers — then the cluster pulls the digest-pinned image and runs it | minutes | Manual / separate workflow |
| `dbt-e2e.sh` | dbt support (ADR 0042): `leoflow compile` renders a dbt project's `manifest.json` into one task per dbt node, then the scheduler dispatches a **pod per node** whose agent runs `dbt seed/run --select <node>` against a shared Postgres warehouse, in dependency order, until every task succeeds and the mart materializes | minutes | Manual (needs `dbt` on PATH) |
| `dbt-connection-e2e.sh` | dbt **managed connections** (ADR 0043): a dbt task generates `profiles.yml` in the pod from a Leoflow managed connection (not a baked one). The image bakes a **deliberately broken** `profiles.yml`; if tasks still succeed and the mart materializes, the runtime used the managed connection — proving no credential is baked | minutes | Manual (needs `dbt` on PATH) |
| `dbt-mixing-e2e.sh` | dbt **mixed with operators** (ADR 0043): a `dag.py` wires BashOperators around a `dbt_group()`; the compiler merges them into one `dag.json` and the scheduler runs operators **and** per-model dbt pods in order (operator → group roots, group leaves → operator), all succeeding with the mart materialized | minutes | Manual (needs `dbt`, `python3`) |

The `lite-*` scripts gate Lite behavior; `e2e.sh` gates the pod-path that
Lite cluster mode and Pro both rely on; `deploy-e2e.sh` gates the pipeline-less
`leoflow deploy` promotion (the build→push→digest→register glue that unit tests
leave to e2e).

## Running locally

### Lite tests (Postgres + Redis required for `lite-login.sh`)

```bash
make dev-up                          # Postgres + Redis on the host
bash test/e2e/lite-login.sh          # ~15 s
bash test/e2e/lite-multidag.sh       # ~5 s — no DB needed
```

`lite-login.sh` is destructive — it resets `leoflow_dev`.
`lite-multidag.sh` runs entirely in a tmpdir and needs nothing but Go +
Python.

### Pod-path test (k3d + Docker)

```bash
make dev-up      # Postgres + Redis on the host
make build       # bin/leoflow, bin/leoflow-server, bin/leoflow-agent
make e2e         # or: bash test/e2e/e2e.sh [cluster-name]
```

The pod-path test builds the base + DAG images, imports them into k3d, runs
the control plane on the host, pushes and triggers a DAG, and asserts every
task instance reaches `success` — i.e. each task ran in a real pod whose
agent reported state over gRPC.

## Network layout for `e2e.sh`

The control plane runs on the host and listens for agents on `:9091`. Task
pods run inside k3d and dial back via `host.k3d.internal:9091`, which the
script sets through `LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` — the host
listen address (`0.0.0.0:9091`) is not reachable from inside a pod. The DAG
image is imported into the cluster with `k3d image import` so no registry
is needed.

### Deploy test (`deploy-e2e.sh`) — k3d + a registry the cluster pulls from

```bash
make dev-up      # Postgres + Redis on the host
make build       # bin/leoflow, bin/leoflow-server
bash test/e2e/deploy-e2e.sh
```

Unlike `e2e.sh` (which `k3d image import`s the image), this uses a **k3d-managed
registry** so it exercises the real push→pull path: a single `leoflow deploy`
builds for the cluster arch (auto-detected — `linux/arm64` on an arm64 Mac,
`linux/amd64` on amd64 CI), pushes to `k3d-registry.localhost:5111`, captures the
image digest, re-pins `dag.json`, and registers; the cluster then pulls the
digest-pinned image and runs it. It binds the API on `:18080` by default
(`LEOFLOW_E2E_HTTP_PORT`) to avoid clashing with a Lima/other forward on 8080.

!!! note "Docker Desktop on macOS"
    The host `docker push` to the k3d registry can be intermittently routed
    through Docker Desktop's HTTP proxy. If a push times out
    (`proxyconnect … i/o timeout`), add `k3d-registry.localhost:5111` to Docker
    Desktop → Settings → Resources → Proxies bypass, or run this gate on Linux/CI
    (no Docker Desktop proxy), where it is clean.

## Adding a new gate

Per ADR 0033, every gate added here should be:

- **Reality-anchored** — no mocks at the boundary it gates.
- **Fast enough for PR-time CI** — target < 60 s for `lite-*`; longer is OK
  for k3d-heavy paths if they run on a separate workflow.
- **Named after the bug it catches** — e.g. `lite-multidag.sh` exists because
  shipping `v0.0.1-prealpha.21` taught us subdir DAGs need their own gate.
  Name reveals intent.

Wire the new script into a CI job in `.github/workflows/ci.yaml` following
the `e2e-lite-multidag` pattern (no DB dep) or the `e2e-lite` pattern
(Postgres + Redis services).
