"""Compile an Airflow DAG into the canonical Leoflow dag.json.

The compiler imports the DAG module through Airflow's DagBag (which never runs
task bodies), inspects ``dag.task_dict``, and maps each operator to a Leoflow
task. It supports Python (including TaskFlow ``@task``), Bash, and HTTP tasks.
"""
from __future__ import annotations

import inspect
import os
import runpy
import sys
import warnings
from pathlib import Path
from typing import Any

import yaml

_SUPPORTED_TRIGGER_RULES = {
    "all_success",
    "all_failed",
    "all_done",
    "one_success",
    "one_failed",
}


def compile_dag(
    source: str,
    config_path: str,
    image: str,
    dag_version: str = "dev",
) -> dict[str, Any]:
    """Compile the DAG in ``source`` into a dag.json dictionary."""
    config = _load_config(config_path)
    dag = _load_dag(source, config.get("dag_id"))

    spec: dict[str, Any] = {
        "schema_version": "1.0",
        "dag_id": dag.dag_id,
        "dag_version": dag_version,
        "image": image,
        "tasks": [_map_task(task, source) for task in _ordered_tasks(dag)],
    }

    schedule = _schedule(dag)
    if schedule is not None:
        spec["schedule"] = schedule
    if config.get("owner"):
        spec["owner"] = config["owner"]
    tags = config.get("tags") or sorted(getattr(dag, "tags", []) or [])
    if tags:
        spec["tags"] = list(tags)
    default_args = _default_args(config)
    if default_args:
        spec["default_args"] = default_args
    return spec


def _load_config(path: str) -> dict[str, Any]:
    with open(path) as handle:
        return yaml.safe_load(handle) or {}


def _load_dag(source: str, dag_id: str | None):
    """Load the DAG(s) from ``source`` and select one. The default backend is the
    dependency-free structural shim (ADR 0024); set LEOFLOW_PARSER_BACKEND=airflow
    to use the real Airflow DagBag (requires apache-airflow installed)."""
    if os.environ.get("LEOFLOW_PARSER_BACKEND") == "airflow":
        dags, error = _load_dags_airflow(source)
    else:
        dags, error = _load_dags_shim(source)

    if error:
        raise ValueError(f"failed to import {source}: {error}")
    if not dags:
        raise ValueError(f"no DAG found in {source}")
    if dag_id and dag_id in dags:
        return dags[dag_id]
    if dag_id:
        raise ValueError(f"DAG {dag_id!r} not found in {source}; found {sorted(dags)}")
    if len(dags) > 1:
        raise ValueError(f"multiple DAGs in {source}; set dag_id in leoflow.yaml")
    return next(iter(dags.values()))


def _ensure_shim_on_path() -> None:
    """Put the bundled `airflow` shim first on sys.path so the user's DAG imports
    resolve to it, dropping any real-airflow modules already imported in-process."""
    shim_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "_shim")
    try:
        sys.path.remove(shim_dir)
    except ValueError:
        pass
    sys.path.insert(0, shim_dir)
    for name in list(sys.modules):
        if (name == "airflow" or name.startswith("airflow.")) and \
                shim_dir not in (getattr(sys.modules[name], "__file__", "") or ""):
            del sys.modules[name]


def _load_dags_shim(source: str):
    """Exec the DAG file against the structural shim; return ({dag_id: DAG}, error).
    An unsupported operator surfaces as a clear import error (ADR 0024)."""
    _ensure_shim_on_path()
    import airflow._core as core  # the shim

    core.reset()
    # Put the DAG's own directory on sys.path so sibling-module imports
    # (`from helpers import ...` next to dag.py) resolve, as Airflow's DagBag does.
    source_dir = os.path.dirname(os.path.abspath(source))
    added_dir = source_dir not in sys.path
    if added_dir:
        sys.path.insert(0, source_dir)
    before = set(sys.modules)
    try:
        runpy.run_path(source, run_name="__leoflow_dag__")
    except ModuleNotFoundError as exc:
        return {}, _unsupported(f"module {exc.name!r}")
    except ImportError as exc:
        # A missing name the shim does not provide (e.g. `chain`, a Branch operator).
        return {}, _unsupported(str(exc))
    except NotImplementedError as exc:
        # A construct the shim deliberately rejects (e.g. dynamic task mapping).
        return {}, _unsupported(str(exc))
    finally:
        # Isolate compiles: drop modules the DAG imported (incl. sibling helpers)
        # and remove its directory, so repeated compiles never serve stale code.
        for name in set(sys.modules) - before:
            del sys.modules[name]
        if added_dir:
            try:
                sys.path.remove(source_dir)
            except ValueError:
                pass
    return dict(core.COLLECTED), None


