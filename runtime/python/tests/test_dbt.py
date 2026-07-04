"""Tests for the managed-connection -> dbt profile mapping (ADR 0043, #2)."""
from __future__ import annotations

import pytest

import json
from urllib.parse import urlencode

from leoflow_runtime.dbt import dbt_profile_from_uri, write_dbt_profile


def _conn_uri(scheme, login="", password="", host="", port=None, schema="", extra=None):
    """Build an Airflow connection URI the way Leoflow delivers it (conn_type with
    _->-, extra as a single __extra__ JSON query param)."""
    netloc = ""
    if login or password:
        netloc += f"{login}:{password}@"
    netloc += host + (f":{port}" if port else "")
    path = f"/{schema}" if schema else ""
    query = "?" + urlencode({"__extra__": json.dumps(extra)}) if extra else ""
    return f"{scheme.replace('_', '-')}://{netloc}{path}{query}"


def test_postgres_uri_maps_to_dbt_profile():
    uri = "postgres://user:pass@db.example.com:5432/warehouse?schema=analytics&threads=8"
    assert dbt_profile_from_uri(uri) == {
        "type": "postgres",
        "host": "db.example.com",
        "port": 5432,
        "user": "user",
        "password": "pass",
        "dbname": "warehouse",
        "schema": "analytics",
        "threads": 8,
    }


def test_postgres_defaults_port_schema_threads():
    out = dbt_profile_from_uri("postgresql://u:p@h/db")
    assert out["type"] == "postgres"
    assert out["port"] == 5432
    assert out["dbname"] == "db"
    assert out["schema"] == "public"
    assert out["threads"] == 4


def test_url_encoded_credentials_are_decoded():
    out = dbt_profile_from_uri("postgres://u%40corp:p%3Aword@h:5432/db")
    assert out["user"] == "u@corp"
    assert out["password"] == "p:word"


def test_duckdb_uri_maps_to_dbt_profile():
    # duckdb is embedded/file-based — the URI path is the DB file (for local dev).
    out = dbt_profile_from_uri("duckdb:///tmp/wh.duckdb?threads=2")
    assert out == {"type": "duckdb", "path": "/tmp/wh.duckdb", "threads": 2}


def test_duckdb_defaults_to_in_memory():
    out = dbt_profile_from_uri("duckdb://")
    assert out["type"] == "duckdb"
    assert out["path"] == ":memory:"
    assert out["threads"] == 4


def test_default_duckdb_profile_is_written(tmp_path):
    from leoflow_runtime.dbt import write_dbt_default_duckdb

    db = str(tmp_path / "wh.duckdb")
    path = write_dbt_default_duckdb("shop", str(tmp_path), db)
    prof = json.load(open(path))
    assert prof == {"shop": {"target": "dev", "outputs": {"dev": {
        "type": "duckdb", "path": db, "threads": 4}}}}


def test_default_duckdb_defaults_to_memory(tmp_path):
    from leoflow_runtime.dbt import write_dbt_default_duckdb

    path = write_dbt_default_duckdb("shop", str(tmp_path), "")
    prof = json.load(open(path))
    assert prof["shop"]["outputs"]["dev"]["path"] == ":memory:"


def test_profile_step_writes_to_cwd_not_home(tmp_path, monkeypatch):
    # Regression: the profile-generation step must write to the task's CWD, never
    # clobber the user's global ~/.dbt/profiles.yml (Lite runs on the host).
    from leoflow_runtime.__main__ import main

    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("DBT_PROFILES_DIR", raising=False)
    assert main(["--dbt-default-duckdb", "shop"]) == 0
    assert (tmp_path / "profiles.yml").exists()


def test_unsupported_adapter_is_a_loud_error():
    with pytest.raises(ValueError):
        dbt_profile_from_uri("mysql://u:p@h/db")


def test_snowflake_uri_maps_to_dbt_profile():
    uri = _conn_uri("snowflake", login="user", password="pass", schema="analytics",
                    extra={"account": "ab12345", "warehouse": "WH", "database": "DB", "role": "TRANSFORMER"})
    assert dbt_profile_from_uri(uri) == {
        "type": "snowflake", "account": "ab12345", "user": "user", "password": "pass",
        "role": "TRANSFORMER", "database": "DB", "warehouse": "WH", "schema": "analytics", "threads": 4,
    }


def test_bigquery_uri_maps_to_dbt_profile():
    keyfile = '{"type": "service_account", "project_id": "p"}'
    uri = _conn_uri("google_cloud_platform", schema="my_dataset",
                    extra={"project": "my-proj", "keyfile_dict": keyfile})
    out = dbt_profile_from_uri(uri)
    assert out["type"] == "bigquery"
    assert out["method"] == "service-account-json"
    assert out["project"] == "my-proj"
    assert out["dataset"] == "my_dataset"
    assert out["keyfile_json"] == {"type": "service_account", "project_id": "p"}


def test_databricks_uri_maps_to_dbt_profile():
    uri = _conn_uri("databricks", password="dapitoken", host="dbc.databricks.com", schema="analytics",
                    extra={"http_path": "/sql/1.0/warehouses/abc", "catalog": "main"})
    assert dbt_profile_from_uri(uri) == {
        "type": "databricks", "host": "dbc.databricks.com", "http_path": "/sql/1.0/warehouses/abc",
        "token": "dapitoken", "catalog": "main", "schema": "analytics", "threads": 4,
    }


def test_airflow_prefixed_extra_keys_are_accepted():
    # Older Airflow exports extra as extra__<conn_type>__<key>; accept both forms.
    uri = _conn_uri("databricks", password="t", host="h",
                    extra={"extra__databricks__http_path": "/sql/x"})
    assert dbt_profile_from_uri(uri)["http_path"] == "/sql/x"


def test_write_dbt_profile_from_env(tmp_path, monkeypatch):
    monkeypatch.setenv("AIRFLOW_CONN_WAREHOUSE_PG", "postgres://u:p@h:5432/wh?schema=analytics")
    path = write_dbt_profile("warehouse_pg", "analytics", str(tmp_path))
    # JSON is valid YAML, so dbt reads what we write; assert the structure
    data = json.loads((tmp_path / "profiles.yml").read_text())
    assert path.endswith("profiles.yml")
    assert data["analytics"]["target"] == "dev"
    out = data["analytics"]["outputs"]["dev"]
    assert out["type"] == "postgres"
    assert out["dbname"] == "wh"
    assert out["schema"] == "analytics"


def test_write_dbt_profile_missing_connection_is_loud(tmp_path):
    with pytest.raises(RuntimeError):
        write_dbt_profile("absent", "p", str(tmp_path), env={})


def test_write_dbt_profile_schema_override(tmp_path, monkeypatch):
    # the connection has no schema, which would default to public; an explicit
    # schema (from leoflow.yaml) wins so models land where the team expects.
    monkeypatch.setenv("AIRFLOW_CONN_WH", "postgres://u:p@h/db")
    write_dbt_profile("wh", "p", str(tmp_path), schema="marts")
    data = json.loads((tmp_path / "profiles.yml").read_text())
    assert data["p"]["outputs"]["dev"]["schema"] == "marts"
