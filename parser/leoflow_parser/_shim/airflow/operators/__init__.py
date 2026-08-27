"""Placeholder `airflow.operators` package.

The core ``airflow.operators.*`` operators were removed from Airflow in 3.0 and
relocated to apache-airflow-providers-standard, so the shim deliberately provides
NO submodules here — importing one must fail. This package exists only so that
``from airflow.operators.<x> import <Op>`` fails on ``airflow.operators.<x>``
(surfacing the specific submodule name) rather than on the bare
``airflow.operators`` parent, letting the loader name the canonical
``airflow.providers.standard.operators.<x>`` replacement in its error.
"""
