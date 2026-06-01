"""fan_out_aggregate — fan-out to parallel workers, then fan-in to an aggregate.

Each shard runs as its own task/pod in parallel; the aggregate depends on all of
them and combines their XCom results.
"""
from __future__ import annotations

from airflow.sdk import DAG, task

SHARDS = 4


@task
def shard(n: int) -> dict:
    total = sum(i for i in range(n * 1000, (n + 1) * 1000))
    print(f"shard {n}: summed its slice -> {total}")
    return {"shard": n, "total": total}


@task
def aggregate(parts: list[dict]) -> dict:
    grand = sum(p["total"] for p in parts)
    print(f"aggregate: combined {len(parts)} shards -> {grand}")
    # Return the aggregated value so it lands in XCom — the whole point of a
    # fan-in DAG is the reducer's result, and the UI's XCom tab is where the
    # user expects to see it. Returning None would erase the demo's payoff.
    return {"shards": len(parts), "grand_total": grand}


with DAG("fan_out_aggregate", schedule=None, catchup=False, tags=["example"]):
    aggregate([shard(n) for n in range(SHARDS)])
