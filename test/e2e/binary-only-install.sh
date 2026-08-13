#!/usr/bin/env bash
# Genuine binary-only install of Leoflow (#587): a built binary run from OUTSIDE
# the repo, with a clean HOME and NO PYTHONPATH — the real "download the release
# and run it" UX. Every other e2e job presets `PYTHONPATH: parser` (repo path),
# which masks the exact parser-resolution path this exercises.
#
# Asserts:
#   1. `leoflow compile` resolves the embedded parser with no PYTHONPATH and no
#      repo on any path (the primary symptom: `No module named leoflow_parser`).
#   2. the binary self-extracts BOTH the parser AND the runtime sources the Lite
#      per-DAG venv is built from — the venv-before-extract ordering precondition
#      (the second symptom: `'<home>/.leoflow/pysrc/runtime/python' does not exist`).
#
# No Postgres/Redis/agent needed: the failure was in resolution + extraction, not
# in any runtime state, so this gate stays fast and deterministic.
set -euo pipefail

fail() { printf '  \033[31mFAIL\033[0m %b\n' "$1"; exit 1; }
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
BINDIR="$(mktemp -d)"
HOME_DIR="$(mktemp -d)"
WORK="$(mktemp -d)"
cleanup() { chmod -R u+w "$HOME_DIR" 2>/dev/null || true; rm -rf "$BINDIR" "$HOME_DIR" "$WORK"; }
trap cleanup EXIT

echo "==> building the leoflow binary (parser + runtime embedded)"
( cd "$REPO" && go build -o "$BINDIR/leoflow" ./cmd/leoflow )

# The genuine binary-only environment: a clean HOME, NO PYTHONPATH, and a working
# directory OUTSIDE the repo so the repo-relative parser/ and runtime/python are
# never on any path. Only the binary's own extraction can satisfy the parser.
unset PYTHONPATH
export HOME="$HOME_DIR"
cd "$WORK"

echo "==> leoflow init + compile (no PYTHONPATH, outside the repo)"
"$BINDIR/leoflow" init proj >/dev/null || fail "leoflow init failed"
"$BINDIR/leoflow" compile proj --output proj/dag.json --image test:v1 \
  >compile.log 2>&1 || fail "leoflow compile failed:\n$(cat compile.log)"
grep -q '"dag_id"' proj/dag.json \
  || fail "compile produced no valid dag.json:\n$(cat compile.log)"
pass "compile resolved the embedded parser without PYTHONPATH"

echo "==> asserting the binary self-extracted parser + runtime sources"
[ -d "$HOME_DIR/.leoflow/pysrc/parser/leoflow_parser" ] \
  || fail "parser sources not extracted under ~/.leoflow/pysrc/parser"
[ -f "$HOME_DIR/.leoflow/pysrc/runtime/python/pyproject.toml" ] \
  || fail "runtime sources not extracted — the Lite venv build would abort with 'does not exist'"
pass "binary extracted both parser and runtime sources for a binary-only install"

echo "ALL PASS"
