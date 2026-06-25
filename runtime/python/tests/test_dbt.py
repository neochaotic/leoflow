"""Tests for the managed-connection -> dbt profile mapping (ADR 0043, #2)."""
from __future__ import annotations

import pytest

import json

from leoflow_runtime.dbt import dbt_profile_from_uri, write_dbt_profile


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


def test_unsupported_adapter_is_a_loud_error():
    # snowflake/bigquery are follow-ons (#2 ships postgres first).
    with pytest.raises(ValueError):
        dbt_profile_from_uri("snowflake://u:p@acct/db")


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
