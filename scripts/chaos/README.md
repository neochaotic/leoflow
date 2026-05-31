# Chaos dogfood (#231)

The pre-Lima alpha gate. Runs the canonical test suites under host-contract
validation and emits a green/red report. **All green is the alpha bar**; any
red blocks the Lima stress-test build and any tag cut.

## Usage

```bash
# Phase 1 — run on the host (catches contamination, doesn't fix it)
make chaos-dogfood            # default report at /tmp/chaos-report.md
REPORT_FILE=/path/to/out.md \
  make chaos-dogfood          # custom report path

# Phase 2a — run inside a clean python:3.12-slim container
# (clean by construction; no host contamination can leak in)
make chaos-dogfood-docker     # report at ./chaos-report.md (gitignored)
```

Exit codes: `0` all green, `1` any red.

The Dockerized run is the alpha-gate variant — every CI/release-gate
invocation should use it because the host run can produce false greens if
the host happens to be configured the same way the contract expects.

## What's in Phase 1 (here today)

| # | Section | What it checks |
|---|---|---|
| 1 | Fresh-runner contract | `~/.leoflow/` is absent, `leoflow_parser` is NOT pip-installed (catches contributor-machine state — F5/#96) |
| 2 | Go unit tests | `go test ./...` |
| 3 | Go lint | `golangci-lint run ./...` at the CI-pinned version |
| 4 | Parser pytest | `cd parser && pytest` |
| 5 | Runtime pytest | `cd runtime/python && pytest` (skipped if absent) |

## What's in Phase 2a (here today)

- **Docker isolation** — `scripts/chaos/Dockerfile` builds a
  `python:3.12-slim` image with the pinned Go toolchain + golangci-lint +
  the parser-test runtime deps. The harness runs inside it as the
  non-root `chaos` user; the host repo bind-mounts to `/workspace`.
- Image tag includes the Go + golangci-lint versions so a toolchain bump
  busts the cache deterministically.

## What's NOT in Phase 2a (next sub-phases)

- **2b — Failure injection** — kill scheduler mid-tick, kill agent mid-task,
  stop Postgres briefly; assert reapers fire on their declared SLAs
  (`docs/scheduler-resilience.md`).
- **2c — Connector cookbook DAGs** — postgres / mysql / mssql / sqlite /
  redis / http round-trips end-to-end inside the harness.
- **2d — Recurring DAGs** — a `*/3 * * * *` print DAG + a DB-writer DAG
  running for ≥30 min so the scheduler tick is exercised under steady load.

## Why this exists

The risk Phase 1 catches: a contributor runs the full suite locally, every
test passes, but the build CI runner finds the bug. The maintainer machine
had pip-installed `leoflow_parser` so `leoflow compile` "just worked"
without `leoflow setup` — the gap was only caught by reading code, not by
running tests (PR #221 self-review). The harness makes "did you actually
run on a clean env?" a checkable question, not a vibe.

The risk Phase 2 will catch: silent regressions in the reaper SLAs and the
recovery contract — easy to introduce by accident, hard to detect without
intentional failure injection.

References: #231 (this issue), #96 (CI install-smoke fresh-runner contract),
PR #221 (the gap that motivated this), `docs/scheduler-resilience.md`.
