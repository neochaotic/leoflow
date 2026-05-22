"""CLI entry point: ``python -m leoflow_runtime <module:callable>``."""

from __future__ import annotations

import sys

from leoflow_runtime.runner import run


def main(argv: list[str] | None = None) -> int:
    """Run the task entrypoint passed as the sole argument."""
    args = sys.argv[1:] if argv is None else argv
    if len(args) != 1:
        print("usage: python -m leoflow_runtime <module:callable>", file=sys.stderr)
        return 2
    run(args[0])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
