"""Author-declared DAG-run params (typed defaults + JSON-Schema).

A DAG may declare ``params=`` so its run conf gets defaults and trigger-time
validation. The compiler emits them into ``spec["params"]`` as
``{name: {default, schema}}``: a bare value has an empty schema, a ``Param``
carries the JSON-Schema built from its kwargs. A DAG that declares no params
emits no ``params`` key (back-compatible shape).
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


def test_bare_value_param_emits_default_and_empty_schema(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", params={"limit": 5}):
            a()
    """)
    assert spec["params"] == {"limit": {"default": 5, "schema": {}}}


def test_typed_param_emits_default_and_json_schema(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, Param, task
        @task
        def a() -> None: ...
        with DAG("g", params={"n": Param(3, type="integer", minimum=1)}):
            a()
    """)
    assert spec["params"] == {
        "n": {"default": 3, "schema": {"type": "integer", "minimum": 1}}
    }


def test_param_import_from_models_path_also_resolves(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.models.param import Param
        @task
        def a() -> None: ...
        with DAG("g", params={"env": Param("prod", enum=["dev", "prod"])}):
            a()
    """)
    assert spec["params"] == {
        "env": {"default": "prod", "schema": {"enum": ["dev", "prod"]}}
    }


def test_mixed_params_bare_and_typed(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, Param, task
        @task
        def a() -> None: ...
        with DAG("g", params={"limit": 5, "name": Param("x", type="string", maxLength=8)}):
            a()
    """)
    assert spec["params"] == {
        "limit": {"default": 5, "schema": {}},
        "name": {"default": "x", "schema": {"type": "string", "maxLength": 8}},
    }


def test_no_params_declared_emits_no_params_key(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    assert "params" not in spec
