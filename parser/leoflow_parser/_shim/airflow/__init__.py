"""Structural Airflow shim (ADR 0024). On import it installs the generic
provider-operator capture finder (ADR 0040, Phase A).

``DAG`` is re-exported here so the deprecated-but-valid Airflow-3 convenience
spelling ``from airflow import DAG`` resolves to the same shim as
``from airflow.sdk import DAG``."""
from airflow import _generic
from airflow._core import DAG

__all__ = ["DAG"]

_generic.install()
