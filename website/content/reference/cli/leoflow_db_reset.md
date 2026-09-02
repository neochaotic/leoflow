---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_db_reset.html
# --- end AUTO redirect aliases ---
title: "leoflow db reset"
linkTitle: "db reset"
weight: 32
---

Drop, recreate, and migrate the Lite database (DESTRUCTIVE).

```
leoflow db reset [flags]
```

### Options

```
  -h, --help   help for reset
      --yes    confirm the destructive reset
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow db](/reference/cli/leoflow_db/)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).

