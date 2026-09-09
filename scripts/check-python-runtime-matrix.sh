#!/usr/bin/env bash
# The set of Python lines Leoflow publishes a task base image for is stated in
# five files, and only one of them is enforced by anything.
#
# The enforced one is the `python_version` enum in
# internal/domain/schemas/leoflow-yaml-schema.json: it is embedded into the CLI
# and `LeoflowConfig.Validate()` rejects anything else. (docs/api/ carries the
# byte-equal canonical mirror of the same file; TestEmbeddedSchemasMatchDocs
# already binds the two, so this gate reads the copy that actually ships in the
# binary.) The other four are the
# release matrix that actually pushes the images, the Makefile target that
# builds them locally, the comment in runtime/Dockerfile, and the configuration
# reference the user reads. Nothing reconciled them, and by #1031 they had
# already drifted in both directions: the release published py3.10 while the
# host detector probed for a 3.13 nobody published.
#
# Both directions of drift fail in the same expensive place. A version in the
# enum with no published leg is a `docker pull ... 404` inside the USER's build,
# minutes after their compile started, naming an image they never typed. A
# published leg missing from the enum is a `leoflow compile` that refuses a
# perfectly good image with "value must be one of" — and the user cannot fix
# either one, because both live in our repo.
#
# This is the same "two copies of a fact with nothing between them" shape as
# check-lite-prepull-matches-compose.sh, and it takes the same two lessons from
# it. The workflow is parsed as YAML, not grepped, so a commented-out matrix
# cannot keep the gate green. And a missing file, a renamed job or an
# unrecognized declaration is a FAIL, never a silent pass — a gate that stops
# gating after a file move is worse than no gate, because it still reports OK.
#
# The floor in runtime/python/pyproject.toml is checked too: the runtime helper
# is pip-installed INTO the base image, so a `requires-python` above the oldest
# published line makes that leg unbuildable, and one below it advertises support
# for an interpreter we ship no image for.
#
# Usage: scripts/check-python-runtime-matrix.sh [--self-test]
#        scripts/check-python-runtime-matrix.sh <repo-root>
set -euo pipefail

SCHEMA_REL="internal/domain/schemas/leoflow-yaml-schema.json"
WORKFLOW_REL=".github/workflows/release.yaml"
MAKEFILE_REL="Makefile"
DOCKERFILE_REL="runtime/Dockerfile"
PYPROJECT_REL="runtime/python/pyproject.toml"
DOCS_REL="website/content/reference/configuration.md"
JOB="runtime-image"

