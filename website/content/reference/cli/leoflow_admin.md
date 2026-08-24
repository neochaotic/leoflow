---
title: "leoflow admin"
linkTitle: "admin"
weight: 2
---

Operate a running control plane (health, pause, drain, runs).

### Synopsis

Operator commands for a running Leoflow control plane (Pro). These act over the /api/v2 API — checking health, pausing DAGs, draining the control plane before maintenance, and inspecting runs — and reuse the same --server/--token/config precedence as `leoflow deploy`.

### Options

```
  -h, --help   help for admin
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.
* [leoflow admin dags](/reference/cli/leoflow_admin_dags/)	 - Pause or unpause registered DAGs.
* [leoflow admin drain](/reference/cli/leoflow_admin_drain/)	 - Pause every DAG, then wait for active runs to finish (quiesce for maintenance).
* [leoflow admin health](/reference/cli/leoflow_admin_health/)	 - Report control-plane health; non-zero exit when unhealthy.
* [leoflow admin runs](/reference/cli/leoflow_admin_runs/)	 - Inspect DAG runs across the control plane.
* [leoflow admin users](/reference/cli/leoflow_admin_users/)	 - Inspect accounts on the running control plane.

