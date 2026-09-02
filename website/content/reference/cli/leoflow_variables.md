---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /cli/leoflow_variables.html
# --- end AUTO redirect aliases ---
title: "leoflow variables"
linkTitle: "variables"
weight: 52
---

Manage control-plane variables.

### Options

```
  -h, --help   help for variables
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow](/reference/cli/leoflow/)	 - Leoflow is a GitOps-first, container-native workflow orchestrator.
* [leoflow variables delete](/reference/cli/leoflow_variables_delete/)	 - Delete a variable.
* [leoflow variables get](/reference/cli/leoflow_variables_get/)	 - Show a variable (value masked when the key looks sensitive).
* [leoflow variables list](/reference/cli/leoflow_variables_list/)	 - List variables (encrypted values not shown).
* [leoflow variables set](/reference/cli/leoflow_variables_set/)	 - Create or replace a variable (upsert).

