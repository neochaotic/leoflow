---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_lite_backup.html
# --- end AUTO redirect aliases ---
title: "leoflow lite backup"
linkTitle: "lite backup"
weight: 31
---

Snapshot the Lite install (workspace + datastore + config) into a portable archive.

### Synopsis

backup writes a tar.gz containing your workspace DAGs, a logical pg_dump of the managed Postgres, the config (admin hash + JWT secret), and a small MANIFEST.json. Pair with `leoflow lite restore` to migrate to another machine, survive an OS reinstall, or roll back a botched pre-alpha upgrade.

Backup only covers the managed Postgres path (the default Lite shape). For the Docker datastore path, capture the volume with `docker volume export` instead.

```
leoflow lite backup [flags]
```

### Options

```
  -h, --help            help for backup
  -o, --output string   path for the archive (default: leoflow-backup-<timestamp>.tar.gz in the cwd)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow lite](/reference/cli/leoflow_lite/)	 - Run Leoflow Lite locally with hot reload.

