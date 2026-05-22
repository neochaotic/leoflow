"""Mixed-operator DAG: bash -> python -> http."""
from __future__ import annotations

from airflow.providers.http.operators.http import HttpOperator
from airflow.providers.standard.operators.bash import BashOperator
from airflow.sdk import DAG, task


@task
def transform() -> None: ...


with DAG("mixed_operators", schedule=None, catchup=False):
    extract = BashOperator(task_id="extract", bash_command="echo extract")
    notify = HttpOperator(task_id="notify", method="POST", endpoint="https://example.com/hook")
    extract >> transform() >> notify
