---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_runs_trigger.html
# --- end AUTO redirect aliases ---
title: "leoflow runs trigger"
linkTitle: "runs trigger"
weight: 47
---

Trigger a new run of a DAG.

```
leoflow runs trigger <dag_id> [flags]
```

### Options

```
      --conf string        run configuration as an inline JSON object, exposed to tasks as params (e.g. --conf '{"date":"2026-01-01"}')
      --conf-file string   path to a JSON file whose object contents become the run configuration; mutually exclusive with --conf
  -h, --help               help for trigger
      --server string      control plane base URL (default: config server_url)
      --token string       JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow runs](/reference/cli/leoflow_runs/)	 - Trigger and inspect DAG runs.

