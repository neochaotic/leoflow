# Local deploy (the dev loop)

The fastest way to validate a change end-to-end against `leoflow lite` —
without going through `git tag`, the release pipeline, install-smoke, or
`curl | sh`. The script is `scripts/lite-redeploy.sh`, wired as a
Makefile target.

## When to use

- Validating a Go change in the control plane, the agent, or the CLI
  against a real `leoflow lite` boot.
- Reproducing a runtime bug a user reported, without round-tripping
  through the release machinery.
- Smoke-testing a fix BEFORE cutting a tag — keeps prealpha tags clean.

This is **not** an install path. Real users still install via
`install.sh` from a published release (see `docs/installation.md`).

## What it does

1. **Builds** `leoflow`, `leoflow-server`, and `leoflow-agent` from the
   current working tree (no `git tag` needed).
2. **Ad-hoc code-signs** the binaries on macOS so the OS does not
   SIGKILL them at exec (Sequoia 14+ refuses to run an unsigned binary
   that carries `com.apple.provenance` — silent failure mode with
   exit 137 and no log output).
3. **Stops** any running `leoflow lite` (via the script's pidfile, with
   a `pkill -f "leoflow lite"` fallback).
4. **Swaps the binaries in both locations** `leoflow lite` resolves
   them from — `./bin/` (the repo's local bin, preferred by
   `resolveBinary` in `internal/cli/dev.go`) and `~/.leoflow/bin/` (the
   user-install location). Keeping them in lockstep is critical: if
   only one is updated the stale one silently runs and the dev loop
   becomes confusing fast (this happened — see the commit that added
   the script).
5. **Starts** `leoflow lite --postgres managed` (a docker-free local
   Postgres on a Unix socket, so the loop does not collide with any
   `postgres:16` you already have on 5432).
6. **Polls** `/readyz` and reports the boot URL + PID + log path.

## Usage

```sh
make lite-redeploy            # rebuild, restart, tail-ready
PORT=9090 make lite-redeploy  # custom HTTP port
LOG_LEVEL=debug make lite-redeploy   # verbose
```

After it returns:

```sh
# log tail (boot lines, http requests, executor activity)
tail -f /tmp/leoflow-lite.log

# get / rotate the admin password
~/.leoflow/bin/leoflow lite reset-password

# stop
kill "$(cat /tmp/leoflow-lite.pid)"
```

## The two-binary trap (why this script exists)

`leoflow lite` is a thin orchestrator: it spawns `leoflow-server` as a
subprocess. The Subprocess executor (the dev-only path that runs your
Python tasks) in turn spawns `leoflow-agent`. So a single `leoflow lite`
process tree uses **all three binaries**:

```
leoflow lite (CLI / orchestrator)
└── leoflow-server (control plane HTTP + gRPC + scheduler)
    └── leoflow-agent (per-task subprocess; runs the user's dag.py)
```

If you rebuild only `leoflow` (the CLI) but leave a stale `leoflow-server`
behind, lite still boots — but the control plane that actually handles
requests is the OLD code. Symptom: your code change "doesn't show up"
and you waste 30 minutes second-guessing the test. The script avoids
this by rebuilding and swapping all three on every invocation.

## How this fits the GitOps flow

| Layer | Tool | Triggered by | Validates |
|---|---|---|---|
| **Local dev loop** | `make lite-redeploy` | manual `make` | a change runs against a real `leoflow lite` |
| **PR CI** | `.github/workflows/ci.yaml` | `pull_request` | unit + integration + lint + e2e on the PR HEAD |
| **Release CI** | `.github/workflows/release.yaml` | `push: tags: v*` | the published binaries install, boot, and upgrade cleanly across 7 distros |

The local loop is the **inner ring** — it catches "does my change boot
at all" in seconds, before you push. The release smoke jobs are
designed for stable releases; they retract a published tag to a draft
on any failure. Using the local loop first keeps prealpha tags clean
and avoids the retract / re-cut churn.

If you find yourself cutting a prealpha purely to test a Lite change,
that is a smell — `make lite-redeploy` will give you the same
validation in seconds without burning a tag number.
