"""soak_ingest: the ingest leg of the soak battery.

Shape: a single wide write followed by a dependent read-back. It is the DAG that
generates the data volume with DuckDB, and it is deliberately the only one that
writes a large file, so the disk budget has exactly one owner to reason about.

Task types exercised: `python` (native).

Volume is bounded by SOAK_ROWS (default 400_000, about 8 MB of Parquet). The
harness overrides it through the soak config file, never by editing this DAG, so
a run at a different volume is still the same code path.

Heavy imports live inside the task bodies: the DAG parser imports this module to
extract structure and does not have the task venv's dependencies.
"""
from __future__ import annotations

import json
import os
import time

from airflow.sdk import DAG, task

# Inlined, not imported from a sibling module on purpose: Lite materializes a
# task's work dir from dag.json.source, which carries dag.py verbatim and
# nothing else, so a `from soak_common import ...` would resolve at parse time
# and fail at run time. Every soak DAG carries its own copy of these six lines.
SOAK_HOME = os.environ.get("SOAK_DATA_DIR") or os.path.expanduser("~/.leoflow/soak-data")


def soak_int(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name, "") or default)
    except ValueError:
        return default


def run_dir(dag_id: str) -> str:
    """A per-DAG scratch directory, reused across runs so disk stays bounded."""
    d = os.path.join(SOAK_HOME, "scratch", dag_id)
    os.makedirs(d, exist_ok=True)
    return d


def data_dir() -> str:
    os.makedirs(SOAK_HOME, exist_ok=True)
    return SOAK_HOME


@task
def extract() -> dict:
    import duckdb

    rows = soak_int("SOAK_ROWS", 400_000)
    out = run_dir("soak_ingest")
    raw = os.path.join(out, "events.parquet")
    t0 = time.time()
    con = duckdb.connect()
    con.execute("PRAGMA enable_progress_bar=false")
    con.execute(
        f"""
        COPY (
          SELECT i AS event_id,
                 TIMESTAMP '2026-01-01' + (i % 604800) * INTERVAL 1 SECOND AS ts,
                 'tenant_' || (i % 7)   AS tenant,
                 'event_'  || (i % 23)  AS kind,
                 (random() * 1000)::DECIMAL(10, 2) AS amount,
                 (random() * 10)::INT   AS qty
          FROM range({rows}) t(i)
        ) TO '{raw}' (FORMAT parquet)
        """
    )
    size = os.path.getsize(raw)
    print(f"extract: {rows} rows to {raw} ({size / 1e6:.1f} MB) in {time.time() - t0:.1f}s")
    return {"raw": raw, "rows": rows, "size_bytes": size}


@task
def verify(meta: dict) -> dict:
    """Read the written file back and assert the row count survived.

    This is the data-integrity half of the soak: a scheduler that dispatches the
    downstream task before the upstream file is durable, or that replays an
    attempt after a crash, shows up here as a count mismatch rather than as a
    green run with wrong data.
    """
    import duckdb

    t0 = time.time()
    con = duckdb.connect()
    got = con.execute(f"SELECT COUNT(*) FROM read_parquet('{meta['raw']}')").fetchone()[0]
    if got != meta["rows"]:
        raise RuntimeError(f"row count mismatch: wrote {meta['rows']}, read back {got}")
    by_tenant = con.execute(
        f"SELECT tenant, COUNT(*) FROM read_parquet('{meta['raw']}') GROUP BY tenant ORDER BY tenant"
    ).fetchall()
    receipt = os.path.join(data_dir(), "receipts", "soak_ingest.jsonl")
    os.makedirs(os.path.dirname(receipt), exist_ok=True)
    with open(receipt, "a", encoding="utf-8") as fh:
        fh.write(json.dumps({"at": time.time(), "rows": got, "bytes": meta["size_bytes"]}) + "\n")
    print(f"verify: {got} rows across {len(by_tenant)} tenants in {time.time() - t0:.1f}s")
    return {"rows": got, "tenants": len(by_tenant)}


with DAG("soak_ingest", schedule="*/5 * * * *", catchup=False, max_active_runs=1, tags=["soak"]):
    verify(extract())
