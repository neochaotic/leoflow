"""Map a managed Leoflow/Airflow connection URI to a dbt profile (ADR 0043, #2).

Connections are delivered to the task pod as ``AIRFLOW_CONN_<ID>`` (an Airflow
connection URI). A dbt task generates its ``profiles.yml`` from that at runtime,
so no warehouse credential is baked into the image. Postgres first; other
adapters (snowflake, bigquery, …) are follow-ons.
"""
from __future__ import annotations

import json
import os
from urllib.parse import parse_qs, unquote, urlsplit

_POSTGRES_SCHEMES = {"postgres", "postgresql"}


def dbt_profile_from_uri(uri: str) -> dict:
    """Return the dbt ``outputs.<target>`` block for a connection URI.

    The Airflow URI's path is the database (Airflow's "schema" = dbt's
    ``dbname``); the dbt target ``schema`` and ``threads`` come from query
    parameters, defaulting to ``public`` and ``4``. Only postgres is supported
    for now; any other scheme is a loud error.
    """
    parts = urlsplit(uri)
    scheme = parts.scheme.lower()
    if scheme not in _POSTGRES_SCHEMES:
        raise ValueError(
            f"unsupported dbt connection scheme {scheme!r}; only postgres is "
            "supported for now (snowflake/bigquery are follow-ons)"
        )
    query = parse_qs(parts.query)

    def _first(key: str, default: str) -> str:
        return query.get(key, [default])[0]

    return {
        "type": "postgres",
        "host": parts.hostname or "",
        "port": parts.port or 5432,
        "user": unquote(parts.username or ""),
        "password": unquote(parts.password or ""),
        "dbname": unquote(parts.path.lstrip("/")),
        "schema": _first("schema", "public"),
        "threads": int(_first("threads", "4")),
    }


def write_dbt_profile(conn_id: str, profile_name: str, profiles_dir: str, env=None) -> str:
    """Generate ``<profiles_dir>/profiles.yml`` from the ``AIRFLOW_CONN_<conn_id>``
    the agent delivered, under the dbt project's ``profile_name``. Written as JSON,
    which is valid YAML, so dbt reads it without us depending on PyYAML. Returns the
    written path; a missing connection is a loud error.
    """
    env = os.environ if env is None else env
    key = f"AIRFLOW_CONN_{conn_id.upper()}"
    uri = env.get(key)
    if not uri:
        raise RuntimeError(
            f"dbt connection {conn_id!r} was not delivered to the task ({key} is unset)"
        )
    profile = {profile_name: {"target": "dev", "outputs": {"dev": dbt_profile_from_uri(uri)}}}
    path = os.path.join(profiles_dir, "profiles.yml")
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(profile, handle)
    return path
