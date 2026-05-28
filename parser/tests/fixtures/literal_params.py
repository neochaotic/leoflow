"""Fixture for #115: TaskFlow @task called with literal kwargs.

shard(n=0), shard(n=1) etc. bind literal integers to the task's parameter
at DAG-build time. The compiler must capture these into the task spec so
the runtime can deliver them at execution.
"""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def shard(n: int) -> dict:
    return {"shard": n, "value": n * 10}


@task
def aggregate(rows: list) -> int:
    return sum(r["value"] for r in rows)


with DAG("literal_params", schedule="@daily", catchup=False, tags=["example"]):
    # Literal-only: each shard takes a different int — the most common pattern.
    aggregate([shard(0), shard(1), shard(2)])
