"""duckdb_http_csv — DuckDB reads a remote CSV over HTTP and aggregates it."""
from __future__ import annotations

import os

from airflow.sdk import DAG, task

# A small, stable public CSV.
CSV_URL = "https://raw.githubusercontent.com/datasciencedojo/datasets/master/titanic.csv"
STAGING = os.environ.get("LEOFLOW_STAGING_DIR", "/tmp")


@task
def aggregate() -> dict:
    import duckdb

    con = duckdb.connect()
    con.execute("INSTALL httpfs; LOAD httpfs;")
    rows = con.execute(
        f"""
        SELECT Pclass AS klass,
               COUNT(*)              AS n,
               ROUND(AVG(Age), 1)    AS avg_age,
               ROUND(AVG(Survived), 3) AS survival_rate
        FROM read_csv_auto('{CSV_URL}')
        GROUP BY Pclass ORDER BY Pclass
        """
    ).fetchall()
    result = {int(r[0]): {"n": r[1], "avg_age": r[2], "survival": r[3]} for r in rows}
    print(f"aggregate: titanic survival by class -> {result}")
    return result


@task
def report(by_class: dict) -> None:
    best = max(by_class, key=lambda k: by_class[k]["survival"])
    print(f"report: class {best} had the highest survival rate ({by_class[best]['survival']})")


with DAG("duckdb_http_csv", schedule=None, catchup=False, tags=["example"]):
    report(aggregate())
