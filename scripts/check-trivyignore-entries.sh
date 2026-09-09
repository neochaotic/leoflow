#!/usr/bin/env bash
# Every accepted container-scan finding must say why it is accepted and when
# that acceptance lapses — and the file holding them must actually be wired to a
# scan.
#
# An ignore list is where a scanning policy goes to die. Not through a bad entry:
# through a reasonable entry added in a hurry that nobody revisits, until the
# list is the reason the scanner reports clean. Trivy gives us half the cure —
# it honours `expired_at` and stops suppressing once the date passes, so neglect
# makes a finding COME BACK rather than stay hidden. It does not give us the
# other half: nothing in Trivy requires an entry to carry an `expired_at` at all,
# or a rationale, so the easiest entry to write is the permanent, unexplained one.
# This gate makes that entry impossible to commit.
#
# The wiring check is the part that is easy to leave out and the most likely to
# fail open. A renamed ignore file, or a `--ignorefile` flag dropped from a
# workflow during an edit, leaves this gate happily validating a file that no
# scan reads — entries look accepted and suppress nothing, or worse, a scan runs
# with no suppressions and someone "fixes" the noise by disabling the scan.
# Neither end proves the chain on its own, so both ends are checked here: the
# workflows must reference an ignore file, and the file they reference must be
# the one that exists.
#
# The tree is PARSED, not grepped. `--ignorefile` lives inside a `run:` block, so
# the raw text also contains it in shell comments and in this script's own
# documentation; a grep would count those as wiring and pass a workflow that has
# none. Shell-comment lines inside the parsed `run:` scripts are dropped for the
# same reason.
#
# Usage: scripts/check-trivyignore-entries.sh [--self-test]
#        scripts/check-trivyignore-entries.sh <workflow-dir> <ignore-file>
set -euo pipefail

# Longest an acceptance may be parked before it must be re-argued. Six months is
# two release trains: long enough that a real "wait for upstream" is not busywork,
# short enough that nobody can park a finding past the memory of why.
MAX_DAYS="${MAX_DAYS:-180}"
# A rationale shorter than this is not one. Tuned to reject "not exploitable"
# while accepting a genuine one-sentence explanation.
MIN_STATEMENT="${MIN_STATEMENT:-40}"

