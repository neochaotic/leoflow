"""Run a user task callable and capture its return value."""

from __future__ import annotations

import importlib
import inspect
import json
import os
import sys

from leoflow_runtime.xcom import xcom_pull

# Lifecycle messages are prefixed so the user can distinguish runtime-emitted
# lines from their own print(). They go to stdout (not stderr) on purpose:
# stderr maps to log level=error in the UI, which would visually scream on
# every successful run.
_LIFECYCLE_PREFIX = "[leoflow]"


def _lifecycle(msg: str) -> None:
    """Emit a one-line lifecycle event the user sees in the UI log panel."""
    print(f"{_LIFECYCLE_PREFIX} {msg}", flush=True)


def _group_start(title: str) -> None:
    """Open a collapsible log group in the Airflow 3.2 SPA log viewer.

    The SPA renders ``::group::TITLE`` and ``::endgroup::`` markers as
    expandable sections in the log panel, matching Airflow's own
    Pre/Post-task execution grouping. We wrap our lifecycle lines so a
    real task's log isn't dominated by setup noise — the user's print()
    output stays out of any group, visible by default.
    """
    print(f"::group::{title}", flush=True)


def _group_end() -> None:
    """Close the most recently opened log group."""
    print("::endgroup::", flush=True)

DEFAULT_RETURN_VALUE_PATH = "/tmp/leoflow_return_value.json"  # noqa: S108

_UNSET = object()


def _load_call_args() -> dict:
    """Decode LEOFLOW_CALL_ARGS_JSON, the compile-time TaskFlow literals (#115).

    The parser captures literal call args of a ``@task`` invocation
    (``shard(n=0)`` → ``{"n": 0}``) and the agent stamps the result as the
    LEOFLOW_CALL_ARGS_JSON env var. Malformed JSON is silently dropped: the
    parser's contract is to emit valid JSON, and dying with a JSON error the
    user did not write would be worse than running with the function's
    defaults. The env name is call_args (not params) to leave Airflow's
    DAG-run params term free for a future feature (#148).
    """
    raw = os.environ.get("LEOFLOW_CALL_ARGS_JSON", "")
    if not raw:
        return {}
    try:
        decoded = json.loads(raw)
    except (TypeError, ValueError):
        return {}
    return decoded if isinstance(decoded, dict) else {}


def _resolve_kwargs(fn) -> dict:
    """Resolve each of fn's parameters from compile-time literals and upstream XCom.

    Two injection paths are merged into the same kwargs map:

    - **LEOFLOW_CALL_ARGS_JSON** (#115): the literal args the user wrote at
      the ``@task`` call site (``shard(n=0)``), captured by the parser at
      compile time.
    - **LEOFLOW_XCOM_<PARAM>**: an upstream task's ``return_value``, fetched
      by the agent at dispatch time. Takes precedence over a same-name
      literal so an explicit upstream binding always wins (in practice
      ``shard(extract())`` would only have one or the other; the deterministic
      precedence keeps the contract clean).

    Parameters with neither binding are left unset so the function's defaults
    apply (or it raises TypeError if it has none — exactly Airflow's
    semantics).
    """
    call_args = _load_call_args()
    kwargs: dict = {}
    for name, param in inspect.signature(fn).parameters.items():
        if param.kind in (inspect.Parameter.VAR_POSITIONAL, inspect.Parameter.VAR_KEYWORD):
            continue
        if name in call_args:
            kwargs[name] = call_args[name]
        value = xcom_pull(name, _UNSET)
        if value is not _UNSET:
            kwargs[name] = value
    return kwargs


def return_value_path() -> str:
    """Return the path the task's return value is written to.

    Overridable via ``LEOFLOW_RETURN_VALUE_PATH`` (primarily for tests).
    """
    return os.environ.get("LEOFLOW_RETURN_VALUE_PATH", DEFAULT_RETURN_VALUE_PATH)


def run(entrypoint: str) -> None:
    """Import and call ``module:callable``, writing a non-None return as JSON.

    The agent reads the file and pushes it as the task's ``return_value`` XCom.
    A None return writes nothing, so downstream tasks see no XCom.

    Emits three lifecycle lines so the UI's log panel is informative even when
    the user function is silent: ``loading <entrypoint>``, ``resolved kwargs:
    {...}`` (when any), and ``returned <repr>`` (success) or no extra line on
    raise (the agent wraps the traceback). All three use ``[leoflow]`` prefix
    via :func:`_lifecycle` so they are distinguishable from user ``print()``.
    """
    module_name, sep, fn_name = entrypoint.partition(":")
    if not sep or not module_name or not fn_name:
        raise ValueError(f"entrypoint must be 'module:callable', got {entrypoint!r}")

    # Pre task execution group: setup that happens BEFORE the user function
    # runs. Collapsed by default in the UI so the user's print() output is the
    # focus; expand to see what the runtime did to prepare.
    _group_start("Pre task execution")
    _lifecycle(f"loading {entrypoint}")
    module = importlib.import_module(module_name)
    fn = getattr(module, fn_name)
    # Airflow TaskFlow @task decorators are not executed when called directly —
    # calling them returns an XComArg (a task reference), not the function's
    # result. Unwrap to the underlying Python function so we run the user's code
    # and capture its real return value.
    if hasattr(fn, "function"):
        fn = fn.function
    kwargs = _resolve_kwargs(fn)
    if kwargs:
        _lifecycle(f"resolved kwargs: {kwargs}")
    _group_end()

    try:
        result = fn(**kwargs)
    except Exception as exc:  # noqa: BLE001 — surface user errors to the log
        # Wrap the failure summary + traceback in a Post group too — same
        # collapse semantics, makes the panel scannable even on failure.
        _group_start("Post task execution")
        _lifecycle(f"user function {fn_name} raised {type(exc).__name__}: {exc}")
        # Stdout is line-buffered or `-u` unbuffered; flush stderr too so the
        # ordering in the log panel matches the wall-clock order.
        sys.stdout.flush()
        sys.stderr.flush()
        # Re-raise so Python's default handler emits the traceback to stderr —
        # the agent captures it. The Post group stays open across the
        # traceback so it's all one collapsible block; the group is closed
        # only on success (below) since on failure the process exits and the
        # UI auto-closes any open group at end-of-stream.
        raise

    # Post task execution group: wraps the return-value handling so the user's
    # output ends cleanly, then the lifecycle epilogue is collapsible.
    _group_start("Post task execution")
    if result is None:
        _lifecycle("returned None (no XCom pushed)")
        _group_end()
        return
    # Mention the type + payload size so the user can confirm the right value
    # is leaving the task without dumping huge return values into the log.
    payload = json.dumps(result)
    _lifecycle(f"returned {type(result).__name__} ({len(payload)} B XCom)")
    with open(return_value_path(), "w", encoding="utf-8") as f:
        f.write(payload)
    _group_end()
