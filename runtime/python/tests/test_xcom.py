"""Tests for XCom input access inside a task container."""

from leoflow_runtime import xcom


def test_xcom_pull_decodes_env(monkeypatch):
    monkeypatch.setenv("LEOFLOW_XCOM_UPSTREAM", '{"n": 1}')
    assert xcom.xcom_pull("upstream") == {"n": 1}


def test_xcom_pull_uppercases_name(monkeypatch):
    monkeypatch.setenv("LEOFLOW_XCOM_MY_INPUT", "[1, 2, 3]")
    assert xcom.xcom_pull("my_input") == [1, 2, 3]


def test_xcom_pull_missing_returns_default(monkeypatch):
    monkeypatch.delenv("LEOFLOW_XCOM_NOPE", raising=False)
    assert xcom.xcom_pull("nope") is None
    assert xcom.xcom_pull("nope", default=42) == 42
