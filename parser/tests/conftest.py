"""Shared pytest setup: isolate Airflow's home and quiet its logging."""
from __future__ import annotations

import os
import tempfile

os.environ.setdefault("AIRFLOW_HOME", tempfile.mkdtemp(prefix="airflow_home_"))
os.environ.setdefault("AIRFLOW__LOGGING__LOGGING_LEVEL", "ERROR")
os.environ.setdefault("AIRFLOW__CORE__LOAD_EXAMPLES", "False")
