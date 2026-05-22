# leoflow-runtime

The Python helper baked into every Leoflow task base image. It runs the user's
task callable inside the container and bridges data flow with the control plane:

- `python -m leoflow_runtime <module:callable>` imports and calls the task,
  writing a non-`None` return value to `/tmp/leoflow_return_value.json`. The
  `leoflow-agent` reads that file and pushes it as the task's `return_value`
  XCom.
- `leoflow_runtime.xcom_pull("name")` returns an upstream XCom the agent injected
  as `LEOFLOW_XCOM_<NAME>`.

This package contains no third-party dependencies so the base images stay small.
