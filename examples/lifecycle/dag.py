"""lifecycle — a three-task pipeline that passes data via XCom.

Each task prints, sleeps, and prints again so the run shows real (non-trivial)
durations and multi-line logs in the UI. The TaskFlow return values flow
extract -> transform -> load as XCom, exercising cross-task data passing.
"""
from __future__ import annotations

import time

from airflow.sdk import DAG, task


@task
def extract() -> dict:
    print("extract: starting")
    time.sleep(2)
    print("extract: queried source, produced 100 rows")
    time.sleep(2)
    print("extract: done")
    return {"rows": 100}


@task
def transform(data: dict) -> dict:
    print(f"transform: starting, received {data}")
    time.sleep(2)
    out = {"rows": data["rows"], "doubled": data["rows"] * 2}
    print(f"transform: computed {out}")
    time.sleep(2)
    print("transform: done")
    return out


@task
def load(result: dict) -> None:
    print(f"load: starting, writing {result}")
    time.sleep(2)
    print("load: committed to warehouse")
    time.sleep(2)
    print("load: done")


with DAG("lifecycle", schedule=None, catchup=False, tags=["example"]):
    load(transform(extract()))
