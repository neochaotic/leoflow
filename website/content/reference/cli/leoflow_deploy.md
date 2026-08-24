---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_deploy.html
# --- end AUTO redirect aliases ---
title: "leoflow deploy"
linkTitle: "deploy"
weight: 27
---

Build, push, and register a DAG to a control plane (Pro).

```
leoflow deploy [path | dag_id] [flags]
```

### Options

```
      --all                  deploy every DAG project in the workspace
      --builder string       image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --dag-version string   DAG version label (default: git describe, else dev)
      --dockerfile string    Dockerfile path relative to the DAG directory (default "Dockerfile")
  -h, --help                 help for deploy
      --server string        control plane base URL (default: config server_url)
      --skip-build           re-use the already-built image (promote without rebuilding)
      --token string         JWT bearer token (default: config token)
      --trigger              trigger a run immediately after registering
  -y, --yes                  skip the confirmation prompt (for automation)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.

