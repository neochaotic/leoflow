"""http_operator — HttpOperator hitting a public API (Leoflow's 'http_api' type).

Leoflow compiles an HttpOperator to an 'http_api' task that the control plane runs
INLINE (no pod): it issues the request itself. The endpoint here is a full URL, so
no Connection is needed for the example.
"""
from __future__ import annotations

from airflow.providers.http.operators.http import HttpOperator
from airflow.sdk import DAG

with DAG("http_operator", schedule=None, catchup=False, tags=["example"]):
    HttpOperator(
        task_id="get_todo",
        method="GET",
        endpoint="https://jsonplaceholder.typicode.com/todos/1",
    )
