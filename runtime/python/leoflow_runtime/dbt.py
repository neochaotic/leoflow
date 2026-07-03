"""Map a managed Leoflow/Airflow connection URI to a dbt profile (ADR 0043, #2).

Connections are delivered to the task pod as ``AIRFLOW_CONN_<ID>`` (an Airflow
connection URI). A dbt task generates its ``profiles.yml`` from that at runtime,
so no warehouse credential is baked into the image. Supports postgres, snowflake,
bigquery, databricks (the official ``dbt-databricks`` adapter), and duckdb
(embedded, for zero-server local development).
"""

from __future__ import annotations

import json
import os
from urllib.parse import parse_qs, unquote, urlsplit


def _uri_extra(query: str) -> dict:
    """Merge direct query params and the ``__extra__`` JSON blob Leoflow carries in
    the URI into one dict."""
    parsed = {k: v[0] for k, v in parse_qs(query).items()}
    raw = parsed.pop("__extra__", None)
    if raw:
        try:
            extra = json.loads(raw)
            if isinstance(extra, dict):
                parsed.update(extra)
        except (TypeError, ValueError):
            pass
    return parsed


def _eget(extra: dict, conn_type: str, key: str, default=None):
    """Look up an extra key, trying the plain name then Airflow's
    ``extra__<conn_type>__<key>`` form."""
    if key in extra:
        return extra[key]
    return extra.get(f"extra__{conn_type}__{key}", default)


def _threads(extra: dict) -> int:
    return int(extra.get("threads", 4))


def _postgres(parts, _ct, login, password, path, extra):
    return {
        "type": "postgres", "host": parts.hostname or "", "port": parts.port or 5432,
        "user": login, "password": password, "dbname": path,
        "schema": extra.get("schema", "public"), "threads": _threads(extra),
    }


def _snowflake(_parts, ct, login, password, path, extra):
    account = _eget(extra, ct, "account")
    if not account:
        raise ValueError("snowflake connection needs `account` in its extra")
    return {
        "type": "snowflake", "account": account, "user": login, "password": password,
        "role": _eget(extra, ct, "role"), "database": _eget(extra, ct, "database"),
        "warehouse": _eget(extra, ct, "warehouse"),
        "schema": path or _eget(extra, ct, "schema", "PUBLIC"), "threads": _threads(extra),
    }


def _bigquery(_parts, ct, _login, _password, path, extra):
    keyfile = _eget(extra, ct, "keyfile_dict")
    if not keyfile:
        raise ValueError("bigquery connection needs `keyfile_dict` in its extra")
    return {
        "type": "bigquery", "method": "service-account-json",
        "project": _eget(extra, ct, "project"), "dataset": path or _eget(extra, ct, "dataset"),
        "keyfile_json": json.loads(keyfile) if isinstance(keyfile, str) else keyfile,
        "threads": _threads(extra),
    }


def _databricks(parts, ct, _login, password, path, extra):
    http_path = _eget(extra, ct, "http_path")
    if not http_path:
        raise ValueError("databricks connection needs `http_path` in its extra")
    return {
        "type": "databricks", "host": parts.hostname or "", "http_path": http_path,
        "token": password or _eget(extra, ct, "token"), "catalog": _eget(extra, ct, "catalog"),
        "schema": path or _eget(extra, ct, "schema"), "threads": _threads(extra),
    }


def _duckdb(_parts, ct, _login, _password, path, extra):
    # duckdb is an embedded, file-based warehouse — no host or credentials. The DB
    # file comes from the `path` extra or the URI path (empty means in-memory). Ideal
    # for zero-server local development.
    db = _eget(extra, ct, "path") or (("/" + path) if path else ":memory:")
    return {"type": "duckdb", "path": db, "threads": _threads(extra)}


# conn_type -> dbt profile mapper. The official dbt-databricks adapter is used for
# databricks (not the community dbt-spark); duckdb backs zero-server local dev.
_MAPPERS = {
    "postgres": _postgres,
    "postgresql": _postgres,
    "snowflake": _snowflake,
    "google_cloud_platform": _bigquery,
    "databricks": _databricks,
    "duckdb": _duckdb,
}


def dbt_profile_from_uri(uri: str) -> dict:
    """Map an Airflow connection URI to a dbt ``outputs.<target>`` block, dispatching
    by connection type. Supports postgres, snowflake, bigquery
    (``google_cloud_platform``), databricks, and duckdb; any other type is a loud error.
    """
    parts = urlsplit(uri)
    conn_type = parts.scheme.lower().replace("-", "_")
    mapper = _MAPPERS.get(conn_type)
    if mapper is None:
        raise ValueError(
            f"unsupported dbt adapter for connection type {conn_type!r} "
            "(supported: postgres, snowflake, bigquery, databricks, duckdb)"
        )
    extra = _uri_extra(parts.query)
    return mapper(
        parts, conn_type,
        unquote(parts.username or ""), unquote(parts.password or ""),
        unquote(parts.path.lstrip("/")), extra,
    )


def write_dbt_profile(
    conn_id: str, profile_name: str, profiles_dir: str, env=None, schema=None
) -> str:
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
    output = dbt_profile_from_uri(uri)
    if schema:
        output["schema"] = schema  # explicit leoflow.yaml schema wins over the URI/default
    profile = {profile_name: {"target": "dev", "outputs": {"dev": output}}}
    path = os.path.join(profiles_dir, "profiles.yml")
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(profile, handle)
    return path
