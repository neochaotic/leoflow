"""Shim of ``airflow.models.param``: the older-style ``Param`` import path.

Airflow 3.x's canonical spelling is ``from airflow.sdk import Param``; this module
keeps ``from airflow.models.param import Param`` resolving to the same shim class
so DAGs written against either import compile identically.
"""
from airflow._core import Param

__all__ = ["Param"]
