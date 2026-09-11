---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_build.html
# --- end AUTO redirect aliases ---
title: "leoflow build"
linkTitle: "build"
weight: 14
---

## leoflow build

Build the container image of every DAG project in a workspace.

### Synopsis

Compiles and builds each project found under the workspace, using the image reference that project's own `registry:` block derives.

A project that declares no registry cannot be built and is reported by name rather than skipped quietly — a build that covers less than the workspace, silently, leaves you believing images exist.

```
leoflow build [workspace] [flags]
```

### Options

```
      --builder string       image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --dag-version string   version recorded in each dag.json and used by the registry tag strategy
  -h, --help                 help for build
      --push                 push each built image to its registry
      --sha sha              commit sha for the sha tag strategy
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](leoflow.md)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.

