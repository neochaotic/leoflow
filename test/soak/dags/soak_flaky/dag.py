"""soak_flaky: the failure and retry shape of the soak battery.

Two deliberately different failure modes, because the scheduler treats them
differently:

* `flaky` fails its first two attempts and succeeds on the third. It is what
  exercises the retry rail continuously. What the monitor actually asserts on it
  is one predicate, `try_number > max_tries`, so a try 5 on a budget of 4 is
  caught; "succeeded before try 3" is NOT asserted anywhere, and this docstring
  used to claim it was.
* `always_fails` never succeeds and exhausts its budget, so every run of this
  DAG reaches the terminal `failed` state. That is on purpose: a battery where
  every run is green never exercises run finalization on the failure branch, nor
  the on-failure alert path, nor the "downstream is upstream_failed" transition.

The attempt counter is kept on disk per (run_id, task_id) rather than read from
the run context, so the decision is deterministic across a control-plane restart.

Nothing reads that counter back yet, so it does not make a replay visible: a
replayed fourth execution simply succeeds like the third. It is written in this
shape because comparing it against the task instance's `try_number +
infra_attempts` is the cheapest at-most-once probe available to this battery, and
that comparison is the follow-up. Until it exists, the suite does not assert
at-most-once (see README section 1, C3).

Task types exercised: `python` (native).
"""
from __future__ import annotations

import json
import os

from airflow.sdk import DAG, task

SOAK_HOME = os.environ.get("SOAK_DATA_DIR") or os.path.expanduser("~/.leoflow/soak-data")
SUCCEED_ON_TRY = 3


def _attempt_file(run_id: str, task_id: str) -> str:
    d = os.path.join(SOAK_HOME, "attempts")
    os.makedirs(d, exist_ok=True)
    safe = "".join(ch if ch.isalnum() or ch in "-_." else "_" for ch in f"{task_id}__{run_id}")
    return os.path.join(d, safe + ".json")


def _bump(run_id: str, task_id: str) -> int:
    path = _attempt_file(run_id, task_id)
    n = 0
    if os.path.exists(path):
        try:
            with open(path, encoding="utf-8") as fh:
                n = int(json.load(fh).get("attempts", 0))
        except (ValueError, OSError):
            n = 0
    n += 1
    with open(path, "w", encoding="utf-8") as fh:
        json.dump({"attempts": n}, fh)
    return n


@task
def flaky(**context) -> int:
    run_id = str(context["run_id"])
    n = _bump(run_id, "flaky")
    print(f"flaky: attempt {n} of run {run_id}")
    if n < SUCCEED_ON_TRY:
        raise RuntimeError(f"soak_flaky: deliberate failure on attempt {n} (succeeds on {SUCCEED_ON_TRY})")
    return n


@task
def downstream_of_flaky(n: int) -> None:
    print(f"downstream_of_flaky: upstream succeeded on attempt {n}")


@task
def always_fails(**context) -> None:
    run_id = str(context["run_id"])
    n = _bump(run_id, "always_fails")
    raise RuntimeError(f"soak_flaky: always_fails attempt {n} of run {run_id} (by design)")


@task
def never_runs() -> None:
    """Must end `upstream_failed`, never `success`.

    If the harness ever sees this task succeed, the scheduler dispatched a task
    whose upstream failed, which is a correctness bug, not a flake.
    """
    raise AssertionError("soak_flaky: never_runs executed, but its upstream always fails")


with DAG("soak_flaky", schedule="*/7 * * * *", catchup=False, max_active_runs=1, tags=["soak"]):
    downstream_of_flaky(flaky())
    never_runs_task = never_runs()
    always_fails() >> never_runs_task
