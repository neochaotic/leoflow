"""taskflow_sales — a pure TaskFlow ETL passing data via XCom (no deps)."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def extract() -> list[dict]:
    # A deterministic synthetic dataset (no I/O, so the example always runs).
    return [{"region": ["N", "S", "E", "W"][i % 4], "amount": (i * 37) % 1000} for i in range(1000)]


@task
def transform(rows: list[dict]) -> dict:
    by_region: dict[str, int] = {}
    for r in rows:
        by_region[r["region"]] = by_region.get(r["region"], 0) + r["amount"]
    print(f"transform: {len(rows)} rows -> {len(by_region)} regions")
    return by_region


@task
def load(totals: dict) -> None:
    top = max(totals, key=totals.get)
    print(f"load: totals {totals}; top region {top} = {totals[top]}")


with DAG("taskflow_sales", schedule=None, catchup=False, tags=["example"]):
    load(transform(extract()))