check() { # <workflow-dir> <ignore-file>
	MAX_DAYS="$MAX_DAYS" MIN_STATEMENT="$MIN_STATEMENT" python3 - "$@" <<'PY'
import datetime
import os
import pathlib
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("PyYAML is required (pip install pyyaml)")

wf_dir, ignore_path = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
max_days = int(os.environ["MAX_DAYS"])
min_statement = int(os.environ["MIN_STATEMENT"])
today = datetime.date.today()
fail = []

# Trivy's known ignore sections. A typo like `vulnerability:` parses fine and
# suppresses nothing, so an unknown key is a silent no-op, not a style nit.
KNOWN_SECTIONS = {"vulnerabilities", "misconfigurations", "secrets", "licenses"}

PLACEHOLDERS = {"todo", "tbd", "fixme", "xxx", "n/a", "na", "none", "wip",
                "accepted", "temporary", "later", "see above", "-"}

# ── 1. The ignore file must be wired to at least one scan ────────────────────
FLAG = re.compile(r"--ignorefile[=\s]+(\S+)")


def run_scripts(doc):
    """Yield text in which a `--ignorefile <path>` reference may appear.

    Two shapes wire an ignore file, and both must be seen or the gate reports a
    file as unwired when it is not (or, worse, passes a workflow that dropped
    the flag):

      * a `run:` shell script passing `--ignorefile <path>` to the trivy binary;
      * an action step whose `with:` carries the file as a dedicated input.

    `args` is scanned for the flag rather than treated as a path — it is a free
    -form command line, and other actions in this repository use `args` for
    entirely unrelated things.
    """
    for job in (doc.get("jobs") or {}).values():
        if not isinstance(job, dict):
            continue
        for step in (job.get("steps") or []):
            if not isinstance(step, dict):
                continue
            if isinstance(step.get("run"), str):
                yield step["run"]
            with_ = step.get("with")
            if not isinstance(with_, dict):
                continue
            if isinstance(with_.get("args"), str):
                yield with_["args"]
            # aquasecurity/trivy-action names it `trivyignores`, and accepts a
            # comma-separated list.
            for key in ("trivyignores", "ignorefile"):
                val = with_.get(key)
                if isinstance(val, str):
                    for part in val.split(","):
                        if part.strip():
                            yield f"--ignorefile {part.strip()}"


if not wf_dir.is_dir():
    sys.exit(f"FAIL: {wf_dir} is not a directory — the scan target moved, refusing to report clean")

wf_files = sorted(list(wf_dir.glob("*.yaml")) + list(wf_dir.glob("*.yml")))
if not wf_files:
    sys.exit(f"FAIL: no workflow files under {wf_dir} — refusing to report clean")

referenced = set()
for f in wf_files:
    try:
        doc = yaml.safe_load(f.read_text()) or {}
    except yaml.YAMLError:
        continue
    if not isinstance(doc, dict):
        continue
    for script in run_scripts(doc):
        for line in script.splitlines():
            # A shell comment inside a run: block is documentation, not wiring.
            if line.lstrip().startswith("#"):
                continue
            for m in FLAG.finditer(line):
                referenced.add(m.group(1).strip("\"'"))

if not referenced:
    fail.append(
        f"no workflow under {wf_dir} passes --ignorefile, so {ignore_path} is read by nothing.\n"
        "      Entries in it would look accepted and suppress nothing."
    )

root = ignore_path.parent if str(ignore_path.parent) != "" else pathlib.Path(".")
for ref in sorted(referenced):
    # Workflow paths are repo-root-relative; resolve against the ignore file's root.
    if not (root / ref).is_file() and not pathlib.Path(ref).is_file():
        fail.append(f"a workflow passes --ignorefile {ref}, but no such file exists.")

if referenced and ignore_path.name not in {pathlib.Path(r).name for r in referenced}:
    fail.append(
        f"{ignore_path} exists but no workflow references it "
        f"(workflows reference: {', '.join(sorted(referenced))})."
    )

# ── 2. The ignore file itself ────────────────────────────────────────────────
if not ignore_path.is_file():
    fail.append(f"{ignore_path} does not exist, but a workflow passes it to --ignorefile.")
    doc = {}
else:
    try:
        doc = yaml.safe_load(ignore_path.read_text()) or {}
    except yaml.YAMLError as e:
        fail.append(f"{ignore_path} is not valid YAML: {e}")
        doc = {}

if not isinstance(doc, dict):
    fail.append(f"{ignore_path} must be a mapping of section -> entries.")
    doc = {}

for key in doc:
    if key not in KNOWN_SECTIONS:
        fail.append(
            f"{ignore_path}: unknown section '{key}'. Trivy ignores it silently, so every "
            f"entry under it suppresses nothing. Known: {', '.join(sorted(KNOWN_SECTIONS))}."
        )

entries = 0
for section in sorted(KNOWN_SECTIONS & set(doc)):
    items = doc.get(section) or []
    if not isinstance(items, list):
        fail.append(f"{ignore_path}: section '{section}' must be a list.")
        continue
    for i, e in enumerate(items):
        where = f"{ignore_path}: {section}[{i}]"
        if not isinstance(e, dict):
            fail.append(f"{where}: each entry must be a mapping with id/statement/expired_at.")
            continue
        entries += 1
        vid = e.get("id")
        if not isinstance(vid, str) or not vid.strip():
            fail.append(f"{where}: missing 'id'.")
            vid = f"<entry {i}>"

        stmt = e.get("statement")
        if not isinstance(stmt, str) or not stmt.strip():
            fail.append(f"{where} ({vid}): missing 'statement' — say why this is accepted.")
        else:
            s = " ".join(stmt.split())
            low = s.lower().rstrip(".")
            if low in PLACEHOLDERS or re.match(r"^(todo|tbd|fixme|xxx)\b", low):
                fail.append(f"{where} ({vid}): 'statement' is a placeholder ({s!r}).")
            elif len(s) < min_statement:
                fail.append(
                    f"{where} ({vid}): 'statement' is {len(s)} chars, need >= {min_statement}. "
                    "Say what the finding is, why it is accepted, and what would change that."
                )

        exp = e.get("expired_at")
        if exp is None:
            fail.append(
                f"{where} ({vid}): missing 'expired_at' — an acceptance with no end date "
                "is a permanent one."
            )
            continue
        if isinstance(exp, datetime.datetime):
            exp = exp.date()
        elif isinstance(exp, str):
            try:
                exp = datetime.date.fromisoformat(exp.strip())
            except ValueError:
                fail.append(f"{where} ({vid}): 'expired_at' {exp!r} is not an ISO date (YYYY-MM-DD).")
                continue
        elif not isinstance(exp, datetime.date):
            fail.append(f"{where} ({vid}): 'expired_at' must be a YYYY-MM-DD date, got {exp!r}.")
            continue

        if exp < today:
            fail.append(
                f"{where} ({vid}): 'expired_at' {exp} has lapsed. Trivy already stopped "
                "honouring it, so the entry is dead weight — renew it deliberately or delete it."
            )
        elif (exp - today).days > max_days:
            fail.append(
                f"{where} ({vid}): 'expired_at' {exp} is {(exp - today).days} days out, "
                f"limit is {max_days}. 'Accepted' must not mean 'forever'."
            )

if fail:
    print("Container-scan ignore list is not reviewable:", file=sys.stderr)
    for f_ in fail:
        print(f"  - {f_}", file=sys.stderr)
    print("", file=sys.stderr)
    print("Every accepted finding must say why (statement) and when the acceptance", file=sys.stderr)
    print("lapses (expired_at), so the list cannot silently become permanent.", file=sys.stderr)
    sys.exit(1)

print(f"trivyignore ok ({entries} accepted finding(s), {len(wf_files)} workflow file(s) scanned)")
PY
}

