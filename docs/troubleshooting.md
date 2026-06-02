# Troubleshooting & observability

Symptoms grouped by where they surface. Start with the diagnostics — most
issues are one `leoflow doctor` away from a clear cause.

## First things to run

```bash
leoflow doctor                          # host check (OS, python, docker, k3d, kubectl, recommended tier)
leoflow version                         # version + commit + expiry (pre-alpha builds expire)
tail -f /tmp/leoflow-lite.log           # the live boot log when you ran `leoflow lite` via lite-redeploy
journalctl -u leoflow-server -f         # Pro / systemd hosts
```

## Install & setup

| Symptom | Cause / fix |
|---|---|
| `command not found: leoflow` | The binary is not on `PATH` — re-run `curl … \| sh`, or open a fresh shell to pick up the install-script's PATH line. Building from source? `go install .../cmd/leoflow@latest` and add `$(go env GOPATH)/bin` to `PATH`. |
| `leoflow version` shows `[expires …]` and refuses to run | Pre-alpha builds carry a baked-in ~90-day expiry. Re-run the install command for a fresh build. `LEOFLOW_IGNORE_EXPIRY=1` overrides in a pinch. |
| `leoflow setup` says "python: none on PATH" but you have `python3.12` | Older Leoflow versions only matched literal `python3.11`. Update to the latest pre-alpha — `setup` now accepts any `python3.11`+ that's on `PATH`. |
| Install on Alpine / musl fails fetching CPython | The musl-libc relocatable CPython build can be missing system libs. `leoflow lite --postgres docker` falls back to the Docker Postgres path instead of the embedded managed one. |

## `leoflow lite` boot

| Symptom | Cause / fix |
|---|---|
| `error: duplicate dag_id in workspace — rename one of the colliding projects` | The workspace has two project directories declaring the same `dag_id` — the most common cause is clicking the IDE's "Download examples" while a same-named project already exists at the workspace root. Delete or rename one of the two copies, then re-run `leoflow lite`. Recent builds skip the example when a collision is detected (#298). |
| `provision incomplete: dev database` | The managed Postgres did not start. End-users run `leoflow setup` to bootstrap the managed runtime. Contributors on a source checkout use `leoflow lite provision`. If Docker is the chosen backend, confirm the daemon is up. |
| Pro refuses to boot with `LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true` set | The Pro edition rejects this flag at boot (it would expose plaintext secrets). Unset it for Pro deployments; it stays valid for Lite where the agent talks loopback gRPC without TLS by design. |
| `jwt_secret is empty; falling back to the dev-only constant` | First boot before `leoflow setup` has run, or `LEOFLOW_SECRET_KEY` not set. Run `leoflow setup` — it provisions a per-install secret. Not fatal on Lite (the constant works), but rotate before sharing the install. |
| Permission denied on `/tmp/leoflow-*` | Older Lite versions shared `/tmp/leoflow*` paths across users on multi-user hosts. Update to the latest pre-alpha — paths are now per-user. |

## Running a DAG

| Symptom | Cause / fix |
|---|---|
| `leoflow compile` dumps a Python traceback with internal parser paths first | Recent builds lead the failure with the user-facing line (e.g. `SyntaxError: ...`) and put the parser paths in the bounded tail. If you still see the internal-first dump, you are on an older pre-alpha — update. |
| `leoflow compile` rejects a sensor / Jinja template / branching operator | This is **intentional** — Leoflow accepts a closed set of three task types (`python`, `bash`, `http_api`). See [DAG authoring → Not supported](dag-authoring.md#not-supported--leoflow-compile-rejects-these) for the full list and workarounds (`@task` + poll loop for sensors; build values from `airflow.sdk` context for Jinja). |
| `Compiled .../dag.py -> dag.json (image , version dev)` (dangling comma) | Older build — update. Recent versions render `(no image, version dev)` when `--image` is unset. |
| Task pod `ErrImagePull` (cluster mode) | The DAG's image is not in the cluster — rebuild + import. Cluster-mode rebuilds on save; for a manual push, `leoflow compile --build --push`. |
| Run stuck at `queued` (subprocess) | The agent must reach the control plane — Lite uses `127.0.0.1:<grpc>`. The executor launches async and the agent reports state back. Look for the agent process in `ps`; if it exited, check `/tmp/leoflow-lite.log` for the launch error. |
| Run stuck at `running` long after the task finished | The agent's heartbeat reaper picks these up after the configured window. Check `LEOFLOW_TI_HEARTBEAT_TIMEOUT_SECONDS` and look for a `reaped` log line. |

## UI / browser

| Symptom | Cause / fix |
|---|---|
| `Invalid credentials` on the login page even with the right password | Disable autofill or type the password manually — some browsers append a trailing space. Usernames are trimmed, passwords are not (per security best practice). |
| Login rate-limits you out after a few typos | Older builds counted *every* attempt against a 5/min cap; the fix splits successful and failed attempts so a typo does not block recovery. Update to the latest pre-alpha. |
| No **Lite** badge on `http://localhost:8088` | You are likely on the **Demo** (production-shaped reference, port `8080`) — Lite runs on `8088` with a silver `Leoflow Lite` badge. See [operating modes](operating-modes.md). |
| Copy-logs button silently fails over `http://<lan-ip>:8088` | The Clipboard API requires a secure context, so plain HTTP origins (LAN access from another machine) used to break copy. Recent builds inject a polyfill (`document.execCommand('copy')` fallback) — update. |
| Task state badge does not refresh after "Mark as failed/success" | Known upstream Airflow bug — see [apache/airflow#67883](https://github.com/apache/airflow/issues/67883). The server-side mutation persists correctly; the SPA cache update is the gap. Hard-refresh the page (Cmd+Shift+R) to see the new state. |
| Browser tab title shows "Airflow" not "Leoflow Lite" | Old build; the SPA shell rewrites the `<title>` to the configured instance name at request time. Update to the latest pre-alpha. |

## Reset paths (when in doubt)

```bash
leoflow lite reset-password --user admin@leoflow.local  # generate a fresh admin password (no sudo)
leoflow db reset --yes                                  # drop + recreate the Lite database (DESTRUCTIVE)
leoflow uninstall                                       # remove ~/.leoflow (binaries, managed Python, config)
leoflow uninstall --purge                               # also remove the workspace (your DAGs!)
```

## Logs

Task logs stream from the agent over gRPC to the control plane's log sink and
are served at
`/api/v2/dags/<dag>/dagRuns/<run>/taskInstances/<task>/logs/<try>` (the UI's
drill-down). The sink directory is `LEOFLOW_LOGS_DIR` (must be writable;
`leoflow lite` points it at a temp dir).

Control-plane logs are structured `slog` (JSON by default), one line per HTTP
request with a request id — `grep <request_id>` correlates a UI click to its
backend trace.

## Observability

- **Metrics:** Prometheus at `:9090/metrics` (scheduler, dispatch, inline
  runner, undispatchable counters; ADR 0007 has the catalogue).
- **Tracing:** OpenTelemetry — set `LEOFLOW_OBSERVABILITY_OTEL_ENABLED=true`
  and `…_OTEL_ENDPOINT`.
- **Logs:** structured `slog` (JSON by default), one line per HTTP request
  with a request id.

Observability ships from the first commit (it is not optional).
