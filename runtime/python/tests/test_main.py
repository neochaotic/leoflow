"""Tests for the ``python -m leoflow_runtime`` CLI entry point."""

import json
import os

from leoflow_runtime import __main__


def test_dbt_profiles_dir_never_cwd(tmp_path, monkeypatch):
    """#882: with DBT_PROFILES_DIR unset, the profiles dir must NOT be the CWD (the
    dbt project in the user's working tree) — profiles.yml carries the secret and
    would clobber the repo's versioned file. It must be a private (0700) scratch."""
    monkeypatch.delenv("DBT_PROFILES_DIR", raising=False)
    monkeypatch.chdir(tmp_path)  # simulate running from the dbt project dir
    d = __main__._dbt_profiles_dir()
    assert d != str(tmp_path), "must not resolve to the project CWD (#882)"
    assert os.path.isdir(d)
    assert (os.stat(d).st_mode & 0o777) == 0o700, "scratch must be private (0700)"


def test_dbt_profiles_dir_honors_env(tmp_path, monkeypatch):
    target = tmp_path / "scr"
    monkeypatch.setenv("DBT_PROFILES_DIR", str(target))
    assert __main__._dbt_profiles_dir() == str(target)
    assert target.is_dir()


def test_main_requires_exactly_one_arg():
    assert __main__.main([]) == 2
    assert __main__.main(["a", "b"]) == 2


def test_main_runs_entrypoint(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    (tmp_path / "climod.py").write_text("def task():\n    return 'ok'\n")
    monkeypatch.syspath_prepend(str(tmp_path))

    assert __main__.main(["climod:task"]) == 0
    assert json.loads(out.read_text()) == "ok"


def test_main_bash_dispatch(monkeypatch):
    # --bash routes to run_bash (which renders + execs bash in place; mocked here).
    calls = {}
    monkeypatch.setattr(__main__, "run_bash", lambda cmd: calls.update(cmd=cmd))

    assert __main__.main(["--bash", "echo {{ ds }}"]) == 0
    assert calls == {"cmd": "echo {{ ds }}"}


def test_main_dbt_profile_dispatch(tmp_path, monkeypatch):
    # --dbt-profile <conn> <profile> writes profiles.yml from the delivered connection.
    monkeypatch.setenv("AIRFLOW_CONN_WAREHOUSE_PG", "postgres://u:p@h:5432/wh?schema=analytics")
    monkeypatch.setenv("DBT_PROFILES_DIR", str(tmp_path))

    assert __main__.main(["--dbt-profile", "warehouse_pg", "analytics"]) == 0
    data = json.loads((tmp_path / "profiles.yml").read_text())
    assert data["analytics"]["outputs"]["dev"]["type"] == "postgres"
