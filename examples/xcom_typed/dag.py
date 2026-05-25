"""xcom_typed — pass typed dict payloads across tasks via XCom, with validation."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def produce() -> dict:
    batch = {"id": "batch-001", "items": list(range(1, 51)), "schema": "v1"}
    print(f"produce: batch {batch['id']} with {len(batch['items'])} items")
    return batch


@task
def validate(batch: dict) -> dict:
    assert batch.get("schema") == "v1", f"unexpected schema {batch.get('schema')}"
    assert batch["items"], "empty batch"
    checked = {"id": batch["id"], "count": len(batch["items"]), "sum": sum(batch["items"])}
    print(f"validate: ok -> {checked}")
    return checked


@task
def sink(checked: dict) -> None:
    print(f"sink: persisted {checked['id']} (count={checked['count']}, sum={checked['sum']})")


with DAG("xcom_typed", schedule=None, catchup=False, tags=["example"]):
    sink(validate(produce()))
