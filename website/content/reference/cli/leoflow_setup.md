---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_setup.html
# --- end AUTO redirect aliases ---
title: "leoflow setup"
linkTitle: "setup"
weight: 49
---

Bootstrap the managed Leoflow runtime (Python, parser, workspace).

### Synopsis

setup prepares ~/.leoflow: it ensures a Python 3.11 is available (using a system interpreter if present, otherwise downloading a pinned, checksum-verified relocatable CPython — no sudo, no system packages), extracts the embedded parser and runtime sources, points the parser at the interpreter (pure Python, no venv, no Airflow — ADR 0024), and creates a workspace directory. Re-running is safe; --dry-run shows the plan.

```
leoflow setup [flags]
```

### Options

```
      --dry-run            detect and print the plan without downloading or writing anything
  -h, --help               help for setup
      --workspace string   workspace dir for your DAG projects (default ~/leoflow)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.

