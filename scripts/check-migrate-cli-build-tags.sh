#!/usr/bin/env bash
# Keep the migrate CLI we SHIP and the migrate CLI we SCAN the same binary.
#
# `deploy/Dockerfile.migrate` compiles golang-migrate's own `cmd/migrate` from
# the version in our go.mod (#1039), and `.github/workflows/security.yaml` runs
# `govulncheck` over that same package so the binary is inside the module graph
# CI actually reads. Both ends of that chain are needed, and neither proves the
# other:
#
#   - golang-migrate registers database drivers behind BUILD TAGS. A binary
#     built with `-tags 'postgres pgx5'` and analysed with no tags is analysed
#     without the drivers it ships — govulncheck reports clean on a package that
#     is not the one in the image, and reports it in a green check. The reverse
#     drift (a tag added to the scan and not to the build) is quieter still.
#   - Trivy keys `stdlib` findings to the Go toolchain recorded in the binary's
#     build info. If the Dockerfile's GO_VERSION and the workflow's diverge, the
#     blocking image gate scans a toolchain the release never builds with.
#
# The failure mode both share is silence: everything stays green while the
# coverage stops meaning anything. That is the exact shape of the gap #1039
# closed — a migrate binary nobody was watching — so it gets a gate rather than
# a comment asking the next reader to keep two files in sync.
#
# Usage:
#   scripts/check-migrate-cli-build-tags.sh
#   scripts/check-migrate-cli-build-tags.sh --self-test
set -euo pipefail

DOCKERFILE="deploy/Dockerfile.migrate"
WORKFLOW=".github/workflows/security.yaml"
PKG="github.com/golang-migrate/migrate/v4/cmd/migrate"

# ── Pure parsers, so --self-test can drive them without the real files ───────

# dockerfile_tags reads a Dockerfile on stdin and prints the build tags of the
# `go build` line, normalised to a sorted, comma-separated set. Accepts the
# space-separated `-tags 'a b'` spelling the Dockerfile uses.
dockerfile_tags() {
	sed -n "s/.*-tags[ =]*'\([^']*\)'.*/\1/p" | head -1 | tr ' ,' '\n' |
		sed '/^$/d' | LC_ALL=C sort -u | paste -sd, -
}

# workflow_tags reads a workflow on stdin and prints the tags passed to
# govulncheck, normalised the same way. Accepts `-tags a,b` and `-tags=a,b`.
workflow_tags() {
	grep -o 'govulncheck[^|]*-tags[ =][^ ]*' | sed -n 's/.*-tags[ =]\([^ ]*\).*/\1/p' |
		head -1 | tr ' ,' '\n' | sed '/^$/d' | LC_ALL=C sort -u | paste -sd, -
}

# dockerfile_go_version reads a Dockerfile on stdin and prints the default of
# its `ARG GO_VERSION=`.
dockerfile_go_version() {
	sed -n 's/^ARG GO_VERSION=\(.*\)$/\1/p' | head -1 | tr -d "\"'"
}

# workflow_go_version reads a workflow on stdin and prints its top-level
# `GO_VERSION:` env value.
workflow_go_version() {
	sed -n 's/^  GO_VERSION: *"\{0,1\}\([0-9.]*\)"\{0,1\} *$/\1/p' | head -1
}

