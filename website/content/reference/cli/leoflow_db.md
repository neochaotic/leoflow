---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_db.html
# --- end AUTO redirect aliases ---
title: "leoflow db"
linkTitle: "db"
weight: 24
---

Manage the local Lite database (schema name leoflow_dev for upgrade safety).

### Options

```
  -h, --help   help for db
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.
* [leoflow db migrate](/reference/cli/leoflow_db_migrate/)	 - Create (if needed) and migrate the Lite database to the latest schema.
* [leoflow db reset](/reference/cli/leoflow_db_reset/)	 - Drop, recreate, and migrate the Lite database (DESTRUCTIVE).

