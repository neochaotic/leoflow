"""Shim of `airflow.decorators`.

In Airflow 3 the TaskFlow decorators live in `airflow.sdk`, but the pre-3
spelling ``from airflow.decorators import task``/``dag`` is still valid (only
deprecated). Both spellings must resolve to the SAME shim objects, so the parser
accepts a canonical Airflow-3 DAG regardless of which one an author wrote.
"""
from __future__ import annotations

from airflow.sdk import dag, task

__all__ = ["task", "dag"]
