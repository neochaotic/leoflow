"""Tests for the in-pod external-secrets resolver (ADR 0060, 2b)."""

import io
import json

import pytest

from leoflow_runtime import resolve_secrets


class FakeConn:
    def __init__(self, uri):
        self._uri = uri

    def get_uri(self):
        return self._uri


class FakeBackend:
    """Stands in for an Airflow provider secrets backend."""

    def __init__(self, conns=None, vars=None):
        self._conns = conns or {}
        self._vars = vars or {}

    def get_connection(self, name):
        return self._conns.get(name)

    def get_variable(self, name):
        return self._vars.get(name)


def test_resolve_renders_conn_uri_and_variable():
    backend = FakeBackend(conns={"warehouse": FakeConn("postgres://w")}, vars={"region": "us"})
    out = resolve_secrets.resolve(backend, ["warehouse"], ["region"])
    assert out["connections"]["warehouse"] == "postgres://w"
    assert out["variables"]["region"] == "us"


def test_resolve_omits_misses():
    backend = FakeBackend(conns={}, vars={})
    out = resolve_secrets.resolve(backend, ["absent_conn"], ["absent_var"])
    assert out == {"connections": {}, "variables": {}}


def test_resolve_hard_error_propagates():
    class Boom:
        def get_connection(self, name):
            raise RuntimeError("AccessDenied")

        def get_variable(self, name):
            return None

    with pytest.raises(RuntimeError):
        resolve_secrets.resolve(Boom(), ["x"], [])


def test_load_backend_rejects_bare_name():
    with pytest.raises(ValueError):
        resolve_secrets.load_backend("NotDotted", {})


def test_main_round_trip(monkeypatch):
    # Point the resolver at a fake backend class defined here.
    monkeypatch.setattr(
        resolve_secrets,
        "load_backend",
        lambda cls, kwargs: FakeBackend(conns={"db": FakeConn("mysql://d")}, vars={}),
    )
    req = {"backend": "x.Y", "backend_kwargs": {}, "connections": ["db"], "variables": []}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(req)))
    out = io.StringIO()
    monkeypatch.setattr("sys.stdout", out)
    assert resolve_secrets.main() == 0
    assert json.loads(out.getvalue())["connections"]["db"] == "mysql://d"
