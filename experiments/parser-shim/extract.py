"""PoC extractor (issue #83): load a dag.py against the stdlib-only Airflow shim
and produce the same structural fields parser/leoflow_parser/compiler.py emits —
without importing real Airflow.

Run: python extract.py path/to/dag.py [--dag-id X]
"""
from __future__ import annotations

import inspect
import os
import runpy
import sys
from pathlib import Path
from typing import Any

# Put this directory first so the bundled `airflow` shim shadows any real Airflow.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import airflow._core as core  # noqa: E402

_SUPPORTED_TRIGGER_RULES = {"all_success", "all_done", "all_failed", "one_success", "one_failed"}


class UnsupportedOperator(Exception):
    """Raised for an operator type Leoflow does not support."""


def load_dags(source: str) -> dict:
    """Exec the DAG file against the shim; return {dag_id: DAG}. A missing shim
    module (an unsupported provider/operator) surfaces as a clear import error."""
    core.reset()
    try:
        runpy.run_path(source, run_name="__leoflow_dag__")
    except ModuleNotFoundError as exc:
        raise UnsupportedOperator(
            f"{source}: imports an operator Leoflow does not support ({exc.name}). "
            f"Supported: BashOperator, HttpOperator, PythonOperator / @task."
        ) from exc
    return dict(core.COLLECTED)


def operator_type(task) -> str:
    name = type(task).__name__
    if "Bash" in name:
        return "bash"
    if "Http" in name:
        return "http_api"
    if "Python" in name or "Empty" in name:
        return "python"
    raise UnsupportedOperator(f"unsupported operator {name!r} on task {task.task_id}")


def trigger_rule(task) -> str:
    value = getattr(task.trigger_rule, "value", str(task.trigger_rule))
    if value not in _SUPPORTED_TRIGGER_RULES:
        raise UnsupportedOperator(f"unsupported trigger rule {value!r} on task {task.task_id}")
    return value


def xcom_inputs(task) -> dict:
    fn = getattr(task, "python_callable", None)
    if fn is None:
        return {}
    try:
        bound = inspect.signature(fn).bind_partial(*getattr(task, "op_args", ()) or (),
                                                   **getattr(task, "op_kwargs", {}) or {})
    except (TypeError, ValueError):
        return {}
    out = {}
    for pname, value in bound.arguments.items():
        upstream = getattr(getattr(value, "operator", None), "task_id", None)
        if upstream:
            out[pname] = upstream
    return out


def map_task(task, source: str) -> dict[str, Any]:
    ttype = operator_type(task)
    entry: dict[str, Any] = {"task_id": task.task_id, "type": ttype}
    upstream = sorted(task.upstream_task_ids)
    if upstream:
        entry["depends_on"] = upstream
    rule = trigger_rule(task)
    if rule != "all_success":
        entry["trigger_rule"] = rule
    if ttype == "python":
        fn = getattr(task, "python_callable", None)
        name = getattr(fn, "__name__", task.task_id)
        entry["entrypoint"] = f"{Path(source).stem}:{name}"
        xin = xcom_inputs(task)
        if xin:
            entry["xcom_input"] = xin
    elif ttype == "bash":
        entry["entrypoint"] = getattr(task, "bash_command", "") or ""
    elif ttype == "http_api":
        entry["http_request"] = {
            "method": (getattr(task, "method", "GET") or "GET").upper(),
            "url": getattr(task, "endpoint", "") or "",
        }
    return entry


def _load_config(config: str | None) -> dict:
    if not config:
        return {}
    import yaml

    with open(config, encoding="utf-8") as fh:
        return yaml.safe_load(fh) or {}


def compile_dag(source: str, dag_id: str | None = None, config: str | None = None) -> dict:
    """Produce the same structural spec the real compiler emits (minus the
    pass-through ``image``), so golden comparison is apples-to-apples: dag_id,
    optional schedule, optional tags (config overrides the DAG), and tasks."""
    cfg = _load_config(config)
    dags = load_dags(source)
    if not dags:
        raise ValueError(f"no DAG found in {source}")
    want = dag_id or cfg.get("dag_id")
    dag = dags[want] if want and want in dags else next(iter(dags.values()))

    spec: dict[str, Any] = {
        "dag_id": dag.dag_id,
        "tasks": [map_task(dag.task_dict[t], source) for t in sorted(dag.task_dict)],
    }
    schedule = getattr(dag, "schedule", None)
    if schedule is not None:
        spec["schedule"] = schedule
    tags = cfg.get("tags") or sorted(dag.tags)
    if tags:
        spec["tags"] = list(tags)
    return spec


if __name__ == "__main__":
    import argparse
    import json

    ap = argparse.ArgumentParser()
    ap.add_argument("source")
    ap.add_argument("--dag-id")
    ap.add_argument("--config")
    args = ap.parse_args()
    print(json.dumps(compile_dag(args.source, args.dag_id, args.config), indent=2))
