#!/usr/bin/env bash
# Fail a PR that changes user-facing surface without touching the docs site.
#
# The CHANGELOG gate exists because entries "repeatedly merged with none and had
# to be back-filled by separate PRs (at least eight)". The docs are in exactly
# that state, and it is not hypothetical: taskPodSecurity.readOnlyRootFilesystem
# — the security hardening of the v0.4.5 tranche — shipped in rc.3 with ZERO
# mentions anywhere in website/content/. It was validated on a real cluster the
# same day and nobody noticed, because nothing looks.
#
# "User-facing surface" is deliberately narrow, so the gate is about capability,
# not churn: a chart value an operator sets, the authoring schema a user writes,
# and the CLI commands they run. Internal refactors do not trip it.
#
# Escape hatch: the `skip-docs` label, for PRs that genuinely change nothing a
# user could discover — release prep, dependabot, pure internals. Same shape as
# skip-changelog, and the workflow re-runs on label so it takes effect at once.
#
# Usage: scripts/check-docs-updated.sh <base-ref>   e.g. origin/main
#        scripts/check-docs-updated.sh --self-test
set -euo pipefail

DOCS_DIR="website/content"
# Paths whose change implies something an operator or author can see.
SURFACE_RE='^(helm/leoflow/values\.yaml|docs/api/leoflow-yaml-schema\.json|internal/domain/schemas/|internal/cli/[a-z_]+\.go)$'

check() { # <base-ref> [changed-files-file]
	local base="$1" listfile="${2:-}" changed
	if [ -n "$listfile" ]; then
		changed=$(cat "$listfile")
	else
		changed=$(git diff --name-only "$base"...HEAD)
	fi
	local surface docs
	surface=$(printf '%s\n' "$changed" | grep -E "$SURFACE_RE" || true)
	docs=$(printf '%s\n' "$changed" | grep -E "^${DOCS_DIR}/" || true)
	if [ -z "$surface" ]; then
		echo "no user-facing surface changed; docs not required"
		return 0
	fi
	if [ -n "$docs" ]; then
		echo "user-facing surface changed and ${DOCS_DIR}/ was updated"
		return 0
	fi
	{
		echo "This PR changes user-facing surface but updates no docs:"
		printf '  %s\n' $surface
		echo
		echo "Update ${DOCS_DIR}/ in THIS PR — a capability an operator cannot find"
		echo "in the docs did not really ship. If nothing here is user-discoverable,"
		echo "apply the 'skip-docs' label and this guard re-runs."
	} >&2
	return 1
}

self_test() {
	local tmp rc; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' RETURN

	printf 'helm/leoflow/values.yaml\nwebsite/content/operate/x.md\n' > "$tmp/a"
	check X "$tmp/a" >/dev/null || { echo "self-test FAIL: surface+docs rejected" >&2; return 1; }

	printf 'internal/executor/kubernetes.go\ninternal/storage/repo.go\n' > "$tmp/b"
	check X "$tmp/b" >/dev/null || { echo "self-test FAIL: internal-only rejected" >&2; return 1; }

	# The real regression: a chart value with no docs.
	printf 'helm/leoflow/values.yaml\n' > "$tmp/c"
	rc=0; check X "$tmp/c" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: chart value with no docs accepted" >&2; return 1; }

	# The authoring schema is surface too.
	printf 'docs/api/leoflow-yaml-schema.json\n' > "$tmp/d"
	rc=0; check X "$tmp/d" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: schema change with no docs accepted" >&2; return 1; }

	# A CLI command is surface; an internal helper in the same package is not.
	printf 'internal/cli/compile.go\n' > "$tmp/e"
	rc=0; check X "$tmp/e" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: cli change with no docs accepted" >&2; return 1; }

	# A docs-only PR must pass, not trip on itself.
	printf 'website/content/operate/x.md\n' > "$tmp/f"
	check X "$tmp/f" >/dev/null || { echo "self-test FAIL: docs-only PR rejected" >&2; return 1; }

	# Exercise the CLI ENTRYPOINT, not just check(). The bug this catches:
	# the entrypoint forwarded only $1, dropping the file list, so every real
	# invocation diffed <base>...HEAD (empty) and passed. check() was fine.
	rc=0; bash "$0" X "$tmp/c" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: entrypoint ignored the file list" >&2; return 1; }
	rc=0; bash "$0" X "$tmp/f" >/dev/null 2>&1 || rc=$?
	[ "$rc" -eq 0 ] || { echo "self-test FAIL: entrypoint rejected a docs-only PR" >&2; return 1; }

	echo "check-docs-updated self-test: ok"
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
[ $# -ge 1 ] || { echo "usage: $0 <base-ref> [changed-files-file] | --self-test" >&2; exit 2; }
# Forward BOTH arguments. This used to be `check "$1"`, which silently dropped
# the file list and diffed <base>...HEAD instead — empty in CI, so the gate
# passed everything. The self-test never caught it because it calls check()
# directly and bypasses this line, so the only broken path was the only one
# CI uses. A gate whose entrypoint is inert is worse than no gate.
check "$@"
