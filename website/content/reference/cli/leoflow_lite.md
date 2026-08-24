---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_lite.html
# --- end AUTO redirect aliases ---
title: "leoflow lite"
linkTitle: "lite"
weight: 30
---

Run Leoflow Lite locally with hot reload.

### Synopsis

lite is the Leoflow Lite edition: it brings up local dependencies and runs the control plane against an isolated local database, registers the DAG, and hot-reloads on every save. The UI is served on a Lite port (default 8088, --port), marked with a LITE badge, and behind a login (the admin created by `leoflow setup`).

Executor (--executor): 'subprocess' runs tasks unsandboxed on the host with no image build — the fast inner loop, best for local use. 'k8s' runs real pod-per-task on a dedicated, isolated k3d mini-cluster (leoflow-dev) — highest fidelity, best for development; it rebuilds the DAG image on each change.

('leoflow dev' remains as a deprecated alias.)

```
leoflow lite [path] [flags]
```

### Options

```
      --agent-bin string     leoflow-agent binary (default: PATH, then ./bin)
      --compose string       compose file for the local Postgres (default: a managed one under ~/.leoflow, materialized on first run)
      --executor string      execution mode: 'auto' (default; k3d if Docker is present, else subprocess), 'k8s' (dedicated k3d cluster, real pods), or 'subprocess' (host, fast, unsandboxed) (default "auto")
  -h, --help                 help for lite
      --host string          address to bind the UI/API to; use 0.0.0.0 to reach it from your internal network/VPN (insecure — see the warning) (default "127.0.0.1")
      --image string         placeholder image recorded in dag.json (subprocess mode only) (default "leoflow-dev:local")
      --no-up                skip docker compose (Postgres already running); the dev DB + venv are still provisioned
      --port int             HTTP/UI port (dev default 8088, distinct from the demo's 8080) (default 8088)
      --postgres string      Postgres backend: 'auto' (default; the Docker postgres:16 when Docker is present, else a managed relocatable PG under ~/.leoflow on a Unix socket, no Docker), 'docker', or 'managed' (best on full distros; minimal hosts may lack its system libs) (default "auto")
      --runtime-src string   source of the leoflow_runtime package installed into the dev venv (default "runtime/python")
      --server-bin string    leoflow-server binary (default: PATH, then ./bin)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.
* [leoflow lite backup](/reference/cli/leoflow_lite_backup/)	 - Snapshot the Lite install (workspace + datastore + config) into a portable archive.
* [leoflow lite forget](/reference/cli/leoflow_lite_forget/)	 - Remove a DAG (and all its history) from the Lite registry without touching the source files.
* [leoflow lite provision](/reference/cli/leoflow_lite_provision/)	 - Check and provision the local deps the from-source `leoflow lite` loop needs.
* [leoflow lite reset-password](/reference/cli/leoflow_lite_reset-password/)	 - Reset the Leoflow Lite admin password.
* [leoflow lite restore](/reference/cli/leoflow_lite_restore/)	 - Restore a Lite install from an archive produced by `leoflow lite backup`.

