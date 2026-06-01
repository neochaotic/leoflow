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
| `e2e.sh` | Pod-path E2E on k3d: build images, k3d import, agent-over-gRPC, real pod-per-task | minutes | Manual / separate workflow |

The `lite-*` scripts gate Lite behavior; `e2e.sh` gates the pod-path that
Lite cluster mode and Pro both rely on.

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
