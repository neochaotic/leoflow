# End-to-end smoke test

`e2e.sh` exercises the full Leoflow pod-path on a local Kubernetes cluster: it
builds the base and DAG images, imports them into a k3d cluster, runs the
control plane on the host, pushes and triggers a DAG, and asserts every task
instance reaches `success` by running in a real pod whose agent reports state
over gRPC.

This is a developer/CI tool — it is **not** part of `go test`. Production e2e
runs against a real cluster via the Helm chart (later phase).

## Prerequisites

- `k3d`, `kubectl`, `docker`, `jq`, `curl`
- Built binaries: `make build`
- A running dev database: `make dev-up`

## Run

```bash
make dev-up      # Postgres + Redis on the host
make build       # bin/leoflow, bin/leoflow-server, bin/leoflow-agent
make e2e         # or: bash test/e2e/e2e.sh [cluster-name]
```

## How the networking fits together

The control plane runs on the host and listens for agents on `:9091`. Task pods
run inside k3d and dial back via `host.k3d.internal:9091`, which the script sets
through `LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` — the host listen address
(`0.0.0.0:9091`) is not reachable from inside a pod. The DAG image is imported
into the cluster with `k3d image import` so no registry is needed.