check() { # <repo-root>
	python3 - "$1" "$SCHEMA_REL" "$WORKFLOW_REL" "$MAKEFILE_REL" "$DOCKERFILE_REL" "$PYPROJECT_REL" "$DOCS_REL" "$JOB" <<'PY'
import json
import os
import re
import sys

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML unavailable; cannot parse the release workflow (pip install pyyaml)")

root, schema_rel, workflow_rel, makefile_rel, dockerfile_rel, pyproject_rel, docs_rel, job_name = sys.argv[1:9]

problems = []
VERSION_RE = re.compile(r"3\.\d+")


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(rel):
	path = os.path.join(root, rel)
	if not os.path.exists(path):
		fail(f"{rel} does not exist — this gate's scan target moved and it would otherwise report OK")
	with open(path) as fh:
		text = fh.read()
	if not text.strip():
		fail(f"{rel} is empty")
	return text


def note(rel, what, got, want):
	problems.append(f"  {rel}\n    {what}: {got}\n    schema enum:  {want}")


# ── the source of truth ──────────────────────────────────────────────────────
schema = json.loads(read(schema_rel))
py = ((schema.get("properties") or {}).get("python_version") or {})
want = py.get("enum")
if not want:
	fail(f"{schema_rel} declares no python_version enum — the source of truth is gone")
if sorted(set(want)) != sorted(want) or len(want) != len(set(want)):
	fail(f"{schema_rel}'s python_version enum repeats a value: {want}")
for v in want:
	if not VERSION_RE.fullmatch(v):
		fail(f"{schema_rel}'s python_version enum contains {v!r}, which is not a 3.<minor> line")

default = py.get("default")
if default not in want:
	fail(f"{schema_rel}'s python_version default {default!r} is not in its own enum {want}")

deprecations = py.get("x-leoflow-python-deprecations") or {}
for v in deprecations:
	if v not in want:
		fail(
			f"{schema_rel} deprecates python_version {v!r}, which is not in the enum {want}.\n"
			"A deprecation note for a version nobody can select is a note nobody reads;\n"
			"drop the note in the same change that drops the leg."
		)
if default in deprecations:
	fail(f"{schema_rel} defaults python_version to {default!r}, which it also marks deprecated")

wantset = set(want)
sorted_want = sorted(want, key=lambda v: [int(p) for p in v.split(".")])
if want != sorted_want:
	fail(f"{schema_rel}'s python_version enum is not in ascending order: {want}")

# ── 1. the release matrix that pushes the images ─────────────────────────────
workflow = yaml.safe_load(read(workflow_rel)) or {}
jobs = workflow.get("jobs") or {}
if job_name not in jobs:
	fail(f"{workflow_rel} has no `{job_name}` job — did it get renamed?")
matrix = (((jobs[job_name].get("strategy") or {}).get("matrix")) or {}).get("python")
if not matrix:
	fail(
		f"{workflow_rel}'s `{job_name}` job declares no `strategy.matrix.python`.\n"
		"Without it no base image is published at all, and every `leoflow compile`\n"
		"that does not override base_image fails on the pull."
	)
# YAML reads an unquoted 3.10 as the float 3.1, which is the exact typo this
# gate exists to catch; compare as written, not as parsed.
got = [str(v) for v in matrix]
if set(got) != wantset:
	note(workflow_rel, f"`{job_name}` matrix.python", got, want)

# ── 2. the Makefile target that builds them locally ──────────────────────────
make_text = read(makefile_rel)
recipe = None
in_target = False
for line in make_text.splitlines():
	if re.match(r"^runtime-images\s*:", line):
		in_target = True
		continue
	if in_target:
		# A recipe line is TAB-indented; the first line that is not ends the recipe.
		if not line.startswith("\t"):
			if line.strip() == "" or line.startswith("#"):
				continue
			break
		if line.lstrip().startswith("#"):
			continue
		if "for v in" in line:
			recipe = line
			break
if recipe is None:
	fail(
		f"{makefile_rel} has no `runtime-images` recipe with a `for v in ...` loop — did it get renamed?\n"
		"`make runtime-images` is how the base images are built outside CI; a gate that\n"
		"cannot find it must not report OK."
	)
loop = recipe.split("for v in", 1)[1].split(";", 1)[0]
got = VERSION_RE.findall(loop)
if set(got) != wantset:
	note(makefile_rel, "`runtime-images` loop", got, want)

# ── 3. the runtime Dockerfile's own statement of the list ────────────────────
dockerfile_text = read(dockerfile_rel)
marker = "PYTHON_VERSION selects the interpreter"
lines = [ln for ln in dockerfile_text.splitlines() if marker in ln]
if not lines:
	fail(
		f"{dockerfile_rel} no longer carries the `{marker} (...)` comment.\n"
		"It is the list a reader of the Dockerfile sees; either restore it or delete\n"
		"this arm of the gate deliberately."
	)
got = VERSION_RE.findall(lines[0])
if set(got) != wantset:
	note(dockerfile_rel, f"`{marker}` comment", got, want)

# ── 4. the runtime helper's floor ────────────────────────────────────────────
pyproject_text = read(pyproject_rel)
m = re.search(r"^\s*requires-python\s*=\s*[\"']>=\s*(3\.\d+)", pyproject_text, re.M)
if not m:
	fail(f"{pyproject_rel} declares no `requires-python = \">=3.<minor>\"` — did the key change shape?")
floor = m.group(1)
oldest = sorted_want[0]
if floor != oldest:
	problems.append(
		f"  {pyproject_rel}\n"
		f"    requires-python floor: >={floor}\n"
		f"    oldest published leg:  {oldest}\n"
		"    The runtime helper is pip-installed into the base image: a floor above the\n"
		"    oldest published line makes that leg unbuildable; one below it advertises an\n"
		"    interpreter no base image exists for."
	)

# ── 5. the configuration reference the user reads ────────────────────────────
docs_text = read(docs_rel)
rows = [ln for ln in docs_text.splitlines() if ln.startswith("|") and "`python_version`" in ln]
if not rows:
	fail(f"{docs_rel} has no table row naming `python_version` — did the reference move?")
for row in rows:
	got = VERSION_RE.findall(row)
	if set(got) != wantset:
		note(docs_rel, f"row {row.strip()[:60]!r}", got, want)

status_rows = [ln for ln in docs_text.splitlines() if re.match(r"^\|\s*`3\.\d+`\s*\|", ln)]
if not status_rows:
	fail(
		f"{docs_rel} has no Python-support status table (rows of the shape `| \\`3.11\\` | ... |`).\n"
		"That table is where a user learns which lines are deprecated and until when."
	)
got = [VERSION_RE.search(ln).group(0) for ln in status_rows]
if set(got) != wantset:
	note(docs_rel, "Python version support table", got, want)

if problems:
	sys.exit(
		"FAIL: the published-Python list has drifted from the schema enum.\n"
		+ "\n".join(problems)
		+ f"\n\nThe schema ({schema_rel}) is the source of truth: it is what the CLI\n"
		"enforces. Change it first, then bring the copies to match."
	)

deprecated = ", ".join(sorted(deprecations)) or "none"
print(f"OK: published Python matrix agrees everywhere ({', '.join(want)}; deprecated: {deprecated})")
PY
}

