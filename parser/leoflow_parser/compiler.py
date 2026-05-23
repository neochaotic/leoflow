"""Compile an Airflow DAG into the canonical Leoflow dag.json.

The compiler imports the DAG module through Airflow's DagBag (which never runs
task bodies), inspects ``dag.task_dict``, and maps each operator to a Leoflow
task. It supports Python (including TaskFlow ``@task``), Bash, and HTTP tasks.
"""
from __future__ import annotations

import inspect
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
    from airflow.dag_processing.dagbag import DagBag

    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        bag = DagBag(dag_folder=source, include_examples=False)

    if bag.import_errors:
        raise ValueError(f"failed to import {source}: {bag.import_errors}")
    if not bag.dags:
        raise ValueError(f"no DAG found in {source}")
    if dag_id and dag_id in bag.dags:
        return bag.dags[dag_id]
    if dag_id:
        raise ValueError(f"DAG {dag_id!r} not found in {source}; found {sorted(bag.dags)}")
    if len(bag.dags) > 1:
        raise ValueError(f"multiple DAGs in {source}; set dag_id in leoflow.yaml")
    return next(iter(bag.dags.values()))


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
