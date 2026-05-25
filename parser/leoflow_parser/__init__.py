"""Leoflow DAG parser.

Compiles an Airflow DAG (Python source) into the canonical Leoflow dag.json,
without executing user task code.

The parser has no third-party runtime dependencies (ADR 0024): PyYAML is vendored
(pure-Python) under ``_vendor`` and placed on ``sys.path`` here, so ``import yaml``
resolves without a pip install. This lets the embedded parser run on the bare
managed CPython — no venv, no Airflow.
"""
import os as _os
import sys as _sys

__version__ = "0.1.0"

_vendor = _os.path.join(_os.path.dirname(_os.path.abspath(__file__)), "_vendor")
if _vendor not in _sys.path:
    _sys.path.insert(0, _vendor)
