"""Structural shim of Apache Airflow for *parsing only* — PoC for issue #83.

It records DAG/operator structure when a ``dag.py`` is exec'd, without importing
real Airflow. Pure stdlib, zero third-party dependencies. It reproduces exactly
the surface ``parser/leoflow_parser/compiler.py`` reads: a DagBag-like loader,
``DAG.dag_id/tags/task_dict``, and per-task ``task_id / upstream_task_ids /
trigger_rule / python_callable / op_args / op_kwargs / bash_command / endpoint /
method / headers``, plus ``>>``/``<<`` dependencies and TaskFlow ``@task`` XComArg
wiring.

Unsupported operators are simply absent from the shim, so importing them raises
ModuleNotFoundError — which the loader turns into a clear "operator not supported"
import error (the behavior issue #83 asks for).
"""
from __future__ import annotations

_CURRENT: list = []  # stack of DAGs currently being defined
COLLECTED: dict = {}  # dag_id -> DAG, populated as each DAG context is entered


def reset() -> None:
    """Clear collected state between DAG files (the loader calls this)."""
    _CURRENT.clear()
    COLLECTED.clear()


class XComArg:
    """Duck-typed stand-in for Airflow's XComArg: carries the producing operator."""

    def __init__(self, operator):
        self.operator = operator


class BaseOperator:
    """Minimal operator base: registers into the active DAG and tracks edges."""

    def __init__(self, task_id, **kwargs):
        self.upstream_task_ids: set[str] = set()
        self.downstream_task_ids: set[str] = set()
        self.trigger_rule = kwargs.get("trigger_rule", "all_success")
        for key, value in kwargs.items():
            setattr(self, key, value)
        if _CURRENT:
            # Mirror Airflow: a duplicate task_id within a DAG is auto-suffixed
            # __1, __2, … (e.g. calling the same @task in a loop).
            self.task_id = _CURRENT[-1].unique_task_id(task_id)
            _CURRENT[-1].add_task(self)
        else:
            self.task_id = task_id

    def _link(self, others, downstream: bool):
        targets = others if isinstance(others, (list, tuple)) else [others]
        for other in targets:
            ups, downs = (self, other) if downstream else (other, self)
            downs.upstream_task_ids.add(ups.task_id)
            ups.downstream_task_ids.add(downs.task_id)
        return others

    def __rshift__(self, other):
        return self._link(other, downstream=True)

    def __lshift__(self, other):
        return self._link(other, downstream=False)


class DAG:
    """Context-manager DAG that collects the operators defined within it."""

    def __init__(self, dag_id, schedule=None, tags=None, **kwargs):
        self.dag_id = dag_id
        self.schedule = schedule
        self.tags = list(tags or [])
        self.task_dict: dict = {}

    def add_task(self, op):
        self.task_dict[op.task_id] = op

    def unique_task_id(self, task_id: str) -> str:
        if task_id not in self.task_dict:
            return task_id
        i = 1
        while f"{task_id}__{i}" in self.task_dict:
            i += 1
        return f"{task_id}__{i}"

    def __enter__(self):
        _CURRENT.append(self)
        COLLECTED[self.dag_id] = self
        return self

    def __exit__(self, *exc):
        _CURRENT.pop()
        return False
