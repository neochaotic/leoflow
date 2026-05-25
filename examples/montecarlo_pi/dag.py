"""montecarlo_pi — estimate pi with parallel Monte-Carlo workers, then combine."""
from __future__ import annotations

from airflow.sdk import DAG, task

WORKERS = 4
SAMPLES = 500_000


@task
def estimate(seed: int) -> dict:
    import random

    rng = random.Random(seed)
    inside = sum(1 for _ in range(SAMPLES) if rng.random() ** 2 + rng.random() ** 2 <= 1.0)
    print(f"worker {seed}: {inside}/{SAMPLES} inside the circle")
    return {"inside": inside, "total": SAMPLES}


@task
def combine(parts: list[dict]) -> None:
    inside = sum(p["inside"] for p in parts)
    total = sum(p["total"] for p in parts)
    pi = 4 * inside / total
    print(f"combine: {len(parts)} workers, {total:,} samples -> pi ≈ {pi:.5f}")


with DAG("montecarlo_pi", schedule=None, catchup=False, tags=["example"]):
    combine([estimate(s) for s in range(WORKERS)])
