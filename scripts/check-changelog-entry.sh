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
	# The pure filter above was the only thing under test, which is why nothing
	# caught that this gate fails when invoked with no pull request to judge —
	# cut-release.sh's run_gates() globs scripts/check-*.sh and runs this one
	# bare from main, where the base and the working tree are the same commit,
	# so every cut died on "mechanical gates failed". These cases drive the
	# script itself in a throwaway repository.
	local script tmp
	script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
	tmp="$(mktemp -d)"
	(
		cd "$tmp" || exit 1
		git init -q . && git config user.email t@t && git config user.name t
		mkdir -p scripts && cp "$script" scripts/
		printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Fixed' '- a thing (#1)' '' '## [1.0.0] - 2026-01-01' '- old' > CHANGELOG.md
		git add -A && git commit -qm base && git branch -M main
		git remote add origin . && git update-ref refs/remotes/origin/main refs/heads/main
	) >/dev/null 2>&1
	# On the base branch there is no PR: the gate must skip, not fail.
	local rc=0
	( cd "$tmp" && bash scripts/check-changelog-entry.sh >/dev/null 2>&1 ) || rc=$?
	_eq "$rc" "0" "skips on the base branch (the cut invokes it bare)"
	# On a branch that changes something else, it must still fail.
	rc=0
	(
		cd "$tmp" || exit 1
		git checkout -qb feature && echo x > other.txt && git add -A && git commit -qm other
		bash scripts/check-changelog-entry.sh >/dev/null 2>&1
	) || rc=$?
	_eq "$rc" "1" "still fails a branch that adds no entry"
	# And pass when the branch does add one.
	rc=0
	(
		cd "$tmp" || exit 1
		printf '%s\n' '# Changelog' '' '## [Unreleased]' '' '### Fixed' '- a thing (#1)' '- another thing (#2)' '' '## [1.0.0] - 2026-01-01' '- old' > CHANGELOG.md
		git add -A && git commit -qm entry
		bash scripts/check-changelog-entry.sh >/dev/null 2>&1
	) || rc=$?
	_eq "$rc" "0" "passes a branch that adds an entry"
	rm -rf "$tmp"

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
base="${1:-origin/main}"

if [ ! -f "$CHANGELOG" ]; then
	echo "FAIL: $CHANGELOG not found"; exit 1
fi

# With no pull request to judge, this gate has no question to ask. HEAD being an
# ancestor of the base ref means we ARE on the base branch — which is how
# cut-release.sh's run_gates() invokes every scripts/check-*.sh during a release.
# Comparing the base's Unreleased section against its own is always equal, so the
# gate reported FAIL and the cut died on "mechanical gates failed". A pull
# request's HEAD is never an ancestor of its base, so the gate still runs there.
if git rev-parse --verify --quiet "$base" >/dev/null 2>&1 &&
	git merge-base --is-ancestor HEAD "$base" >/dev/null 2>&1; then
	echo "OK: HEAD is on ${base}, so there is no pull request to judge — gate skipped."
	exit 0
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
