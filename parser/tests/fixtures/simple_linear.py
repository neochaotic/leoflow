"""Simple linear DAG: extract -> load."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def extract() -> int:
    return 1


@task
def load(value: int) -> None:
    _ = value


with DAG("simple_linear", schedule="@daily", catchup=False, tags=["example"]):
    load(extract())
