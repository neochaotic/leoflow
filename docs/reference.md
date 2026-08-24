# Reference

References for every Leoflow surface. The HTTP API, CLI, Go, and Python
references below are generated from source on every push, so they never drift
from the code. The [Configuration](configuration.md) page is hand-maintained
against `internal/config` — treat the server source as the final authority.

<div class="grid cards" markdown>

-   :material-api: **HTTP API (Scalar)**

    ---

    The `/api/v2/` control-plane API — Airflow 3.2.x-compatible — as an
    interactive Scalar reference.

    [:octicons-arrow-right-24: Open the API reference](api-reference.html)

-   :material-console: **CLI reference**

    ---

    Every `leoflow` command and flag, generated from Cobra.

    [:octicons-arrow-right-24: CLI commands](cli/leoflow.md)

-   :material-language-go: **Go packages (GoDocs)**

    ---

    GoDocs for the control plane, scheduler, executor, agent, and storage —
    one page per package, each symbol linking to its source.

    [:octicons-arrow-right-24: Go packages](go-api.md)

-   :material-language-python: **Python runtime API**

    ---

    The task-runtime helpers your DAG code imports (XCom, staging paths).

    [:octicons-arrow-right-24: Python runtime](python-api.md)

-   :material-cog: **Configuration**

    ---

    The `LEOFLOW_*` environment variables and config keys for the server,
    hand-maintained against `internal/config`.

    [:octicons-arrow-right-24: Configuration](configuration.md)

</div>
