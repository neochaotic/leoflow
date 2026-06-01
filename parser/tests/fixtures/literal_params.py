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


with DAG("literal_params", schedule="@daily", catchup=False, tags=["example"]):
    # Literal-only: each shard takes a different int — the most common pattern.
    # No downstream fan-in here: fan-in (`combine([shard(0), shard(1), ...])`)
    # is rejected by the parser today (xcom_input_many is post-alpha). The
    # test is about literal capture, not fan-in, so each shard stands alone.
    shard(0)
    shard(1)
    shard(2)
