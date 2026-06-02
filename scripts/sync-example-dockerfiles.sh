#!/usr/bin/env bash
# sync-example-dockerfiles.sh — keep each examples/<dag>/Dockerfile aligned with
# its leoflow.yaml (#318). Idempotent: re-running on an in-sync tree is a no-op.
#
# The generated Dockerfile follows the same template `leoflow lite` uses
# in-process (internal/cli/dev.go devDockerfile): FROM the matching task base,
# pip install declared dependencies, COPY the DAG source, set PYTHONPATH.
# Operators copying these into a Pro deploy walkthrough get the same shape
# regardless of which executor they came from.
#
# Usage:
#   scripts/sync-example-dockerfiles.sh                # write/overwrite all
#   scripts/sync-example-dockerfiles.sh --check        # CI mode: exit non-zero on drift
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mode="${1:-write}"
[ "$mode" = "--check" ] && mode="check"

gen_dockerfile() {
  local yaml="$1"
  local py
  py="$(awk -F'[ "]+' '/^python_version:/ {print $2; exit}' "$yaml")"
  py="${py:-3.11}"

  # Collect declared pip deps. Each line under `dependencies:` looks like
  # `  - package==version`; we take everything after the leading `- `.
  local deps_args=""
  if awk '/^dependencies:/ {found=1; next} found && /^[^ ]/ {exit} found && /- / {sub(/^[ ]*-[ ]*/, "", $0); print}' "$yaml" \
       | grep -q .; then
    while IFS= read -r dep; do
      deps_args+=" \"$dep\""
    done < <(awk '/^dependencies:/ {found=1; next} found && /^[^ ]/ {exit} found && /- / {sub(/^[ ]*-[ ]*/, "", $0); print}' "$yaml")
  fi

  cat <<EOF
# Standard DAG image (#318). Built by 'leoflow compile --build' or by hand:
#   docker build -t my-registry/$(basename "$(dirname "$yaml")"):<tag> .
# Synthesized by scripts/sync-example-dockerfiles.sh from leoflow.yaml; do
# not hand-edit — re-run the script after changing python_version or
# dependencies, or CI's drift check fails.
FROM leoflow-base:py${py}
EOF
  if [ -n "$deps_args" ]; then
    printf 'RUN pip install --no-cache-dir%s\n' "$deps_args"
  fi
  cat <<EOF
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
EOF
}

drift=0
for yaml in examples/*/leoflow.yaml; do
  dir="$(dirname "$yaml")"
  dockerfile="$dir/Dockerfile"
  expected="$(gen_dockerfile "$yaml")"
  if [ "$mode" = "check" ]; then
    actual="$(cat "$dockerfile" 2>/dev/null || true)"
    if [ "$actual" != "$expected" ]; then
      echo "::error::$(realpath --relative-to=. "$dockerfile") is out of sync with leoflow.yaml" >&2
      drift=1
    fi
  else
    printf '%s\n' "$expected" > "$dockerfile"
  fi
done

if [ "$mode" = "check" ] && [ "$drift" -ne 0 ]; then
  echo "" >&2
  echo "Fix: run scripts/sync-example-dockerfiles.sh and commit the result." >&2
  exit 1
fi
