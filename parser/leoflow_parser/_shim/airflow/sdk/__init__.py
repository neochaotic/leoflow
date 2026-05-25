"""Shim of `airflow.sdk`: DAG, the @task TaskFlow decorator, and base operators."""
from __future__ import annotations

import functools

from airflow._core import DAG, BaseOperator, XComArg

__all__ = ["DAG", "BaseOperator", "XComArg", "task", "EmptyOperator", "PythonOperator"]


class PythonOperator(BaseOperator):
    """Classic PythonOperator (name carries 'Python' -> Leoflow 'python')."""


class EmptyOperator(BaseOperator):
    """No-op operator."""


def _iter_xcomargs(value):
    """Yield every XComArg in value, recursing through lists/tuples/sets/dicts."""
    if isinstance(value, XComArg):
        yield value
    elif isinstance(value, (list, tuple, set)):
        for item in value:
            yield from _iter_xcomargs(item)
    elif isinstance(value, dict):
        for item in value.values():
            yield from _iter_xcomargs(item)


def task(fn=None, **dec_kwargs):
    """TaskFlow @task: calling the wrapped function builds a python operator and
    returns an XComArg, wiring upstream edges from any XComArg arguments
    (including those nested in lists/dicts, e.g. a fan-in)."""

    def wrap(func):
        @functools.wraps(func)
        def maker(*args, **kwargs):
            # @task(trigger_rule=…, …) decorator kwargs apply to the operator.
            op = PythonOperator(task_id=func.__name__, **dec_kwargs)
            op.python_callable = func
            op.op_args = args
            op.op_kwargs = kwargs
            op.function = func  # parity with the SDK's @task (.function unwrap)
            for value in list(args) + list(kwargs.values()):
                for xarg in _iter_xcomargs(value):
                    op.upstream_task_ids.add(xarg.operator.task_id)
            return XComArg(op)

        def _no_dynamic_mapping(*_a, **_k):
            raise NotImplementedError("dynamic task mapping (.expand/.partial)")

        maker.function = func
        maker.expand = _no_dynamic_mapping
        maker.partial = _no_dynamic_mapping
        return maker

    return wrap(fn) if callable(fn) else wrap
