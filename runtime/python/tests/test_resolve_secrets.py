"""Tests for the in-pod external-secrets resolver (ADR 0060, 2b)."""

import io
import json
import os
import subprocess
import sys
import textwrap

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


def test_main_stdout_is_pure_json_despite_backend_logging(tmp_path):
    """A real provider backend logs to STDOUT (Airflow's structlog, alembic, the
    secrets masker) while resolving. The agent parses the resolver's stdout as
    strict JSON, so that noise must not reach it. Run the module as a subprocess —
    exactly as the agent does — against a backend that prints to stdout at import,
    init, and every call, and assert stdout is EXACTLY the JSON result and the
    noise went to stderr instead. Guards the "malformed output" failure the pod
    e2e surfaced against a real Secrets Manager backend.
    """
    (tmp_path / "noisy_backend.py").write_text(
        textwrap.dedent(
            '''
            import sys
            print("IMPORT NOISE", file=sys.stdout)

            class _Conn:
                def get_uri(self):
                    return "postgres://w"

            class NoisyBackend:
                def __init__(self, **kwargs):
                    print("INIT NOISE", file=sys.stdout)

                def get_connection(self, name):
                    print("get_connection NOISE", file=sys.stdout)
                    return _Conn() if name == "warehouse" else None

                def get_variable(self, name):
                    print("get_variable NOISE", file=sys.stdout)
                    return "us" if name == "region" else None
            '''
        )
    )
    req = {
        "backend": "noisy_backend.NoisyBackend",
        "backend_kwargs": {},
        "connections": ["warehouse"],
        "variables": ["region"],
    }
    pkg_root = os.path.dirname(os.path.dirname(resolve_secrets.__file__))
    env = dict(os.environ)
    env["PYTHONPATH"] = os.pathsep.join(
        [str(tmp_path), pkg_root, env.get("PYTHONPATH", "")]
    )
    proc = subprocess.run(
        [sys.executable, "-m", "leoflow_runtime.resolve_secrets"],
        input=json.dumps(req),
        capture_output=True,
        text=True,
        env=env,
        check=True,
    )
    result = json.loads(proc.stdout)  # must parse: stdout is JSON and only JSON
    assert result["connections"]["warehouse"] == "postgres://w"
    assert result["variables"]["region"] == "us"
    assert "NOISE" not in proc.stdout, f"log noise leaked onto stdout: {proc.stdout!r}"
    assert "NOISE" in proc.stderr  # the noise was redirected, not lost
