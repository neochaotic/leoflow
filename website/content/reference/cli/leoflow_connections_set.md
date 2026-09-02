---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_connections_set.html
# --- end AUTO redirect aliases ---
title: "leoflow connections set"
linkTitle: "connections set"
weight: 26
---

Create or update a connection (upsert).

### Synopsis

Creates a connection, or updates an existing one with the same id. --conn-type is required.

Only the fields you pass are changed; any field you omit keeps its current value. So you can change just --host without re-supplying the password (which cannot be read back anyway). To clear a field, delete and recreate the connection.

The password and extra are sent to the control plane but never printed back; read commands show masked values. Prefer --password-stdin / --extra-file over --password / --extra so a secret never lands in your shell history or the process table.

```
leoflow connections set <connection_id> [flags]
```

### Options

```
      --conn-type string     connection type, e.g. postgres, http, aws (required)
      --description string   human-readable description
      --extra string         extra JSON blob (provider-specific; secrets here are masked on read)
      --extra-file string    read the extra JSON from a file instead of --extra (keeps provider secrets out of argv)
  -h, --help                 help for set
      --host string          connection host
      --login string         connection login/username
      --password string      connection password (prefer --password-stdin)
      --password-stdin       read the password from stdin instead of --password (avoids ps/shell-history exposure)
      --port int             connection port (omit to keep the stored value; an explicit value, including 0, overwrites it)
      --schema string        connection schema/database
      --server string        control plane base URL (default: config server_url)
      --token string         JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow connections](/reference/cli/leoflow_connections/)	 - Manage control-plane connections.

