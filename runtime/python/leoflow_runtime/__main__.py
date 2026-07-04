"""CLI entry point.

Three modes:
  python -m leoflow_runtime <module:callable>      # run a @task / Python entrypoint
  python -m leoflow_runtime --operator <class>     # run a captured Airflow operator
                                                   # (args from LEOFLOW_OPERATOR_ARGS JSON)
  python -m leoflow_runtime --bash <command>       # render {{ }} with the run context,
                                                   # then exec bash -c in place (#382)
"""

from __future__ import annotations

import json
import os
import sys

from leoflow_runtime.runner import run, run_bash, run_operator


def main(argv: list[str] | None = None) -> int:
    """Dispatch to the entrypoint runner, the generic operator executor, or bash."""
    args = sys.argv[1:] if argv is None else argv
    if len(args) == 2 and args[0] == "--bash":
        # Render the command with the run context, then exec bash in place (#382).
        run_bash(args[1])
        return 0
    if args and args[0] == "--dbt-profile" and len(args) in (3, 4):
        # Generate profiles.yml from the delivered managed connection (ADR 0043),
        # so a dbt task needs no credential baked into the image. An optional 4th
        # arg overrides the dbt target schema.
        from leoflow_runtime.dbt import write_dbt_profile

        profiles_dir = os.environ.get("DBT_PROFILES_DIR", os.path.expanduser("~/.dbt"))
        os.makedirs(profiles_dir, exist_ok=True)
        schema = args[3] if len(args) == 4 else None
        write_dbt_profile(args[1], args[2], profiles_dir, schema=schema)
        return 0
    if args and args[0] == "--dbt-default-duckdb" and len(args) in (2, 3):
        # Zero-config local warehouse (Lite): write a default duckdb profiles.yml when
        # a dbt group has no managed connection. Optional 3rd arg is the DB file path.
        from leoflow_runtime.dbt import write_dbt_default_duckdb

        profiles_dir = os.environ.get("DBT_PROFILES_DIR", os.path.expanduser("~/.dbt"))
        os.makedirs(profiles_dir, exist_ok=True)
        db_path = args[2] if len(args) == 3 else ""
        write_dbt_default_duckdb(args[1], profiles_dir, db_path)
        return 0
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