self_test() {
	local fail=0
	_eq() { # <got> <want> <name>
		if [ "$1" = "$2" ]; then printf '  ok   %s\n' "$3"; else
			printf '  FAIL %s\n    got:  %q\n    want: %q\n' "$3" "$1" "$2"
			fail=1
		fi
	}

	local df
	df=$'ARG GO_VERSION=1.26.6\nFROM golang:${GO_VERSION}-bookworm AS build\nRUN go build -trimpath \\\n\t\t-tags '"'"'postgres pgx5'"'"' \\\n\t\t-o /out/migrate '"$PKG"'\n'
	_eq "$(printf '%s' "$df" | dockerfile_tags)" "pgx5,postgres" "Dockerfile tags parse and sort"
	_eq "$(printf '%s' "$df" | dockerfile_go_version)" "1.26.6" "Dockerfile GO_VERSION parses"

	local wf
	wf=$'env:\n  GO_VERSION: "1.26.6"\njobs:\n  x:\n    steps:\n      - run: govulncheck -tags postgres,pgx5 -show verbose '"$PKG"'\n'
	_eq "$(printf '%s' "$wf" | workflow_tags)" "pgx5,postgres" "workflow tags parse and sort"
	_eq "$(printf '%s' "$wf" | workflow_go_version)" "1.26.6" "workflow GO_VERSION parses"

	# `-tags=a,b` is the other spelling govulncheck accepts; a gate that only
	# understood one would pass by reading nothing.
	local wf2
	wf2=$'      - run: govulncheck -tags=pgx5,postgres '"$PKG"'\n'
	_eq "$(printf '%s' "$wf2" | workflow_tags)" "pgx5,postgres" "workflow -tags=a,b spelling"

	# Order must not matter: the sets are compared, not the strings.
	local df2
	df2=$'RUN go build -tags '"'"'pgx5 postgres'"'"' -o /out/migrate x\n'
	_eq "$(printf '%s' "$df2" | dockerfile_tags)" "$(printf '%s' "$df" | dockerfile_tags)" "tag order is irrelevant"

	# A Dockerfile with no -tags at all must yield the empty string rather than
	# silently matching an empty workflow parse — the caller treats empty as a
	# hard failure, and this pins that it can tell the difference.
	_eq "$(printf 'FROM scratch\n' | dockerfile_tags)" "" "no -tags yields empty"

	[ "$fail" -eq 0 ] && echo "self-test: all passed"
	return "$fail"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit $?
fi

cd "$(dirname "$0")/.."

fail=0
note() {
	echo "FAIL: $*" >&2
	fail=1
}

[ -f "$DOCKERFILE" ] || { echo "FAIL: $DOCKERFILE not found" >&2; exit 1; }
[ -f "$WORKFLOW" ] || { echo "FAIL: $WORKFLOW not found" >&2; exit 1; }

# 1. The image must still build OUR binary. A `FROM migrate/migrate` would put
#    a third-party compiled binary back in the pod that holds the DSN, and every
#    other assertion here would go on passing while meaning nothing.
if grep -qE '^\s*FROM\s+migrate/migrate' "$DOCKERFILE"; then
	note "$DOCKERFILE is back on the third-party migrate/migrate image (#1039). The migrate binary must be compiled from the version in our go.mod so govulncheck can see it."
fi
if ! grep -qF "$PKG" "$DOCKERFILE"; then
	note "$DOCKERFILE does not build $PKG."
fi
if ! grep -qF "$PKG" "$WORKFLOW"; then
	note "$WORKFLOW does not run govulncheck over $PKG, so the shipped migrate binary is outside CI's module graph again (#1039)."
fi

# 2. The version stamped into the binary must be READ from the module graph, not
#    written by hand — a hardcoded `-X main.Version=v4.19.1` survives a go.mod
#    bump and makes `migrate -version` lie about what is actually compiled.
if ! grep -qF 'go list -m' "$DOCKERFILE"; then
	note "$DOCKERFILE no longer derives the stamped version with 'go list -m'; a hardcoded version drifts from go.mod silently."
fi

# 3. Build tags: same set on both ends.
df_tags="$(dockerfile_tags <"$DOCKERFILE")"
wf_tags="$(workflow_tags <"$WORKFLOW")"
if [ -z "$df_tags" ]; then
	note "could not read build tags from $DOCKERFILE. golang-migrate registers no database driver without one, so a tagless build fails at RUNTIME with 'unknown driver'."
elif [ -z "$wf_tags" ]; then
	note "could not read -tags from the govulncheck invocation in $WORKFLOW. Without them govulncheck analyses a driverless package, not the binary we ship."
elif [ "$df_tags" != "$wf_tags" ]; then
	note "build tags disagree: $DOCKERFILE builds with [$df_tags], $WORKFLOW scans with [$wf_tags]. govulncheck would be analysing a different binary than the one in the image."
fi

# 4. Go toolchain: same pin on both ends.
df_go="$(dockerfile_go_version <"$DOCKERFILE")"
wf_go="$(workflow_go_version <"$WORKFLOW")"
if [ -z "$df_go" ]; then
	note "$DOCKERFILE has no 'ARG GO_VERSION=' default. An unpinned toolchain moves this image's reported stdlib findings with no commit of ours."
elif [ -z "$wf_go" ]; then
	note "could not read GO_VERSION from $WORKFLOW."
elif [ "$df_go" != "$wf_go" ]; then
	note "Go toolchain pins disagree: $DOCKERFILE builds with $df_go, $WORKFLOW uses $wf_go. Trivy keys stdlib findings to the toolchain in the binary, so the gate would scan a build the release never produces."
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi

echo "OK: the shipped migrate CLI ($PKG, tags [$df_tags], go $df_go) is the one govulncheck and the image gate scan"
