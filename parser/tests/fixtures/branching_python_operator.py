"""Branching DAG using BranchPythonOperator — the silently-mistranslated case.

This fixture proves the parser REJECTS the construct rather than letting it
through as a plain python task (which would silently run every downstream
branch — issue #225). It uses a locally-defined operator subclass so the test
runs regardless of which shim classes are exported.
"""
from __future__ import annotations

from airflow.sdk import DAG, PythonOperator


class BranchPythonOperator(PythonOperator):
    """Stand-in for airflow.providers.standard.operators.python.BranchPythonOperator.

    The parser's substring match on 'Python' used to translate this to a plain
    python task — silently dropping the branch semantics.
    """


def _pick():
    return "left"


def _left():
    pass


def _right():
    pass


with DAG("branching_python_operator", schedule=None, catchup=False):
    pick = BranchPythonOperator(task_id="pick", python_callable=_pick)
    left = PythonOperator(task_id="left", python_callable=_left)
    right = PythonOperator(task_id="right", python_callable=_right)
    pick >> [left, right]
