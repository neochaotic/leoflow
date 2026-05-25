"""PoC validation (issue #83): every example DAG parses through the stdlib-only
shim, and an unsupported operator raises a clear error. Run from this directory:

    python3 -m pytest test_examples.py -q
"""
from __future__ import annotations

import textwrap
from pathlib import Path

import pytest

import extract

REPO = Path(__file__).resolve().parents[2]
EXAMPLES = sorted((REPO / "examples").glob("*/dag.py"))


@pytest.mark.parametrize("dag_file", EXAMPLES, ids=lambda p: p.parent.name)
def test_example_parses(dag_file):
    out = extract.compile_dag(str(dag_file))
    assert out["dag_id"], f"no dag_id for {dag_file}"
    assert out["tasks"], f"no tasks for {dag_file}"
    for task in out["tasks"]:
        assert task["type"] in {"python", "bash", "http_api"}
        assert task["task_id"]


def test_taskflow_dependencies():
    out = extract.compile_dag(str(REPO / "examples" / "taskflow_sales" / "dag.py"))
    by_id = {t["task_id"]: t for t in out["tasks"]}
    # transform consumes extract; load consumes transform (XCom-derived edges).
    assert "extract" in by_id["transform"].get("depends_on", [])
    assert "transform" in by_id["load"].get("depends_on", [])


def test_unsupported_operator_is_clear(tmp_path):
    dag = tmp_path / "dag.py"
    dag.write_text(textwrap.dedent("""
        from airflow.sdk import DAG
        from airflow.providers.amazon.aws.operators.s3 import S3CreateBucketOperator
        with DAG("unsup"):
            S3CreateBucketOperator(task_id="b", bucket_name="z")
    """))
    with pytest.raises(extract.UnsupportedOperator) as ei:
        extract.compile_dag(str(dag))
    assert "does not support" in str(ei.value)
