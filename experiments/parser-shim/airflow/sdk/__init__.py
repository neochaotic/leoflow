"""Shim of `airflow.sdk`: DAG, the @task TaskFlow decorator, and base operators."""
from __future__ import annotations

import functools

from airflow._core import DAG, BaseOperator, XComArg

__all__ = ["DAG", "BaseOperator", "XComArg", "task", "EmptyOperator", "PythonOperator"]


class PythonOperator(BaseOperator):
    """Classic PythonOperator (name carries 'Python' -> Leoflow 'python')."""


class EmptyOperator(BaseOperator):
    """No-op operator; mapped by Leoflow like a python task with no body."""


def task(fn=None, **dec_kwargs):
    """TaskFlow @task: calling the wrapped function builds a python operator and
    returns an XComArg, wiring upstream edges from any XComArg arguments."""

    def wrap(func):
        @functools.wraps(func)
        def maker(*args, **kwargs):
            op = PythonOperator(task_id=func.__name__)
            op.python_callable = func
            op.op_args = args
            op.op_kwargs = kwargs
            op.function = func  # parity with the SDK's @task (.function unwrap)
            for value in list(args) + list(kwargs.values()):
                upstream = getattr(getattr(value, "operator", None), "task_id", None)
                if upstream:
                    op.upstream_task_ids.add(upstream)
            return XComArg(op)

        maker.function = func
        return maker

    return wrap(fn) if callable(fn) else wrap
