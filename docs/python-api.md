# Python runtime API

The `leoflow_runtime` package runs **your task callable** inside the container and
bridges its return value to XCom. It is installed in the DAG image (and the dev
venv); your `dag.py` uses the **Apache Airflow Task SDK** (`from airflow.sdk import
DAG, task`), and the agent invokes `leoflow_runtime` to execute the callable.

::: leoflow_runtime.runner

::: leoflow_runtime.xcom
