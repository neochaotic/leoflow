---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow.html
# --- end AUTO redirect aliases ---
title: "leoflow"
linkTitle: "leoflow"
weight: 1
---

Leoflow is a GitOps-first, container-native workflow orchestrator.

### Options

```
      --config string       config file path (default ~/.leoflow/config.yaml)
  -h, --help                help for leoflow
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow admin](/reference/cli/leoflow_admin/)	 - Operate a running control plane (health, pause, drain, runs).
* [leoflow auth](/reference/cli/leoflow_auth/)	 - Manage authentication tokens.
* [leoflow compile](/reference/cli/leoflow_compile/)	 - Compile a DAG project into dag.json via the Python parser.
* [leoflow completion](/reference/cli/leoflow_completion/)	 - Generate the autocompletion script for the specified shell
* [leoflow dags](/reference/cli/leoflow_dags/)	 - Manage registered DAGs.
* [leoflow db](/reference/cli/leoflow_db/)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).
* [leoflow deploy](/reference/cli/leoflow_deploy/)	 - Build, push, and register a DAG to a control plane (Pro).
* [leoflow doctor](/reference/cli/leoflow_doctor/)	 - Report host platform, dependencies, and the achievable operating tier.
* [leoflow init](/reference/cli/leoflow_init/)	 - Scaffold a new DAG project (leoflow.yaml + dag.py).
* [leoflow lite](/reference/cli/leoflow_lite/)	 - Run Leoflow Lite locally with hot reload.
* [leoflow push](/reference/cli/leoflow_push/)	 - Register a compiled dag.json with the control plane.
* [leoflow runs](/reference/cli/leoflow_runs/)	 - Trigger and inspect DAG runs.
* [leoflow server](/reference/cli/leoflow_server/)	 - Information about running the control plane.
* [leoflow setup](/reference/cli/leoflow_setup/)	 - Bootstrap the managed Leoflow runtime (Python, parser, workspace).
* [leoflow uninstall](/reference/cli/leoflow_uninstall/)	 - Remove the Leoflow installation (~/.leoflow).
* [leoflow validate](/reference/cli/leoflow_validate/)	 - Validate leoflow.yaml and the DAG source against the schema.
* [leoflow version](/reference/cli/leoflow_version/)	 - Print the version, git commit, and build date.

