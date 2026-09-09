#!/usr/bin/env bash
# Runs every scripts/*.sh self-test.
#
# Release and gate scripts carry logic that nothing else exercises: the tag
# normalizers and FLAKE_RE in cut-release.sh (FLAKE_RE decides whether a red run
# is rerun or stops a release), and the ref comparisons in the check-* gates.
# Each keeps a `self_test()` behind `--self-test` — needing no network and
# running in milliseconds — and for a while nothing ran them, so the coverage
# rotted exactly where it was needed. Two real defects shipped that way: three
# FLAKE_RE alternatives that could never match, and a gate that failed every rc
# cut it was globbed into.
#
# Discovery is by the `self_test()` definition rather than a hand-kept list, so
# a new script's self-test joins this gate by existing — the same property that
# makes cut-release.sh's run_gates() glob scripts/check-*.sh.
#
# run_gates() globs scripts/check-*.sh, so this is also part of the cut's own
# pre-flight.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

found=0 failed=0
for f in "$ROOT"/scripts/*.sh; do
	grep -qE '^self_test\(\)' "$f" || continue
	found=$((found + 1))
	if out="$(bash "$f" --self-test 2>&1)"; then
		echo "OK: $(basename "$f") --self-test"
	else
		printf 'FAIL: %s --self-test\n%s\n' "$(basename "$f")" "$out" >&2
		failed=$((failed + 1))
	fi
done

# Python helpers under scripts/ get the same treatment, by the same
# discovery-by-existence rule. Not every script here is shell, and a report
# generator whose filter silently returns nothing fails exactly as quietly as a
# gate that always passes. Scripts without a self_test are skipped, as above.
for f in "$ROOT"/scripts/*.py; do
	[ -f "$f" ] || continue
	grep -qE '^def self_test\(' "$f" || continue
	found=$((found + 1))
	if out="$(python3 "$f" --self-test 2>&1)"; then
		echo "OK: $(basename "$f") --self-test"
	else
		printf 'FAIL: %s --self-test\n%s\n' "$(basename "$f")" "$out" >&2
		failed=$((failed + 1))
	fi
done

# A rename or a lost self_test() would otherwise shrink this gate to nothing
# while still exiting 0 — the same silent-shrink hole run_gates guards against.
if [ "$found" -lt "${MIN_SELFTESTS:-7}" ]; then
	echo "FAIL: found only $found self-test(s), expected at least ${MIN_SELFTESTS:-7} — did a script lose its self_test()?" >&2
	exit 1
fi
[ "$failed" -eq 0 ] || exit 1
echo "check-script-selftests: $found self-test(s) pass"
