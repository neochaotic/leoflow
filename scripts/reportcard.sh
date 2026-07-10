#!/usr/bin/env bash
# Local Go Report Card A+ validation (ADR 0012).
#
# goreportcard.com was retired ("sunset") in 2026 and its badge now renders
# "retired" for every project. Its CLI (goreportcard-cli) also bit-rotted: it
# shells out to the long-archived `gometalinter`, so go_vet/gocyclo/gofmt error
# out and it reports a bogus "Grade F". This script runs the SAME seven checks
# with maintained tooling, so the A+ floor stays verifiable locally and in CI
# without the dead service. golangci-lint (revive) is the maintained, stricter
# successor to the retired `golint` and subsumes several of the checks; we still
# run the standalone tools so each Go Report Card criterion is visible on its own.
set -euo pipefail

# Generated code is excluded exactly as CI's coverage/lint config does. The file
# lists are word-split into command args on purpose — every path is git-tracked
# and space-free — so SC2046 is disabled at those call sites. (Portable to macOS
# bash 3.2: no arrays/mapfile.)
GEN='\.pb\.go|/queries/|_string\.go'
go_files() { git ls-files '*.go' | grep -vE "$GEN"; }
prod_go() { go_files | grep -vE '_test\.go'; }

fail=0
ok() { printf '  \033[32m✓\033[0m %-14s %s\n' "$1" "$2"; }
bad() {
	printf '  \033[31m✗\033[0m %-14s %s\n' "$1" "$2"
	[ -n "${3:-}" ] && echo "$3"
	fail=1
}
need() { command -v "$1" >/dev/null || { echo "missing tool: $1 (run: make setup)"; exit 2; }; }

for t in gocyclo ineffassign misspell golangci-lint; do need "$t"; done

# 1. gofmt — every file is formatted.
# shellcheck disable=SC2046
if out=$(gofmt -l $(go_files)) && [ -z "$out" ]; then
	ok gofmt "clean"
else
	bad gofmt "not formatted:" "$out"
fi

# 2. go vet — no suspicious constructs.
if out=$(go vet ./... 2>&1); then ok "go vet" "clean"; else bad "go vet" "issues:" "$out"; fi

# 3. gocyclo — no production function over 15 (table-driven tests are exempt,
#    matching the golangci-lint config that gates CI).
# shellcheck disable=SC2046
out=$(gocyclo -over 15 $(prod_go) 2>/dev/null || true)
if [ -z "$out" ]; then ok gocyclo "clean (≤15)"; else bad gocyclo "functions over 15:" "$out"; fi

# 4. ineffassign — no ineffectual assignments.
if out=$(ineffassign ./... 2>&1); then ok ineffassign "clean"; else bad ineffassign "issues:" "$out"; fi

# 5. misspell — Go sources only. (The embedded Airflow UI ships localized,
#    non-English docs, and the authoring guide intentionally shows a mistyped
#    task_id in an error example — both are .md, so scanning .go keeps this signal
#    honest, and golangci-lint already spell-checks .go too.)
# shellcheck disable=SC2046
out=$(misspell $(go_files))
if [ -z "$out" ]; then ok misspell "clean"; else bad misspell "misspellings:" "$out"; fi

# 6. license — the repository ships a license (Apache 2.0).
if [ -f LICENSE ]; then ok license "present"; else bad license "no LICENSE file"; fi

# 7. golint (retired) → the golangci-lint stack, its maintained superset, must be
#    clean: GoDocs on every export (revive), plus the extended linters in .golangci.yaml.
if out=$(golangci-lint run ./... 2>&1); then ok golangci-lint "0 issues"; else bad golangci-lint "issues:" "$out"; fi

echo
if [ "$fail" -ne 0 ]; then
	echo -e "\033[31mreportcard: NOT A+\033[0m — fix the above (ADR 0012)"
	exit 1
fi
echo -e "\033[32mreportcard: A+\033[0m — all seven Go Report Card checks clean"
