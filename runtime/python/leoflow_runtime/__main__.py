"""CLI entry point.

Two modes:
  python -m leoflow_runtime <module:callable>      # run a @task / Python entrypoint
  python -m leoflow_runtime --operator <class>     # run a captured Airflow operator
                                                   # (args from LEOFLOW_OPERATOR_ARGS JSON)
"""

from __future__ import annotations

import json
import os
import sys

from leoflow_runtime.runner import run, run_operator


def main(argv: list[str] | None = None) -> int:
    """Dispatch to the entrypoint runner or the generic operator executor."""
    args = sys.argv[1:] if argv is None else argv
    if len(args) == 2 and args[0] == "--operator":
        raw = os.environ.get("LEOFLOW_OPERATOR_ARGS", "{}")
        try:
            operator_args = json.loads(raw)
        except (TypeError, ValueError) as exc:
            print(f"invalid LEOFLOW_OPERATOR_ARGS JSON: {exc}", file=sys.stderr)
            return 2
        run_operator(args[1], operator_args)
        return 0
    if len(args) == 1 and not args[0].startswith("--"):
        run(args[0])
        return 0
    print(
        "usage: python -m leoflow_runtime <module:callable>\n"
        "   or: python -m leoflow_runtime --operator <dotted.class>",
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
