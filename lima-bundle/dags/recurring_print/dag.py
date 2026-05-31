"""recurring_print — a 3-minute heartbeat DAG.

The simplest possible scheduler smoke: print the current time every 3 minutes,
forever. If the run history is steady (one new run every 3 min, every one
SUCCESS), the scheduler loop is healthy. If gaps or stuck `queued` runs appear,
something is wrong at the scheduler/dispatcher layer.

Use case in the Lima alpha hands-on (#231 + post-helm-polish-plan):
- Leave it running for ~1 h.
- Watch the dashboard: 20 successful runs, no stuck ones, no missed slots.
- If a run is missed or stuck, capture the timestamp and the scheduler log.
"""
from __future__ import annotations

from datetime import datetime

from airflow.sdk import DAG, task


@task
def heartbeat() -> str:
    msg = f"tick: {datetime.now().isoformat()}"
    print(msg)
    return msg


with DAG(
    "recurring_print",
    schedule="*/3 * * * *",   # every 3 minutes
    catchup=False,
    tags=["lima-alpha", "recurring", "smoke"],
):
    heartbeat()
