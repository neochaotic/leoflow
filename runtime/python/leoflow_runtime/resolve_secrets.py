"""Resolve declared external secrets in the task pod (ADR 0060, 2b).

The agent drives this module (``python -m leoflow_runtime.resolve_secrets``) to
resolve a task's declared Connection/Variable names from the operator-configured
provider secrets backend, under the pod's own workload identity — the agent links
no cloud SDK. It reads a request on STDIN and writes hits on STDOUT:

    in : {"backend": "<class>", "backend_kwargs": {...},
          "connections": ["c1", ...], "variables": ["v1", ...]}
    out: {"connections": {"c1": "<uri>"}, "variables": {"v1": "<value>"}}

A name the backend does not have is OMITTED (a clean miss → the caller falls back
to the leoflow vault). Any hard failure (backend init, an unexpected fetch error)
exits non-zero with a TYPE-ONLY message on stderr — never the exception's text or
the secret value — so the agent fails the task closed without leaking.
"""

from __future__ import annotations

import importlib
import json
import os
import sys
from typing import Any


def load_backend(class_path: str, kwargs: dict[str, Any]) -> Any:
    """Import and instantiate the provider secrets backend class."""
    module_path, _, cls_name = class_path.rpartition(".")
    if not module_path:
        raise ValueError("backend must be a dotted class path")
    cls = getattr(importlib.import_module(module_path), cls_name)
    return cls(**kwargs)


def resolve(
    backend: Any, connections: list[str], variables: list[str]
) -> dict[str, dict[str, str]]:
    """Resolve each name against the backend; omit misses.

    Connections are rendered as Airflow connection URIs (``get_connection`` →
    ``get_uri``) so bash tasks reading ``$AIRFLOW_CONN_*`` work, not the raw stored
    value (which for some providers is a JSON blob). A ``None`` return is a miss.
    """
    out: dict[str, dict[str, str]] = {"connections": {}, "variables": {}}
    for name in connections:
        conn = backend.get_connection(name)
        if conn is not None:
            out["connections"][name] = conn.get_uri()
    for name in variables:
        val = backend.get_variable(name)
        if val is not None:
            out["variables"][name] = val
    return out


def _stdout_fd() -> int | None:
    """The OS file descriptor behind stdout, or None if it has none.

    A unit test may replace ``sys.stdout`` with an in-memory buffer (StringIO),
    which has no fileno; there is then no descriptor to protect and main writes
    the JSON to it directly.
    """
    try:
        return sys.stdout.fileno()
    except (AttributeError, OSError):
        return None


def main() -> int:
    """Read the request from stdin, resolve, write hits to stdout.

    Importing the provider backend and initialising it makes Airflow, its
    providers, alembic, and the secrets masker log to STDOUT (structlog defaults
    there). But stdout is a strict JSON channel the agent parses, so a single log
    line would corrupt the result ("malformed output"). Redirect the stdout
    descriptor to stderr for the whole resolve — the providers' logs then land on
    stderr, which the agent routes to its debug log — and emit the JSON result on
    the real stdout, so stdout carries the JSON and nothing else.
    """
    req = json.load(sys.stdin)
    fd = _stdout_fd()
    if fd is None:
        backend = load_backend(req["backend"], req.get("backend_kwargs") or {})
        out = resolve(backend, req.get("connections") or [], req.get("variables") or [])
        json.dump(out, sys.stdout)
        return 0

    saved = os.dup(fd)
    try:
        os.dup2(2, fd)  # provider/Airflow stdout logging -> stderr
        backend = load_backend(req["backend"], req.get("backend_kwargs") or {})
        out = resolve(backend, req.get("connections") or [], req.get("variables") or [])
    finally:
        sys.stdout.flush()  # flush their logs while fd still points at stderr
        os.dup2(saved, fd)  # restore the real stdout
        os.close(saved)
    json.dump(out, sys.stdout)
    sys.stdout.flush()
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:  # noqa: BLE001 — fail closed on any error
        # Type only: never print the exception text or a secret value, even to
        # stderr (the agent routes it to its debug log). The agent surfaces a
        # generic, sanitized failure to the task.
        print(f"resolve_secrets failed: {type(exc).__name__}", file=sys.stderr)
        sys.exit(1)
