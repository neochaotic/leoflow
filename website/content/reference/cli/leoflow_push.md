---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_push.html
# --- end AUTO redirect aliases ---
title: "leoflow push"
linkTitle: "push"
weight: 42
---

Register a compiled dag.json with the control plane.

```
leoflow push <dag.json> [flags]
```

### Options

```
  -h, --help            help for push
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.

