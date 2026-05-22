"""Tests for the Leoflow DAG compiler against fixture DAGs."""
from __future__ import annotations

import json
from pathlib import Path

import pytest
import yaml
from jsonschema.validators import Draft202012Validator

from leoflow_parser.compiler import compile_dag

FIXTURES = Path(__file__).parent / "fixtures"
SCHEMA_PATH = Path(__file__).parents[2] / "docs" / "api" / "dag-schema.json"


@pytest.fixture(scope="session")
def dag_schema() -> dict:
    return json.loads(SCHEMA_PATH.read_text())


def _compile(tmp_path: Path, fixture: str, dag_id: str) -> dict:
    config = tmp_path / "leoflow.yaml"
    config.write_text(yaml.safe_dump({"dag_id": dag_id, "python_version": "3.11"}))
    return compile_dag(str(FIXTURES / f"{fixture}.py"), str(config), "test:v1")


def _tasks_by_id(spec: dict) -> dict:
    return {task["task_id"]: task for task in spec["tasks"]}


def test_simple_linear(tmp_path, dag_schema):
    spec = _compile(tmp_path, "simple_linear", "simple_linear")
    Draft202012Validator(dag_schema).validate(spec)

    assert spec["dag_id"] == "simple_linear"
    tasks = _tasks_by_id(spec)
    assert set(tasks) == {"extract", "load"}
    assert tasks["extract"]["type"] == "python"
    assert tasks["extract"]["entrypoint"] == "simple_linear:extract"
    assert tasks["load"]["depends_on"] == ["extract"]


def test_branching(tmp_path, dag_schema):
    spec = _compile(tmp_path, "branching", "branching")
    Draft202012Validator(dag_schema).validate(spec)

    tasks = _tasks_by_id(spec)
    assert set(tasks) == {"start", "left", "right"}
    assert tasks["left"]["depends_on"] == ["start"]
    assert tasks["right"]["depends_on"] == ["start"]
    assert "depends_on" not in tasks["start"]


def test_mixed_operators(tmp_path, dag_schema):
    spec = _compile(tmp_path, "mixed_operators", "mixed_operators")
    Draft202012Validator(dag_schema).validate(spec)

    tasks = _tasks_by_id(spec)
    assert tasks["extract"]["type"] == "bash"
    assert tasks["extract"]["entrypoint"] == "echo extract"
    assert tasks["transform"]["type"] == "python"
    assert tasks["transform"]["depends_on"] == ["extract"]
    assert tasks["notify"]["type"] == "http_api"
    assert tasks["notify"]["http_request"]["method"] == "POST"
    assert tasks["notify"]["http_request"]["url"] == "https://example.com/hook"
    assert tasks["notify"]["depends_on"] == ["transform"]


def test_missing_dag_raises(tmp_path):
    empty = tmp_path / "empty.py"
    empty.write_text("x = 1\n")
    config = tmp_path / "leoflow.yaml"
    config.write_text(yaml.safe_dump({"dag_id": "nope"}))
    with pytest.raises(ValueError):
        compile_dag(str(empty), str(config), "test:v1")
