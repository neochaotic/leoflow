"""recurring_parallel — a 5-minute parallel-dispatch stress.

Four independent tasks fan out from a single seed, each sleeping a few seconds
and printing. Together they exercise:
- Scheduler tick under concurrent ready tasks (max_active_tasks pressure).
- Dispatcher buffer behavior (Pro pool vs Lite passthrough — same DAG, both
  editions; see ADR 0031).
- Recurring run-creation under the `max_active_runs` cap (#200): with the
  default cap of 16 and a 5-minute schedule, the run pool can't saturate
  unless individual runs hang for ≥80 minutes — useful contrast to the
  3-minute heartbeat DAG which spends ~zero time per run.

Lima alpha use:
- Leave it running for ~30 min.
- Watch each task's individual duration matches its sleep (no scheduling
  delay), and that the 4 tasks ALL finish within a few seconds of each
  other (dispatch fan-out is parallel, not serial).
"""
from __future__ import annotations

import time

from airflow.sdk import DAG, task


@task
def seed() -> int:
    return 4


@task
def worker_a() -> str:
    print("worker_a: sleeping 3 s")
    time.sleep(3)
    return "a"


@task
def worker_b() -> str:
    print("worker_b: sleeping 5 s")
    time.sleep(5)
    return "b"


@task
def worker_c() -> str:
    print("worker_c: sleeping 4 s")
    time.sleep(4)
    return "c"


@task
def worker_d() -> str:
    print("worker_d: sleeping 6 s")
    time.sleep(6)
    return "d"


@task
def aggregate(*results: str) -> str:
    out = ",".join(results)
    print(f"aggregate: {out}")
    return out


with DAG(
    "recurring_parallel",
    schedule="*/5 * * * *",   # every 5 minutes
    catchup=False,
    tags=["lima-alpha", "recurring", "parallel"],
):
    start = seed()
    a = worker_a()
    b = worker_b()
    c = worker_c()
    d = worker_d()
    start >> [a, b, c, d]
    aggregate(a, b, c, d)