self_test() {
	local fail=0 tmp
	tmp="$(mktemp -d)"
	trap 'rm -rf "${tmp:-}"' RETURN

	# Builds a minimal repo root whose five copies all agree on 3.10/3.11.
	_mkroot() { # <dir>
		local d="$1"
		mkdir -p "$d/internal/domain/schemas" "$d/.github/workflows" "$d/runtime/python" \
			"$d/website/content/reference"
		cat >"$d/internal/domain/schemas/leoflow-yaml-schema.json" <<'JSON'
{"properties":{"python_version":{"enum":["3.10","3.11"],"default":"3.11",
 "x-leoflow-python-deprecations":{"3.10":{"eol":"2026-10-31"}}}}}
JSON
		printf 'jobs:\n  runtime-image:\n    strategy:\n      matrix:\n        python: ["3.10", "3.11"]\n' \
			>"$d/.github/workflows/release.yaml"
		printf 'runtime-images:\n\tfor v in 3.10 3.11; do \\\n\t\tdocker build . ; \\\n\tdone\n' >"$d/Makefile"
		printf '# PYTHON_VERSION selects the interpreter (3.10 / 3.11).\nARG PYTHON_VERSION=3.11\n' \
			>"$d/runtime/Dockerfile"
		printf '[project]\nrequires-python = ">=3.10"\n' >"$d/runtime/python/pyproject.toml"
		{
			printf '| `python_version` | `3.10`\\|`3.11` | Base image Python. |\n'
			printf '| `python_version` | `"3.11"` | Pick `3.10` or `3.11`. |\n'
			printf '| Line | Status |\n|---|---|\n'
			printf '| `3.10` | Deprecated |\n| `3.11` | Supported |\n'
		} >"$d/website/content/reference/configuration.md"
	}

	_case() { # <name> <want-rc> <want-substring> <mutator...>
		local name="$1" want_rc="$2" want_msg="$3" rc=0 out d
		shift 3
		d="$tmp/case"
		rm -rf "$d"
		_mkroot "$d"
		"$@" "$d"
		out="$(check "$d" 2>&1)" || rc=$?
		if [ "$rc" != "$want_rc" ]; then
			printf '  FAIL %s (exit)\n    got:  %s\n    want: %s\n    out: %s\n' "$name" "$rc" "$want_rc" "$out"
			fail=1
			return
		fi
		case "$out" in
			*"$want_msg"*) printf '  ok   %s\n' "$name" ;;
			*) printf '  FAIL %s (message)\n    got: %q\n    want to contain: %q\n' "$name" "$out" "$want_msg"; fail=1 ;;
		esac
	}

	_noop() { :; }
	_drop_from_matrix() { printf 'jobs:\n  runtime-image:\n    strategy:\n      matrix:\n        python: ["3.10"]\n' >"$1/.github/workflows/release.yaml"; }
	_comment_out_matrix() { printf 'jobs:\n  runtime-image:\n    strategy:\n      matrix:\n        # python: ["3.10", "3.11"]\n        os: [ubuntu-latest]\n' >"$1/.github/workflows/release.yaml"; }
	_rename_job() { printf 'jobs:\n  runtime-base:\n    strategy:\n      matrix:\n        python: ["3.10", "3.11"]\n' >"$1/.github/workflows/release.yaml"; }
	_unquoted_matrix() { printf 'jobs:\n  runtime-image:\n    strategy:\n      matrix:\n        python: [3.10, 3.11]\n' >"$1/.github/workflows/release.yaml"; }
	_stale_makefile() { printf 'runtime-images:\n\tfor v in 3.10; do \\\n\t\tdocker build . ; \\\n\tdone\n' >"$1/Makefile"; }
	_commented_makefile_loop() { printf 'runtime-images:\n\t# for v in 3.10 3.11; do \\\n\t@echo skip\n' >"$1/Makefile"; }
	_other_target_loop() { printf 'other-images:\n\tfor v in 3.10 3.11; do \\\n\tdone\n\nruntime-images:\n\t@echo nothing\n' >"$1/Makefile"; }
	_stale_dockerfile() { printf '# PYTHON_VERSION selects the interpreter (3.10 / 3.11 / 3.12).\n' >"$1/runtime/Dockerfile"; }
	_dropped_dockerfile_comment() { printf 'ARG PYTHON_VERSION=3.11\n' >"$1/runtime/Dockerfile"; }
	_raised_floor() { printf '[project]\nrequires-python = ">=3.11"\n' >"$1/runtime/python/pyproject.toml"; }
	_stale_docs_row() { printf '| `python_version` | `3.10`\\|`3.11`\\|`3.12` | x |\n| `3.10` | Deprecated |\n| `3.11` | Supported |\n' >"$1/website/content/reference/configuration.md"; }
	_stale_status_table() { printf '| `python_version` | `3.10`\\|`3.11` | x |\n| `3.10` | Deprecated |\n' >"$1/website/content/reference/configuration.md"; }
	_missing_schema() { rm -f "$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_empty_enum() { printf '{"properties":{"python_version":{"default":"3.11"}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_unlisted_deprecation() { printf '{"properties":{"python_version":{"enum":["3.10","3.11"],"default":"3.11","x-leoflow-python-deprecations":{"3.9":{}}}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_deprecated_default() { printf '{"properties":{"python_version":{"enum":["3.10","3.11"],"default":"3.10","x-leoflow-python-deprecations":{"3.10":{}}}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }
	_unsorted_enum() { printf '{"properties":{"python_version":{"enum":["3.11","3.10"],"default":"3.11"}}}\n' >"$1/internal/domain/schemas/leoflow-yaml-schema.json"; }

	_case "an agreeing tree passes"                  0 "agrees everywhere (3.10, 3.11; deprecated: 3.10)" _noop
	_case "a leg missing from the release matrix"    1 "matrix.python" _drop_from_matrix
	_case "a commented-out matrix is not a matrix"   1 "declares no \`strategy.matrix.python\`" _comment_out_matrix
	_case "a renamed job fails loudly"               1 "did it get renamed?" _rename_job
	# YAML turns an unquoted 3.10 into the float 3.1 — the typo this gate is for.
	_case "an unquoted 3.10 is caught"               1 "matrix.python" _unquoted_matrix
	_case "a stale Makefile loop"                    1 "runtime-images\` loop" _stale_makefile
	_case "a commented-out Makefile loop"            1 "did it get renamed?" _commented_makefile_loop
	_case "another target's loop does not count"     1 "did it get renamed?" _other_target_loop
	_case "a stale Dockerfile comment"               1 "selects the interpreter\` comment" _stale_dockerfile
	_case "a dropped Dockerfile comment"             1 "no longer carries the" _dropped_dockerfile_comment
	_case "a raised requires-python floor"           1 "oldest published leg" _raised_floor
	_case "a stale docs row"                         1 "configuration.md" _stale_docs_row
	_case "a stale docs status table"                1 "Python version support table" _stale_status_table
	_case "a missing schema fails, not skips"        1 "does not exist" _missing_schema
	_case "an empty enum fails"                      1 "declares no python_version enum" _empty_enum
	_case "a deprecation outside the enum"           1 "is not in the enum" _unlisted_deprecation
	_case "a deprecated default"                     1 "also marks deprecated" _deprecated_default
	_case "an out-of-order enum"                     1 "not in ascending order" _unsorted_enum

	if [ "$fail" -eq 0 ]; then echo "self-test: PASS"; return 0; else echo "self-test: FAIL"; return 1; fi
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-$PWD}"
