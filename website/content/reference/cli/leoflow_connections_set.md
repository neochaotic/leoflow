---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_connections_set.html
# --- end AUTO redirect aliases ---
title: "leoflow connections set"
linkTitle: "connections set"
weight: 26
---

Create or replace a connection (upsert).

### Synopsis

Upserts a connection: creates it, or replaces an existing one with the same id. --conn-type is required. The password and extra are sent to the control plane but never printed back; read commands show masked values.

Prefer --password-stdin over --password so the secret never lands in your shell history or the process table.

```
leoflow connections set <connection_id> [flags]
```

### Options

```
      --conn-type string     connection type, e.g. postgres, http, aws (required)
      --description string   human-readable description
      --extra string         extra JSON blob (provider-specific; secrets here are masked on read)
  -h, --help                 help for set
      --host string          connection host
      --login string         connection login/username
      --password string      connection password (prefer --password-stdin)
      --password-stdin       read the password from stdin instead of --password (avoids ps/shell-history exposure)
      --port int             connection port (0 leaves it unset)
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

