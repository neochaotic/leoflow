"""DAG-level scheduling/metadata attributes captured by the shim.

``max_active_runs`` (concurrency), ``catchup``/``start_date`` (backfill) and
``description`` (UI) are all honored by the domain + scheduler; the shim was
dropping them silently. The compiler now emits each only when set, so a DAG that
declares none keeps its compiled shape.
"""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

from leoflow_parser.compiler import compile_dag


def _compile(monkeypatch, tmp_path: Path, body: str, config: dict | None = None) -> dict:
    monkeypatch.setenv(
        "LEOFLOW_PROJECT_CONFIG_JSON",
        json.dumps(config or {"schema_version": "1.0"}),
    )
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1", dag_version="v1")


def test_dag_attrs_captured(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from datetime import datetime, timezone
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", description="nightly etl",
                 start_date=datetime(2026, 1, 1, tzinfo=timezone.utc),
                 max_active_runs=3, catchup=True):
            a()
    """)
    assert spec["description"] == "nightly etl"
    assert spec["start_date"] == "2026-01-01T00:00:00+00:00"
    assert spec["max_active_runs"] == 3
    assert spec["catchup"] is True


def test_dag_attrs_omitted_when_unset(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    for key in ("description", "start_date", "max_active_runs", "catchup"):
        assert key not in spec


def test_catchup_false_is_omitted(monkeypatch, tmp_path):
    # catchup defaults to False in Airflow 3; only an explicit True is emitted,
    # so the domain's zero-value (no backfill) is the compiled default.
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", catchup=False):
            a()
    """)
    assert "catchup" not in spec
