"""Branching DAG: start fans out to left and right."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def start() -> None: ...


@task
def left() -> None: ...


@task
def right() -> None: ...


with DAG("branching", schedule=None, catchup=False):
    begin = start()
    begin >> [left(), right()]
