"""soak_token: does a long-running task still hold a usable credential at the end?

A task pod is dispatched with a short-lived agent credential that the control
plane RENEWS on each heartbeat, up to auth.max_attempt_credential_lifetime
(24h by default). Renewal is the mechanism that lets an attempt outlive its own
token, and nothing in the battery exercised it: the longest task ran for seven
minutes, which crosses no boundary worth crossing.

The shape that matters is not "a task ran for a long time". It is "a task ran for
a long time AND THEN used its credential". A task that sleeps and exits proves
nothing, because the agent may need the token only at dispatch: the failure this
DAG looks for is a call that worked at minute one and fails at minute forty.

So the body does the same authenticated call three times:

  - at the start, to prove the credential worked at all. If this fails the
    deployment is broken in a way that has nothing to do with duration, and the
    task says so instead of burning an hour to report it;
  - in the middle, to place the failure in time if one happens;
  - at the end, after SOAK_TOKEN_SECONDS of sleeping, which is the assertion.

Resolving the managed Connection `soak_http` is what makes each call an
authenticated one: the connection is delivered by the control plane to the
agent, gated on the attempt's credential (ADR 0055), so a lapsed token fails
here and nowhere else in this DAG.

RETRIES ARE OFF, and that is deliberate. A retry is dispatched with a fresh
credential, so a task that died of a lapsed token would pass on attempt 2 and
the run would finish green. The one failure this DAG exists to catch is the one
a retry would erase.

WHAT THIS DOES NOT COVER. It proves renewal holds for as long as the task runs.
It does not reach auth.max_attempt_credential_lifetime, which is 24h by default,
so the ceiling that deliberately STOPS renewing is untested here. Testing that
means lowering the ceiling for a dedicated run and asserting the task fails for
that reason, which is a different experiment: this one asserts the happy path
holds, that one asserts the bound is enforced.

The schedule is hourly against a body that defaults to forty minutes, so a run
never collides with the next. If you raise SOAK_TOKEN_SECONDS past the interval,
max_active_runs=1 will block the following run and the cadence check will report
the DAG as starved. That reading would be correct and would mean this knob, not
the scheduler.
"""

from __future__ import annotations

import os
import time

from airflow.sdk import DAG, task


def soak_int(name: str, default: int) -> int:
    try:
        return max(1, int(os.environ.get(name, default)))
    except ValueError:
        return default


TOTAL_SECONDS = soak_int("SOAK_TOKEN_SECONDS", 2400)
SLICE_SECONDS = soak_int("SOAK_TOKEN_SLICE_SECONDS", 30)


def _authenticated_probe(label: str) -> str:
    """Resolve the managed connection, which is the call that needs a live token.

    Any failure is raised rather than caught. A task that logs a credential
    failure and returns success is a task that reports the bug as health, which
    is the shape this whole battery exists to remove.
    """
    from airflow.hooks.base import BaseHook

    conn = BaseHook.get_connection("soak_http")
    host = conn.host or ""
    if not host:
        raise RuntimeError(
            f"{label}: soak_http resolved to a connection with no host; "
            "the credential was accepted but the secret payload is empty"
        )
    print(f"{label}: resolved soak_http -> {host}", flush=True)
    return host


with DAG(
    "soak_token",
    schedule="0 * * * *",
    catchup=False,
    max_active_runs=1,
    tags=["soak"],
):

    @task
    def long_then_authenticate() -> dict:
        started = time.monotonic()
        first = _authenticated_probe("t=0")

        midpoint = TOTAL_SECONDS // 2
        probed_mid = False
        while True:
            elapsed = time.monotonic() - started
            if elapsed >= TOTAL_SECONDS:
                break
            if not probed_mid and elapsed >= midpoint:
                _authenticated_probe(f"t={int(elapsed)}s (midpoint)")
                probed_mid = True
            remaining = TOTAL_SECONDS - elapsed
            time.sleep(min(SLICE_SECONDS, remaining))

        elapsed = int(time.monotonic() - started)
        # The assertion. If the credential lapsed while this task slept, this is
        # where it shows, and it shows as a failed task rather than a warning
        # nobody reads.
        last = _authenticated_probe(f"t={elapsed}s (final)")
        if last != first:
            raise RuntimeError(
                f"soak_http resolved to {first!r} at the start and {last!r} after "
                f"{elapsed}s; the secret changed under a running attempt"
            )
        return {"seconds": elapsed, "probes": 3, "host": last}

    long_then_authenticate()
