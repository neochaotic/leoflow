---
title: "leoflow runs trigger"
linkTitle: "runs trigger"
weight: 39
---

Trigger a new run of a DAG.

```
leoflow runs trigger <dag_id> [flags]
```

### Options

```
  -h, --help            help for trigger
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

* [leoflow runs](/reference/cli/leoflow_runs/)	 - Trigger and inspect DAG runs.

