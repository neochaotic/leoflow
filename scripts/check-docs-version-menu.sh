#!/usr/bin/env bash
# The documentation version menu is defined in two files that nothing reconciles:
#
#   website/scripts/ci/versions.json   what the Pages build actually publishes
#   website/hugo.toml                  the fallback a plain `hugo` build uses
#
# hugo.toml's own comment says to "keep this block in sync by hand" when
# versions.json changes. It was not: versions.json listed v0.4.5 and hugo.toml
# did not, so a local build and the published site disagreed about which
# releases exist. A procedure written in a comment that no script performs is a
# procedure that does not happen, which is the same defect class this repo
# already gates for the chart README, the Airflow UI pin and the probe budgets.
#
# It matters beyond tidiness because the fallback is what a contributor sees
# locally, and the whole point of a version menu is telling a reader which
# version they are on. A menu that lies locally teaches the wrong thing about
# the menu that ships.
#
# The gate compares the SET of labels and the latest/dev entries. It does not
# compare urls: hugo.toml carries absolute URLs and versions.json carries
# subpaths, and reconciling those is the renderer's job
# (website/scripts/ci/render-version-config.py), not this gate's.
#
# Usage: scripts/check-docs-version-menu.sh [--self-test]
#        scripts/check-docs-version-menu.sh <versions.json> <hugo.toml>
set -euo pipefail

check() { # <versions.json> <hugo.toml>
	python3 - "$1" "$2" <<'PY'
import json
import re
import sys

vpath, hpath = sys.argv[1], sys.argv[2]


def fail(msg):
	sys.exit("FAIL: " + msg)


def read(p):
	try:
		with open(p, encoding="utf-8") as fh:
			return fh.read()
	except OSError as exc:
		fail(f"cannot read {p}: {exc}")


try:
	manifest = json.loads(read(vpath))
except ValueError as exc:
	fail(f"{vpath} is not valid JSON: {exc}")

entries = manifest.get("versions")
if not entries:
	fail(f"{vpath} has no `versions` array")

json_labels = [e.get("label") for e in entries]
if any(l is None for l in json_labels):
	fail(f"{vpath} has an entry with no `label`")

hugo = read(hpath)
hugo_labels = re.findall(r'\[\[params\.versions\]\]\s*\n\s*version\s*=\s*"([^"]+)"', hugo)
if not hugo_labels:
	fail(f"{hpath} has no `[[params.versions]]` block: renamed, or the fallback was dropped")

missing = [l for l in json_labels if l not in hugo_labels]
extra = [l for l in hugo_labels if l not in json_labels]
if missing or extra:
	parts = []
	if missing:
		parts.append("missing from the fallback: " + ", ".join(repr(m) for m in missing))
	if extra:
		parts.append("present only in the fallback: " + ", ".join(repr(e) for e in extra))
	fail(
		f"{hpath}'s version menu disagrees with {vpath} ({'; '.join(parts)}). A local build would "
		f"then offer a different set of releases than the published site. Update the "
		f"[[params.versions]] block in {hpath} to match."
	)

# The two legs that are not plain archived tags carry meaning a reader depends
# on, so they are checked by name rather than only by set membership.
by_id = {e.get("id"): e for e in entries}
for leg in ("latest", "dev"):
	if leg not in by_id:
		fail(f"{vpath} has no `{leg}` entry; the menu needs both a current release and an unreleased leg")

latest = by_id["latest"]["label"]
version_match = re.search(r"v\d+\.\d+\.\d+(?:[-.][0-9A-Za-z]+)*", latest)
if not version_match:
	fail(
		f"the `latest` label is {latest!r} and names no version. The current release is then the one "
		f"release the menu never identifies, since an archived tag shows its number only after it is "
		f"superseded. Use a label like 'latest (v1.2.3)'."
	)
# A version-shaped substring is not enough on its own: a label left over from a
# previous promotion also matches the pattern above. Tie the check to the
# `ref` the `latest` entry actually points at, so a label that names the wrong
# release (stale, or copy-pasted from an archived leg) fails instead of
# passing because *some* version string is present.
latest_ref = by_id["latest"].get("ref")
if latest_ref and version_match.group(0) != latest_ref:
	fail(
		f"the `latest` label is {latest!r} but its `ref` is {latest_ref!r}. The label names a version "
		f"other than the one it actually builds from; update the label to match the ref."
	)
if not re.search(r"\bmain\b", by_id["dev"]["label"]):
	fail(
		f"the `dev` label is {by_id['dev']['label']!r} and does not say it is main. A reader cannot tell "
		f"unreleased documentation from the current release."
	)

print(f"docs version menu: {len(json_labels)} legs agree, latest={latest!r}, dev={by_id['dev']['label']!r}")
PY
}

