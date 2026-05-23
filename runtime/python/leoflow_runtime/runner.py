"""Run a user task callable and capture its return value."""

from __future__ import annotations

import importlib
import inspect
import json
import os

from leoflow_runtime.xcom import xcom_pull

DEFAULT_RETURN_VALUE_PATH = "/tmp/leoflow_return_value.json"  # noqa: S108

_UNSET = object()


def _resolve_kwargs(fn) -> dict:
    """Resolve each of fn's parameters from its injected upstream XCom.

    The agent injects each declared input as ``LEOFLOW_XCOM_<PARAM>``; this binds
    them to the function's parameters so a TaskFlow task that consumes an
    upstream's output (``transform(extract())``) receives it. Parameters with no
    injected XCom are left to their default (Airflow resolves a missing XCom to
    None / the default).
    """
    kwargs: dict = {}
    for name, param in inspect.signature(fn).parameters.items():
        if param.kind in (inspect.Parameter.VAR_POSITIONAL, inspect.Parameter.VAR_KEYWORD):
            continue
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
