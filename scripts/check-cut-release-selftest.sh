#!/usr/bin/env bash
# Runs the release script's own pure-logic self-test.
#
# cut-release.sh carries logic that only ever executes during a cut: the tag
# normalizers, and FLAKE_RE, which decides whether a red run is rerun or stops
# the cut. Nothing else exercises it, so a defect there is discovered at the
# worst possible moment — mid-release. `--self-test` needs no network and runs
# in milliseconds, so there is no reason for it not to be a gate.
#
# run_gates() in cut-release.sh globs scripts/check-*.sh, so this file also
# makes the self-test part of the cut's own pre-flight.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! out="$(bash "$ROOT/scripts/cut-release.sh" --self-test 2>&1)"; then
  printf '%s\n' "$out" >&2
  echo "check-cut-release-selftest: cut-release.sh --self-test FAILED" >&2
  exit 1
fi
echo "check-cut-release-selftest: cut-release.sh --self-test passes"
