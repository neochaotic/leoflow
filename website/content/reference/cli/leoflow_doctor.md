---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_doctor.html
# --- end AUTO redirect aliases ---
title: "leoflow doctor"
linkTitle: "doctor"
weight: 28
---

Report host platform, dependencies, and the achievable operating tier.

### Synopsis

doctor inspects the host (OS, architecture, libc), checks for Python 3.11, Docker, k3d, and kubectl, and reports which operating tier is achievable. It changes nothing; run `leoflow setup` to bootstrap.

```
leoflow doctor [flags]
```

### Options

```
  -h, --help   help for doctor
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.

