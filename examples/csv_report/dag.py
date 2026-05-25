"""csv_report — a SCHEDULED daily report: generate a CSV, then summarize it.

Unlike the other examples (manual trigger), this one runs on a cron schedule
(06:00 every day), so the scheduler creates a run per logical date.
"""
from __future__ import annotations

import os

from airflow.sdk import DAG, task

OUT = os.path.join(os.environ.get("LEOFLOW_STAGING_DIR", "/tmp"), "report_input.csv")


@task
def generate() -> str:
    import csv
    import random

    rng = random.Random(42)
    with open(OUT, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["product", "units", "price"])
        for i in range(5000):
            w.writerow([f"sku_{i % 100}", rng.randint(1, 20), round(rng.uniform(5, 500), 2)])
    print(f"generate: wrote {OUT}")
    return OUT


@task
def report(path: str) -> None:
    import csv

    revenue: dict[str, float] = {}
    with open(path) as f:
        for row in csv.DictReader(f):
            revenue[row["product"]] = revenue.get(row["product"], 0.0) + int(row["units"]) * float(row["price"])
    top = max(revenue, key=revenue.get)
    print(f"report: {len(revenue)} products; top {top} = {revenue[top]:.2f}")


with DAG("csv_report", schedule="0 6 * * *", catchup=False, tags=["example", "scheduled"]):
    report(generate())