def _unsupported(detail: str) -> str:
    return (f"{detail}: not supported by Leoflow "
            f"(supported: Bash, Http, Python/@task; no dynamic task mapping or task groups)")


def _load_dags_airflow(source: str):
    """Fallback loader using the real Airflow DagBag (opt-in)."""
    from airflow.dag_processing.dagbag import DagBag

    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        bag = DagBag(dag_folder=source, include_examples=False)
    error = f"{bag.import_errors}" if bag.import_errors else None
    return dict(bag.dags), error


def _ordered_tasks(dag) -> list[Any]:
    return [dag.task_dict[task_id] for task_id in sorted(dag.task_dict)]


def _map_task(task, source: str) -> dict[str, Any]:
    task_type = _operator_type(task)
    entry: dict[str, Any] = {"task_id": task.task_id, "type": task_type}

    upstream = sorted(task.upstream_task_ids)
    if upstream:
        entry["depends_on"] = upstream

    rule = _trigger_rule(task)
    if rule != "all_success":
        entry["trigger_rule"] = rule

    if task_type == "python":
        entry["entrypoint"] = _python_entrypoint(task, source)
        xcom_input = _xcom_inputs(task)
        if xcom_input:
            entry["xcom_input"] = xcom_input
    elif task_type == "bash":
        entry["entrypoint"] = _bash_command(task)
    elif task_type == "http_api":
        entry["http_request"] = _http_request(task)
    return entry


def _operator_type(task) -> str:
    name = type(task).__name__
    if "Bash" in name:
        return "bash"
    if "Http" in name:
        return "http_api"
    if "Python" in name:
        return "python"
    raise ValueError(f"unsupported operator {name!r} on task {task.task_id}")


def _trigger_rule(task) -> str:
    value = getattr(task.trigger_rule, "value", str(task.trigger_rule))
    if value not in _SUPPORTED_TRIGGER_RULES:
        raise ValueError(f"unsupported trigger rule {value!r} on task {task.task_id}")
    return value


def _python_entrypoint(task, source: str) -> str:
    callable_obj = getattr(task, "python_callable", None)
    name = getattr(callable_obj, "__name__", task.task_id)
    return f"{Path(source).stem}:{name}"


def _xcom_inputs(task) -> dict[str, str]:
    """Map each TaskFlow parameter that consumes an upstream output to its task.

    For ``transform(extract())`` the operator stores ``extract``'s XComArg in its
    op_args/op_kwargs; binding those to the callable's signature yields
    ``{"n": "extract"}``. XComArg is duck-typed (a value carrying an ``operator``
    with a ``task_id``) to stay robust across Airflow SDK versions. The agent
    fetches each upstream's return_value and injects it for the runner.
    """
    callable_obj = getattr(task, "python_callable", None)
    if callable_obj is None:
        return {}
    op_args = getattr(task, "op_args", ()) or ()
    op_kwargs = getattr(task, "op_kwargs", {}) or {}
    try:
        bound = inspect.signature(callable_obj).bind_partial(*op_args, **op_kwargs)
    except (TypeError, ValueError):
        return {}
    mapping: dict[str, str] = {}
    for name, value in bound.arguments.items():
        operator = getattr(value, "operator", None)
        upstream = getattr(operator, "task_id", None)
        if upstream:
            mapping[name] = upstream
    return mapping


def _bash_command(task) -> str:
    return getattr(task, "bash_command", "") or ""


def _http_request(task) -> dict[str, Any]:
    request: dict[str, Any] = {
        "method": (getattr(task, "method", "GET") or "GET").upper(),
        "url": getattr(task, "endpoint", "") or "",
    }
    headers = getattr(task, "headers", None)
    if headers:
        request["headers"] = dict(headers)
    body = getattr(task, "data", None)
    if body:
        request["body"] = body
    return request


def _schedule(dag) -> str | None:
    timetable = getattr(dag, "timetable", None)
    summary = getattr(timetable, "summary", None)
    if isinstance(summary, str) and summary:
        return summary
    schedule = getattr(dag, "schedule", None)
    return schedule if isinstance(schedule, str) else None


def _default_args(config: dict[str, Any]) -> dict[str, Any]:
    defaults = config.get("defaults") or {}
    out: dict[str, Any] = {}
    for key in ("retries", "retry_delay_seconds", "execution_timeout_seconds"):
        if key in defaults:
            out[key] = defaults[key]
    return out
