#!/usr/bin/env bash
# Fail a PR that changes shipped behavior without recording it in the CHANGELOG.
#
# Leoflow keeps a hand-written Keep-a-Changelog `CHANGELOG.md`; entries are added
# manually under `## [Unreleased]` and `cut-release.sh` dates that section at a GA.
# Nothing forced an entry per PR, so features repeatedly merged with none and had
# to be back-filled by separate "docs(changelog): record …" PRs (#529, #532, #708,
# #739, #756, #780, #781, #888 — at least eight). Each wastes a cycle and risks a
# release shipping an incomplete changelog (v0.4.4's headline #881 nearly did).
#
# This gate passes when a PR's `[Unreleased]` section differs from the base
# branch's — i.e. the PR added/changed a changelog entry. Pure-mechanics PRs that
# legitimately have no user-facing change (release-prep, chore, dependabot,
# docs-only) carry the `skip-changelog` label, which the CI job honors before
# invoking this script.
#
# Usage:
#   scripts/check-changelog-entry.sh [base-ref]   # default: origin/main
#   scripts/check-changelog-entry.sh --self-test
set -euo pipefail

CHANGELOG="CHANGELOG.md"

# unreleased_section reads a CHANGELOG on stdin and prints the body of its
# `## [Unreleased]` section (everything up to the next `## [` header). Kept as a
# pure filter so --self-test can exercise it without git.
unreleased_section() {
	awk '
		/^## \[Unreleased\]/ { inu = 1; next }
		inu && /^## \[/       { exit }
		inu                   { print }
	'
}

self_test() {
	local fail=0
	_eq() { # <got> <want> <name>
		if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"; fail=1; fi
	}
	local doc
	doc=$'# Changelog\n\n## [Unreleased]\n\n### Added\n- a new thing (#1)\n\n## [1.0.0] - 2026-01-01\n\n### Fixed\n- old (#0)\n'
	_eq "$(printf '%s' "$doc" | unreleased_section | grep -c 'new thing')" "1" "extracts the Unreleased body"
	_eq "$(printf '%s' "$doc" | unreleased_section | grep -c 'old (#0)')" "0" "stops at the next dated section"
	# An empty Unreleased (just dated below, as right after a GA cut) yields blank.
	local dated
	dated=$'## [Unreleased]\n\n## [1.0.0] - 2026-01-01\n- x\n'
	_eq "$(printf '%s' "$dated" | unreleased_section | tr -d '[:space:]')" "" "empty Unreleased extracts blank"
	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
base="${1:-origin/main}"

if [ ! -f "$CHANGELOG" ]; then
	echo "FAIL: $CHANGELOG not found"; exit 1
fi

base_section="$(git show "${base}:${CHANGELOG}" 2>/dev/null | unreleased_section || true)"
head_section="$(unreleased_section <"$CHANGELOG")"

if [ "$base_section" = "$head_section" ]; then
	echo "FAIL: this PR does not add a CHANGELOG entry under '## [Unreleased]'."
	echo
	echo "Add a Keep-a-Changelog entry describing the user-facing change (Added /"
	echo "Changed / Fixed / Security), or — if the PR has no user-facing change"
	echo "(release-prep, chore, dependabot, docs-only) — apply the 'skip-changelog'"
	echo "label to the PR."
	exit 1
fi

echo "OK: CHANGELOG [Unreleased] updated relative to ${base}."
