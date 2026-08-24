---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_db_migrate.html
# --- end AUTO redirect aliases ---
title: "leoflow db migrate"
linkTitle: "db migrate"
weight: 25
---

Create (if needed) and migrate the Lite database to the latest schema.

```
leoflow db migrate [flags]
```

### Options

```
  -h, --help   help for migrate
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow db](/reference/cli/leoflow_db/)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).

