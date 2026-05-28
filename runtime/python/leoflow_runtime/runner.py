"""Run a user task callable and capture its return value."""

from __future__ import annotations

import importlib
import inspect
import json
import os

from leoflow_runtime.xcom import xcom_pull

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
    """
    module_name, sep, fn_name = entrypoint.partition(":")
    if not sep or not module_name or not fn_name:
        raise ValueError(f"entrypoint must be 'module:callable', got {entrypoint!r}")

    module = importlib.import_module(module_name)
    fn = getattr(module, fn_name)
    # Airflow TaskFlow @task decorators are not executed when called directly —
    # calling them returns an XComArg (a task reference), not the function's
    # result. Unwrap to the underlying Python function so we run the user's code
    # and capture its real return value.
    if hasattr(fn, "function"):
        fn = fn.function
    result = fn(**_resolve_kwargs(fn))

    if result is not None:
        with open(return_value_path(), "w", encoding="utf-8") as f:
            json.dump(result, f)
