#!/usr/bin/env bash
# Every github/codeql-action/* step must be pinned to the SAME commit.
#
# CodeQL refuses to run when they disagree. `init` writes a configuration file
# stamped with its own version, and `autobuild` and `analyze` read it back:
#
#   We were unable to automatically build your code. [...]
#   Loaded a configuration file for version '4.38.1', but running version '4.35.5'
#
# Dependabot cannot prevent this, and reliably causes it. It treats
# `github/codeql-action/init`, `/autobuild`, `/analyze` and `/upload-sarif` as
# four independent dependencies and opens a pull request per sub-action, so the
# normal state during a bump is a workflow whose steps disagree. #1135 was
# exactly that, and the repository had ALREADY been sitting at two versions
# before it: init/autobuild/analyze on v4.35.5 and three upload-sarif on
# v4.37.9, which happened to work only because upload-sarif does not read the
# file init writes.
#
# The failure lands in CodeQL, which reads as a code-scanning problem rather
# than as a pin problem, and the remedy the message suggests (replace autobuild
# with custom build steps) is the wrong one.
#
# Usage:
#   scripts/check-codeql-action-pins.sh
#   scripts/check-codeql-action-pins.sh --self-test
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# pins <dir>: print "<file> <sha> <version-comment>" for every codeql-action
# step found under dir. A pure filter over files, so the self-test can drive it
# with fixtures instead of the real workflows.
pins() { # <dir>
	grep -rhoE 'github/codeql-action/[a-z-]+@[0-9a-f]{40} # v[0-9][0-9.]*' "$1" 2>/dev/null |
		sed -E 's|github/codeql-action/[a-z-]+@||'
}

self_test() {
	local fail=0
	_eq() { if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"
		else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi; }
	local tmp; tmp="$(mktemp -d)"; trap 'rm -rf "${tmp:-}"' RETURN

	# Agreeing pins across several files and several sub-actions.
	printf '%s\n' '      - uses: github/codeql-action/init@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa # v4.38.1' \
		'      - uses: github/codeql-action/analyze@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa # v4.38.1' > "$tmp/a.yaml"
	printf '%s\n' '        uses: github/codeql-action/upload-sarif@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa # v4.38.1' > "$tmp/b.yaml"
	_eq "$(pins "$tmp" | sort -u | wc -l | tr -d ' ')" "1" "agreeing pins count as one"

	# The #1135 shape: init bumped, the rest left behind.
	printf '%s\n' '      - uses: github/codeql-action/autobuild@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb # v4.35.5' > "$tmp/c.yaml"
	_eq "$(pins "$tmp" | sort -u | wc -l | tr -d ' ')" "2" "a single stale pin is detected"

	# A directory with no codeql-action at all must not look like agreement.
	local empty; empty="$(mktemp -d)"
	printf '%s\n' '      - uses: actions/checkout@cccccccccccccccccccccccccccccccccccccccc # v5' > "$empty/d.yaml"
	_eq "$(pins "$empty" | wc -l | tr -d ' ')" "0" "no codeql-action is not agreement"
	rm -rf "$empty"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

found="$(pins "$ROOT/.github/workflows" || true)"
if [ -z "$found" ]; then
	# Not "OK": a gate that stops finding its subject after a rename still
	# reports green, which is worse than no gate.
	echo "FAIL: no github/codeql-action/* pin found under .github/workflows — did the workflows move, or the pin comment format change?"
	exit 1
fi

distinct="$(printf '%s\n' "$found" | sort -u)"
if [ "$(printf '%s\n' "$distinct" | wc -l | tr -d ' ')" -gt 1 ]; then
	echo "FAIL: github/codeql-action/* steps are pinned to more than one commit."
	echo
	printf '%s\n' "$distinct" | sed 's/^/    /'
	echo
	echo "CodeQL refuses to run when they disagree: init writes a config stamped"
	echo "with its version and autobuild/analyze read it back, so the run dies with"
	echo "\"Loaded a configuration file for version 'X', but running version 'Y'\"."
	echo
	echo "Dependabot opens one pull request per sub-action, so this is the normal"
	echo "state during a bump. Bring every step to the same commit in one commit."
	exit 1
fi

echo "OK: all github/codeql-action/* steps pinned to $(printf '%s\n' "$distinct")"
