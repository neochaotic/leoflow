---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_connections_get.html
# --- end AUTO redirect aliases ---
title: "leoflow connections get"
linkTitle: "connections get"
weight: 24
---

Show a connection (password omitted, extra masked).

```
leoflow connections get <connection_id> [flags]
```

### Options

```
  -h, --help            help for get
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow connections](/reference/cli/leoflow_connections/)	 - Manage control-plane connections.

