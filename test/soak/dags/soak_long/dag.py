"""soak_long: the long-running-task shape of the soak battery.

One task whose body runs for SOAK_LONG_SECONDS (default 420 s, seven minutes),
followed by a short dependent tail. It exists for two reasons that no other DAG
in the battery covers:

1. **The reapers must not fire on it.** The agent-lost threshold is 90 s and the
   pod-lost liveness floor is 60 s, so a task running for seven minutes is only
   safe because its heartbeats keep flowing for the whole body. A heartbeat that
   stops at, say, the first blocking call would show up as this task being
   failed `agent_lost` while it is demonstrably still running. That is a silent
   data-loss class in production and this is the cheapest place to catch it.

2. **The scheduler must not wedge behind it.** While this task runs, every other
   DAG in the battery keeps its cadence. If the per-hop latency of `soak_chain`
   degrades during this DAG's window, something in the tick is serialized behind
   a running task.

The body sleeps in short slices and prints a progress line per slice, so the log
itself is evidence of continuity: a gap in the printed timestamps is a stall.

Task types exercised: `python` (native).
"""
from __future__ import annotations

import os
import time

from airflow.sdk import DAG, task

SOAK_HOME = os.environ.get("SOAK_DATA_DIR") or os.path.expanduser("~/.leoflow/soak-data")
SLICE_SECONDS = 15


def soak_int(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name, "") or default)
    except ValueError:
        return default


@task
def long_task() -> dict:
    total = soak_int("SOAK_LONG_SECONDS", 420)
    started = time.time()
    slices = max(1, total // SLICE_SECONDS)
    worst_gap = 0.0
    last = started
    for i in range(slices):
        time.sleep(SLICE_SECONDS)
        now = time.time()
        worst_gap = max(worst_gap, now - last - SLICE_SECONDS)
        last = now
        print(f"long_task: slice {i + 1}/{slices} at +{now - started:.1f}s (drift {worst_gap:.2f}s)")
    return {"seconds": round(time.time() - started, 1), "worst_gap": round(worst_gap, 2)}


@task
def tail(meta: dict) -> None:
    print(f"tail: upstream ran {meta['seconds']}s with worst scheduling gap {meta['worst_gap']}s")


with DAG("soak_long", schedule="*/15 * * * *", catchup=False, max_active_runs=1, tags=["soak"]):
    tail(long_task())
