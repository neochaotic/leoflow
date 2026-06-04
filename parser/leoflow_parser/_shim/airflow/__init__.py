"""Structural Airflow shim (ADR 0024). On import it installs the generic
provider-operator capture finder (ADR 0040, Phase A)."""
from airflow import _generic

_generic.install()