self_test() {
	local tmp rc
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	mkdir -p "$tmp/wf"
	local future past far
	future=$(python3 -c 'import datetime;print(datetime.date.today()+datetime.timedelta(days=30))')
	past=$(python3 -c 'import datetime;print(datetime.date.today()-datetime.timedelta(days=1))')
	far=$(python3 -c 'import datetime;print(datetime.date.today()+datetime.timedelta(days=400))')

	# A workflow that wires the ignore file, in the shape the real ones use.
	printf 'jobs:\n  scan:\n    steps:\n      - run: |\n          trivy image --ignorefile .trivyignore.yaml alpine\n' \
		>"$tmp/wf/scan.yaml"

	ok() { # <yaml-body> <label>
		printf '%s' "$1" >"$tmp/.trivyignore.yaml"
		check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 ||
			{ echo "self-test FAIL: rejected $2" >&2; return 1; }
	}
	bad() { # <yaml-body> <label>
		printf '%s' "$1" >"$tmp/.trivyignore.yaml"
		rc=0; check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 || rc=$?
		[ "$rc" -ne 0 ] || { echo "self-test FAIL: accepted $2" >&2; return 1; }
	}

	local good_stmt="Debian marks this will_not_fix; the affected code path is not linked in."

	# ── Accepted shapes ──────────────────────────────────────────────────
	ok "vulnerabilities: []
" "an empty list (no exceptions is a valid state)" || return 1

	ok "vulnerabilities:
  - id: CVE-2023-45853
    statement: \"$good_stmt\"
    expired_at: $future
" "a well-formed entry" || return 1

	# Trivy accepts a quoted date too; both must pass.
	ok "vulnerabilities:
  - id: CVE-2023-45853
    statement: \"$good_stmt\"
    expired_at: \"$future\"
" "a quoted ISO date" || return 1

	# ── Rejected shapes. Each must fail ON ITS OWN, or one catch masks the rest.
	bad "vulnerabilities:
  - id: CVE-2023-45853
    expired_at: $future
" "an entry with no statement" || return 1

	# Long enough to clear the length floor, so ONLY the placeholder rule can
	# catch it. A short "TODO" would be rejected by the length check instead, and
	# this case would pass while the placeholder rule was broken.
	bad "vulnerabilities:
  - id: CVE-2023-45853
    statement: \"TODO: work out whether this one actually reaches us, sometime\"
    expired_at: $future
" "a placeholder statement long enough to clear the length floor" || return 1

	bad "vulnerabilities:
  - id: CVE-2023-45853
    statement: accepted
    expired_at: $future
" "a bare placeholder statement" || return 1

	bad "vulnerabilities:
  - id: CVE-2023-45853
    statement: not exploitable
    expired_at: $future
" "a statement below the length floor" || return 1

	bad "vulnerabilities:
  - id: CVE-2023-45853
    statement: \"$good_stmt\"
" "an entry with no expired_at" || return 1

	bad "vulnerabilities:
  - id: CVE-2023-45853
    statement: \"$good_stmt\"
    expired_at: $past
" "a lapsed expired_at" || return 1

	bad "vulnerabilities:
  - id: CVE-2023-45853
    statement: \"$good_stmt\"
    expired_at: $far
" "an expired_at parked beyond the limit" || return 1

	bad "vulnerabilities:
  - id: CVE-2023-45853
    statement: \"$good_stmt\"
    expired_at: someday
" "an unparseable expired_at" || return 1

	bad "vulnerabilities:
  - statement: \"$good_stmt\"
    expired_at: $future
" "an entry with no id" || return 1

	# A typo'd section suppresses nothing while looking configured.
	bad "vulnerability:
  - id: CVE-2023-45853
    statement: \"$good_stmt\"
    expired_at: $future
" "an unknown top-level section" || return 1

	# ── Wiring. Both ends, because neither proves the chain alone. ───────
	printf 'vulnerabilities: []\n' >"$tmp/.trivyignore.yaml"

	# No workflow passes --ignorefile: the file is read by nothing.
	printf 'jobs:\n  scan:\n    steps:\n      - run: trivy image alpine\n' >"$tmp/wf/scan.yaml"
	rc=0; check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: accepted an ignore file no workflow reads" >&2; return 1; }

	# Only a SHELL COMMENT mentions the flag — that is documentation, not wiring.
	printf 'jobs:\n  scan:\n    steps:\n      - run: |\n          # we used to pass --ignorefile .trivyignore.yaml here\n          trivy image alpine\n' \
		>"$tmp/wf/scan.yaml"
	rc=0; check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: a commented-out --ignorefile counted as wiring" >&2; return 1; }

	# The action-input shape (aquasecurity/trivy-action `trivyignores:`) counts as
	# wiring, including as one entry of a comma-separated list.
	printf 'jobs:\n  scan:\n    steps:\n      - uses: aquasecurity/trivy-action@abc\n        with:\n          trivyignores: .trivyignore.yaml\n' \
		>"$tmp/wf/scan.yaml"
	check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 ||
		{ echo "self-test FAIL: a trivyignores: action input did not count as wiring" >&2; return 1; }
	printf 'jobs:\n  scan:\n    steps:\n      - uses: aquasecurity/trivy-action@abc\n        with:\n          trivyignores: "other.yaml,.trivyignore.yaml"\n' \
		>"$tmp/wf/scan.yaml"
	printf 'x\n' >"$tmp/other.yaml"
	check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 ||
		{ echo "self-test FAIL: a comma-separated trivyignores list did not count as wiring" >&2; return 1; }
	rm -f "$tmp/other.yaml"

	# An unrelated action's `args:` is a free-form command line, NOT a path. An
	# earlier version treated the whole value as an ignore-file path and failed
	# against the real repository, where other actions use `args` for their own
	# purposes. It must be scanned for the flag, and count as wiring only if the
	# flag is actually in it.
	printf 'jobs:\n  a:\n    steps:\n      - uses: some/action@abc\n        with:\n          args: --config release\n      - run: |\n          trivy image --ignorefile .trivyignore.yaml alpine\n' \
		>"$tmp/wf/scan.yaml"
	check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 ||
		{ echo "self-test FAIL: an unrelated action's args: was misread as an ignore-file path" >&2; return 1; }

	# ...but a flag genuinely inside `args:` DOES count.
	printf 'jobs:\n  a:\n    steps:\n      - uses: some/action@abc\n        with:\n          args: image --ignorefile .trivyignore.yaml alpine\n' \
		>"$tmp/wf/scan.yaml"
	check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 ||
		{ echo "self-test FAIL: --ignorefile inside args: did not count as wiring" >&2; return 1; }

	# The workflow points at a file that does not exist (a rename on either end).
	printf 'jobs:\n  scan:\n    steps:\n      - run: |\n          trivy image --ignorefile .trivyignore-renamed.yaml alpine\n' \
		>"$tmp/wf/scan.yaml"
	rc=0; check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: accepted a --ignorefile path that does not exist" >&2; return 1; }

	# A missing or empty workflow dir must NOT report clean.
	printf 'jobs:\n  scan:\n    steps:\n      - run: |\n          trivy image --ignorefile .trivyignore.yaml alpine\n' \
		>"$tmp/wf/scan.yaml"
	rc=0; check "$tmp/gone" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: a missing workflow directory reported clean" >&2; return 1; }
	mkdir -p "$tmp/empty"
	rc=0; check "$tmp/empty" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: an empty workflow directory reported clean" >&2; return 1; }

	# A wired workflow with the ignore file deleted.
	rm -f "$tmp/.trivyignore.yaml"
	rc=0; check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: a wired but missing ignore file reported clean" >&2; return 1; }

	echo "check-trivyignore-entries self-test: ok"
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
if [ "$#" -eq 2 ]; then check "$1" "$2"; exit $?; fi
cd "$(dirname "$0")/.."
check ".github/workflows" ".trivyignore.yaml"
