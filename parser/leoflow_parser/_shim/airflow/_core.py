"""Structural shim of Apache Airflow for *parsing only* (ADR 0024).

It records DAG/operator structure when a ``dag.py`` is exec'd, without importing
real Airflow. Pure standard library, zero third-party dependencies. It reproduces
exactly the attribute surface ``leoflow_parser.compiler`` reads, and TaskFlow
``@task`` calls only build structure (task bodies never run).

Unsupported operators are simply absent from this package, so importing one
raises ModuleNotFoundError — which the loader turns into a clear "not supported"
error (the behavior ADR 0024 specifies).
"""
from __future__ import annotations

_CURRENT: list = []  # stack of DAGs currently being defined
COLLECTED: dict = {}  # dag_id -> DAG, populated as each DAG context is entered


def reset() -> None:
    """Clear collected state between DAG files (the loader calls this)."""
    _CURRENT.clear()
    COLLECTED.clear()


class XComArg:
    """Duck-typed stand-in for Airflow's XComArg: carries the producing operator
    and proxies dependency operators (``>>`` / ``<<``) to it, so TaskFlow chains
    like ``a() >> b()`` and ``x >> [y(), z()]`` wire edges correctly."""

    def __init__(self, operator):
        self.operator = operator

    def __rshift__(self, other):
        return self.operator.__rshift__(other)

    def __lshift__(self, other):
        return self.operator.__lshift__(other)


def _as_operator(node):
    """Unwrap an XComArg to its operator; pass operators through unchanged."""
    return node.operator if isinstance(node, XComArg) else node


class BaseOperator:
    """Minimal operator base: registers into the active DAG and tracks edges."""

    def __init__(self, task_id, **kwargs):
        self.upstream_task_ids: set[str] = set()
        self.downstream_task_ids: set[str] = set()
        self.trigger_rule = kwargs.get("trigger_rule", "all_success")
        for key, value in kwargs.items():
            setattr(self, key, value)
        # Attach to a DAG: an explicit dag= kwarg wins (operators built outside a
        # `with DAG()` block), otherwise the active context DAG. Mirrors Airflow.
        target = kwargs.get("dag") or (_CURRENT[-1] if _CURRENT else None)
        if target is not None:
            # Airflow auto-suffixes a duplicate task_id within a DAG (__1, __2, …).
            self.task_id = target.unique_task_id(task_id)
            target.add_task(self)
        else:
            self.task_id = task_id

    def _link(self, others, downstream: bool):
        targets = others if isinstance(others, (list, tuple)) else [others]
        for other in targets:
            other_op = _as_operator(other)
            ups, downs = (self, other_op) if downstream else (other_op, self)
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
        # Collect on construction too, so DAGs defined without `with` (e.g.
        # module-level `dag = DAG(...)` with operators attached via dag=) are seen.
        COLLECTED[dag_id] = self

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