self_test() {
	local fail=0 tmp
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN

	_write() { # <json> <hugo-labels...>
		printf '%s\n' "$1" > "$tmp/versions.json"
		shift
		: > "$tmp/hugo.toml"
		for l in "$@"; do
			printf '  [[params.versions]]\n    version = "%s"\n    url = "https://example.invalid/"\n' "$l" >> "$tmp/hugo.toml"
		done
	}

	_case() { # <name> <want-exit> <want-substr> <json> <hugo-labels...>
		local name=$1 want=$2 substr=$3 json=$4
		shift 4
		_write "$json" "$@"
		local out rc
		out=$(check "$tmp/versions.json" "$tmp/hugo.toml" 2>&1) && rc=0 || rc=$?
		if [ "$rc" -ne "$want" ]; then
			echo "self-test FAIL: $name: exit $rc, wanted $want"; echo "  $out"; fail=1; return
		fi
		case "$out" in *"$substr"*) ;; *) echo "self-test FAIL: $name: output lacks \"$substr\""; echo "  $out"; fail=1 ;; esac
	}

	local good='{"versions":[{"id":"latest","label":"latest (v1.2.3)"},{"id":"v1.2.2","label":"v1.2.2"},{"id":"dev","label":"dev (main, unreleased)"}]}'
	local unnumbered='{"versions":[{"id":"latest","label":"latest"},{"id":"dev","label":"dev (main, unreleased)"}]}'
	local vague_dev='{"versions":[{"id":"latest","label":"latest (v1.2.3)"},{"id":"dev","label":"dev"}]}'
	local no_dev='{"versions":[{"id":"latest","label":"latest (v1.2.3)"}]}'
	# A version-shaped substring is not the same as the RIGHT version: this
	# `latest` label still matches v1.2.3, but its own `ref` says v1.2.4, which
	# is the shape of a promotion that updated the ref and forgot the label.
	local stale_latest='{"versions":[{"id":"latest","ref":"v1.2.4","label":"latest (v1.2.3)"},{"id":"dev","label":"dev (main, unreleased)"}]}'
	# "main" is a substring of "maintenance" too. A dev label built by string
	# concatenation could land here without ever saying it is the main branch.
	local mainless_dev='{"versions":[{"id":"latest","label":"latest (v1.2.3)"},{"id":"dev","label":"dev (maintenance branch)"}]}'

	_case "matching menus pass"            0 "3 legs agree"          "$good"       "latest (v1.2.3)" "v1.2.2" "dev (main, unreleased)"
	_case "a leg missing downstream"       1 "missing from the fallback" "$good"   "latest (v1.2.3)" "dev (main, unreleased)"
	_case "a leg only in the fallback"     1 "present only in the fallback" "$good" "latest (v1.2.3)" "v1.2.2" "v0.9.0" "dev (main, unreleased)"
	_case "an unnumbered latest is caught" 1 "names no version"      "$unnumbered" "latest" "dev (main, unreleased)"
	_case "a dev that hides main"          1 "does not say it is main" "$vague_dev" "latest (v1.2.3)" "dev"
	_case "a missing dev leg"              1 "no \`dev\` entry"       "$no_dev"     "latest (v1.2.3)"
	_case "a latest label stale vs. its ref" 1 "names a version other than the one it actually builds from" "$stale_latest" "latest (v1.2.3)" "dev (main, unreleased)"
	_case "'maintenance' does not count as naming main" 1 "does not say it is main" "$mainless_dev" "latest (v1.2.3)" "dev (maintenance branch)"

	# A renamed-away fallback must fail loudly rather than pass by matching nothing.
	printf '%s\n' "$good" > "$tmp/versions.json"
	printf 'params:\n  version = "dev"\n' > "$tmp/hugo.toml"
	if check "$tmp/versions.json" "$tmp/hugo.toml" >/dev/null 2>&1; then
		echo "self-test FAIL: a dropped [[params.versions]] block passes silently"; fail=1
	fi

	if [ "$fail" -eq 0 ]; then echo "check-docs-version-menu self-test: ok"; return 0; fi
	return 1
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

cd "$(dirname "$0")/.."
check "${1:-website/scripts/ci/versions.json}" "${2:-website/hugo.toml}"
