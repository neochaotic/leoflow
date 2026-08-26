---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_runs.html
# --- end AUTO redirect aliases ---
title: "leoflow runs"
linkTitle: "runs"
weight: 37
---

Trigger and inspect DAG runs.

### Options

```
  -h, --help   help for runs
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.
* [leoflow runs list](/reference/cli/leoflow_runs_list/)	 - List DAG runs, filtered by --state, --older-than, and/or --dag.
* [leoflow runs logs](/reference/cli/leoflow_runs_logs/)	 - Stream a task attempt's logs (the latest attempt by default).
* [leoflow runs status](/reference/cli/leoflow_runs_status/)	 - Show the state of a DAG run (the latest by default).
* [leoflow runs trigger](/reference/cli/leoflow_runs_trigger/)	 - Trigger a new run of a DAG.

