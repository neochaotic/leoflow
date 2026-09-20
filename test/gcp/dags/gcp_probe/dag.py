"""gcp_probe: the smallest DAG that still exercises the pod-per-task path.

One task that sleeps briefly and exits. That is deliberate. These experiments
measure the time to GET a task pod running (dispatch, schedule, image pull,
container start), so the work inside the pod is noise that would only add
variance to the phase being measured.

The sleep is not zero: a pod that exits instantly can reach a terminal state
before the informer observes it Running, which would drop the `start` phase
observation for that pod and quietly bias the series toward the slow pods that
did get observed.
"""
from __future__ import annotations

import os
import time

from airflow.sdk import DAG, task


@task
def probe() -> dict:
    dwell = float(os.environ.get("GCP_PROBE_DWELL_SECONDS", "5"))
    started = time.time()
    time.sleep(dwell)
    # Printed so a pod's own log can be correlated with the phase timings the
    # runner reconstructs from the Kubernetes API, which are quantized to one
    # second and cannot be checked against anything else.
    print(f"probe: dwelled {time.time() - started:.2f}s")
    return {"dwell": dwell}


with DAG(
    "gcp_probe",
    schedule=None,          # triggered by the runner, never by a timer
    catchup=False,
    max_active_runs=64,     # the runner drives concurrency; the DAG must not cap it
    tags=["gcp-experiment"],
):
    probe()
