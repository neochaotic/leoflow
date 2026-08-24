---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_admin_dags.html
# --- end AUTO redirect aliases ---
title: "leoflow admin dags"
linkTitle: "admin dags"
weight: 3
---

Pause or unpause registered DAGs.

### Options

```
  -h, --help   help for dags
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow admin](/reference/cli/leoflow_admin/)	 - Operate a running control plane (health, pause, drain, runs).
* [leoflow admin dags pause](/reference/cli/leoflow_admin_dags_pause/)	 - Pause a DAG (PATCH is_paused), or every DAG with --all.
* [leoflow admin dags unpause](/reference/cli/leoflow_admin_dags_unpause/)	 - Unpause a DAG (PATCH is_paused), or every DAG with --all.

