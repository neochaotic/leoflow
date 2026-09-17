#!/usr/bin/env bash
# The Playwright browser version is pinned in two places that nothing reconciles:
#
#   .github/workflows/ci.yaml   PLAYWRIGHT_VERSION   what CI installs, and the cache key
#   package.json                playwright-core      what a local `make e2e-*` run resolves
#
# CI installs with --no-save at the pinned version, so a range in package.json
# never breaks CI. It breaks the other direction: a developer running the same
# e2e locally drives a different Chromium than the one the gate runs, and a
# browser-behavior failure reproduces on exactly one of the two machines. That is
# the worst shape a browser test can take, because the disagreement looks like
# flakiness rather than a version difference.
#
# The ci.yaml pin is deliberate and documented at length there (a constant cache
# key would freeze on today's chromium revision). This gate makes the second copy
# follow it, exactly, with no range operator: ^ or ~ would re-open the drift on
# the next npm publish rather than at the next edit.
#
# Usage: scripts/check-playwright-pin.sh [--self-test]
#        scripts/check-playwright-pin.sh <ci.yaml> <package.json>
set -euo pipefail

check() { # <ci.yaml> <package.json>
	python3 - "$1" "$2" <<'PY'
import json
import re
import sys

ci_path, pkg_path = sys.argv[1], sys.argv[2]


def fail(msg):
	sys.exit("FAIL: " + msg)


try:
	ci = open(ci_path).read()
except OSError as exc:
	fail(f"cannot read {ci_path}: {exc}")

pins = set(re.findall(r'^\s*PLAYWRIGHT_VERSION:\s*"([^"]+)"\s*$', ci, re.M))
if not pins:
	fail(f'{ci_path} has no `PLAYWRIGHT_VERSION: "..."`: renamed, or the pin was dropped')
if len(pins) > 1:
	fail(f"{ci_path} pins more than one Playwright version ({', '.join(sorted(pins))}); the browser cache key is shared, so they must agree")
pin = pins.pop()

try:
	pkg = json.load(open(pkg_path))
except (OSError, ValueError) as exc:
	fail(f"cannot read {pkg_path}: {exc}")

deps = {}
for section in ("dependencies", "devDependencies"):
	deps.update(pkg.get(section) or {})
got = deps.get("playwright-core")
if got is None:
	fail(f"{pkg_path} does not depend on playwright-core; the e2e scripts import it")
if got != pin:
	fail(
		f'{pkg_path} wants playwright-core "{got}" while {ci_path} pins "{pin}". A local e2e run then drives a '
		f"different Chromium than the gate does, so a browser-behavior failure reproduces on one machine and not "
		f'the other. Set it to the exact version, with no range operator: "{pin}".'
	)
print(f"playwright pin: {pkg_path} and {ci_path} agree on {pin}")
PY
}

self_test() {
	local fail=0 tmp
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN

	_case() { # <name> <want-exit> <want-substr> <ci-body> <pkg-body>
		local name=$1 want=$2 substr=$3
		printf '%s\n' "$4" > "$tmp/ci.yaml"
		printf '%s\n' "$5" > "$tmp/package.json"
		local out rc
		out=$(check "$tmp/ci.yaml" "$tmp/package.json" 2>&1) && rc=0 || rc=$?
		if [ "$rc" -ne "$want" ]; then
			echo "self-test FAIL: $name: exit $rc, wanted $want"; echo "  $out"; fail=1; return
		fi
		case "$out" in *"$substr"*) ;; *) echo "self-test FAIL: $name: output lacks \"$substr\""; echo "  $out"; fail=1 ;; esac
	}

	local ci='env:
      PLAYWRIGHT_VERSION: "1.63.0"'
	local ci_two='env:
      PLAYWRIGHT_VERSION: "1.63.0"
      PLAYWRIGHT_VERSION: "1.64.0"'

	_case "an exact match passes"          0 "agree on 1.63.0" "$ci"     '{"dependencies":{"playwright-core":"1.63.0"}}'
	_case "a caret range is caught"        1 "no range operator" "$ci"   '{"dependencies":{"playwright-core":"^1.63.0"}}'
	_case "a different version is caught"  1 "no range operator" "$ci"   '{"dependencies":{"playwright-core":"1.62.0"}}'
	_case "devDependencies count too"      0 "agree on 1.63.0" "$ci"     '{"devDependencies":{"playwright-core":"1.63.0"}}'
	_case "a missing dependency is caught" 1 "does not depend on"  "$ci" '{"dependencies":{}}'
	_case "a dropped ci pin is caught"     1 "no \`PLAYWRIGHT_VERSION" 'env:' '{"dependencies":{"playwright-core":"1.63.0"}}'
	_case "two disagreeing ci pins caught" 1 "more than one"  "$ci_two"  '{"dependencies":{"playwright-core":"1.63.0"}}'

	if [ "$fail" -eq 0 ]; then echo "check-playwright-pin self-test: ok"; return 0; fi
	return 1
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-.github/workflows/ci.yaml}" "${2:-package.json}"
