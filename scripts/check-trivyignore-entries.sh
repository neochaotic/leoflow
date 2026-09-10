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
# The wiring assertion is PER INVOCATION, not per repository. An earlier version
# asked only whether SOME workflow somewhere passed an ignore file, which is a
# question the repository answers "yes" to for as long as any one scan still
# carries the flag: deleting `trivyignores:` from the BLOCKING gate on its own
# left this gate green, because the daily reporting scan still had it. That is
# the one mutation that mattered and the only one the old shape could not see.
# So every step that runs a trivy SCAN must carry an ignore-file reference of
# its own — `--ignorefile` on the command line, the action's `trivyignores:` /
# `ignorefile:` input, or `TRIVY_IGNOREFILE` in the environment.
#
# A scan that must run with no suppressions (the unfiltered inventory keeps the
# complete picture precisely so that no filter this policy applies is the only
# copy of the data) declares itself, in the step or in the comment above it:
#
#   # trivy-ignorefile-exempt: <why this scan must apply no suppressions>
#
# The reason is mandatory and length-checked, so an exemption is an argument
# somebody wrote, not a flag somebody dropped. The declaration is per step.
#
# The tree is PARSED, not grepped. `--ignorefile` lives inside a `run:` block, so
# the raw text also contains it in shell comments and in this script's own
# documentation; a grep would count those as wiring and pass a workflow that has
# none. Shell-comment lines inside the parsed `run:` scripts are dropped for the
# same reason, `\`-continuations are joined before the line is read, and `trivy`
# counts as an invocation only in command position — `install /tmp/trivy
# /usr/local/bin/trivy` installs the binary, it does not scan anything.
#
# Classification fails CLOSED: a trivy subcommand this script does not recognise
# is an error, not a pass. A gate that silently ignores what it cannot parse is
# the failure mode this whole file exists to prevent.
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

# ── 1. Every trivy scan must be wired to the ignore file ─────────────────────
FLAG = re.compile(r"--ignorefile[=\s]+(\S+)")
EXEMPT = re.compile(r"trivy-ignorefile-exempt:\s*(\S.*)")

# Subcommands that read an image/filesystem/repository and report findings —
# the ones an ignore file applies to.
SCAN_SUBCOMMANDS = {"image", "i", "fs", "filesystem", "rootfs", "repo",
                    "repository", "config", "conf", "k8s", "vm", "sbom", "aws"}
# Subcommands that do not scan anything, so an ignore file is meaningless.
# `convert` re-renders a report that was already filtered when it was produced.
NON_SCAN_SUBCOMMANDS = {"version", "convert", "clean", "module", "plugin",
                        "server", "completion", "registry", "help"}
NON_SCAN_FLAGS = {"--version", "-v", "--help", "-h"}
# `trivy image --download-db-only` warms the vulnerability database. It names a
# scan subcommand and scans nothing.
MAINTENANCE_FLAGS = ("--download-db-only", "--download-java-db-only", "--reset",
                     "--clear-cache", "--generate-default-trivy-yaml")
# Words that may precede the real command word without changing what it is.
WRAPPERS = {"sudo", "env", "command", "exec", "time", "nice", "then", "do",
            "else", "!"}
ASSIGNMENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=")
# Splits a shell line into the segments a command can start in.
SEGMENT = re.compile(r"\|\||&&|[|;&()]")


def logical_lines(script):
    """Yield each shell line of `script` with `\\`-continuations joined.

    A `--ignorefile` two lines below the `trivy` that consumes it is the same
    invocation, and the real workflows are written that way. Reading raw lines
    would report the flag as missing on the command and as orphaned on its own.
    Shell comments are dropped: an invocation someone commented out is not one.
    """
    buf = ""
    for raw in script.splitlines():
        stripped = raw.strip()
        if not buf:
            if not stripped or stripped.startswith("#"):
                continue
            buf = stripped
        else:
            buf += " " + stripped
        if buf.endswith("\\"):
            buf = buf[:-1].rstrip()
            continue
        yield buf
        buf = ""
    if buf:
        yield buf


def trivy_invocations(line):
    """Yield each segment of `line` in which trivy is the command being run.

    Command position matters. `install -m 0755 /tmp/trivy /usr/local/bin/trivy`
    mentions trivy twice and runs it zero times; treating either mention as an
    invocation would demand an ignore file from the step that installs the
    scanner.
    """
    for segment in SEGMENT.split(line):
        toks = segment.split()
        while toks and (toks[0] in WRAPPERS or ASSIGNMENT.match(toks[0])):
            toks.pop(0)
        if not toks:
            continue
        if pathlib.PurePosixPath(toks[0].strip("\"'")).name != "trivy":
            continue
        yield segment, toks[1:]


def classify(args):
    """Return "scan", "other", or None when the subcommand is unrecognised."""
    for tok in args:
        if tok in MAINTENANCE_FLAGS or tok.split("=", 1)[0] in MAINTENANCE_FLAGS:
            return "other"
    for tok in args:
        bare = tok.strip("\"'")
        if bare in NON_SCAN_FLAGS:
            return "other"
        if bare.startswith("-"):
            continue
        if bare in SCAN_SUBCOMMANDS:
            return "scan"
        if bare in NON_SCAN_SUBCOMMANDS:
            return "other"
        # A bare word that is not a subcommand we know about. Fail closed.
        return None
    # `trivy` with no subcommand at all (a bare path, or `trivy` alone).
    return "other"


def step_span(lines, step):
    """Raw workflow lines covering a step, plus the comment block above it.

    An exemption is an argument a human wrote next to the scan it excuses, and
    the natural place to write it for an action step — which has no `run:` body
    — is the comment immediately above. YAML comments are gone by the time the
    document is parsed, so the raw text is read back over the step's own range.
    """
    start = step.get("__line__")
    end = step.get("__endline__")
    if not isinstance(start, int) or not isinstance(end, int):
        return []
    first = lines[start - 1]
    indent = len(first) - len(first.lstrip())
    # A block mapping's end mark sits on the FIRST TOKEN AFTER it, so the raw
    # range runs into the next step. Stop at the first line that dedents back to
    # the step's own level, or an exemption written for one step would excuse
    # the step above it.
    stop = end
    for j in range(start, min(end, len(lines))):
        line = lines[j]
        if line.strip() and (len(line) - len(line.lstrip())) <= indent:
            stop = j
            break
    i = start - 1  # 0-based index of the step's first line
    while i - 1 >= 0 and lines[i - 1].lstrip().startswith("#"):
        i -= 1
    return lines[i:stop]


def wiring_refs(step, job, doc):
    """Ignore-file paths this step's scans would actually use, minus the CLI flag."""
    refs = []
    for scope in (doc, job, step):
        env = scope.get("env") if isinstance(scope, dict) else None
        if isinstance(env, dict) and isinstance(env.get("TRIVY_IGNOREFILE"), str):
            refs.append(env["TRIVY_IGNOREFILE"].strip("\"'"))
    with_ = step.get("with")
    if isinstance(with_, dict):
        # aquasecurity/trivy-action names it `trivyignores`, and accepts a
        # comma-separated list. `args` is a free-form command line — other
        # actions in this repository use it for entirely unrelated things — so
        # it is scanned for the flag rather than read as a path.
        for key in ("trivyignores", "ignorefile"):
            val = with_.get(key)
            if isinstance(val, str):
                refs += [p.strip().strip("\"'") for p in val.split(",") if p.strip()]
        if isinstance(with_.get("args"), str):
            refs += [m.group(1).strip("\"'") for m in FLAG.finditer(with_["args"])]
    return refs


class LineLoader(yaml.SafeLoader):
    """SafeLoader that records where each mapping started and ended."""


def _mapping_with_lines(loader, node, deep=False):
    mapping = yaml.SafeLoader.construct_mapping(loader, node, deep=deep)
    mapping["__line__"] = node.start_mark.line + 1
    mapping["__endline__"] = node.end_mark.line + 1
    return mapping


LineLoader.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, _mapping_with_lines
)

if not wf_dir.is_dir():
    sys.exit(f"FAIL: {wf_dir} is not a directory — the scan target moved, refusing to report clean")

wf_files = sorted(list(wf_dir.glob("*.yaml")) + list(wf_dir.glob("*.yml")))
if not wf_files:
    sys.exit(f"FAIL: no workflow files under {wf_dir} — refusing to report clean")

referenced = set()
scans_seen = 0
for f in wf_files:
    text = f.read_text()
    lines = text.splitlines()
    try:
        doc = yaml.load(text, Loader=LineLoader) or {}
    except yaml.YAMLError:
        continue
    if not isinstance(doc, dict):
        continue
    for job_id, job in (doc.get("jobs") or {}).items():
        if not isinstance(job, dict):
            continue
        for step in (job.get("steps") or []):
            if not isinstance(step, dict):
                continue
            where = f"{f.name}: job '{job_id}', step '{step.get('name') or step.get('uses') or step.get('id') or '?'}'"

            # What this step would pass to trivy without the CLI flag.
            step_refs = wiring_refs(step, job, doc)
            invocations = []          # (label, refs-for-this-invocation)
            uses = step.get("uses")
            if isinstance(uses, str) and re.search(r"trivy", uses, re.I):
                invocations.append(("the trivy action", list(step_refs)))
            if isinstance(step.get("run"), str):
                for line in logical_lines(step["run"]):
                    for segment, args in trivy_invocations(line):
                        kind = classify(args)
                        if kind is None:
                            fail.append(
                                f"{where}: cannot tell whether `{segment.strip()}` scans anything. "
                                "Teach this gate the subcommand rather than letting it guess."
                            )
                            continue
                        if kind != "scan":
                            continue
                        cli = [m.group(1).strip("\"'") for m in FLAG.finditer(segment)]
                        invocations.append((f"`{segment.strip()}`", step_refs + cli))

            if not invocations:
                continue
            exemption = None
            for raw in step_span(lines, step):
                m = EXEMPT.search(raw)
                if m:
                    exemption = " ".join(m.group(1).split()).rstrip("\"'")
                    break

            for label, refs in invocations:
                scans_seen += 1
                if refs:
                    referenced.update(refs)
                    continue
                if exemption is None:
                    fail.append(
                        f"{where}: {label} runs a scan with no ignore file.\n"
                        f"      Pass --ignorefile {ignore_path.name} (or the action's `trivyignores:` input),\n"
                        "      or declare why this scan must apply no suppressions with a\n"
                        "      `# trivy-ignorefile-exempt: <reason>` comment on the step."
                    )
                elif len(exemption) < min_statement:
                    fail.append(
                        f"{where}: 'trivy-ignorefile-exempt' reason is {len(exemption)} chars, "
                        f"need >= {min_statement}. Say why this scan must see every finding."
                    )

if scans_seen == 0:
    fail.append(
        f"no workflow under {wf_dir} runs a trivy scan, so {ignore_path} is read by nothing.\n"
        "      Entries in it would look accepted and suppress nothing."
    )

root = ignore_path.parent if str(ignore_path.parent) != "" else pathlib.Path(".")
for ref in sorted(referenced):
    # Workflow paths are repo-root-relative; resolve against the ignore file's root.
    if not (root / ref).is_file() and not pathlib.Path(ref).is_file():
        fail.append(f"a workflow passes --ignorefile {ref}, but no such file exists.")

if scans_seen and not referenced:
    fail.append(
        f"every trivy scan under {wf_dir} is exempt from the ignore file, so {ignore_path} "
        "is read by nothing."
    )
elif referenced and ignore_path.name not in {pathlib.Path(r).name for r in referenced}:
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

print(
    f"trivyignore ok ({entries} accepted finding(s), {scans_seen} trivy scan(s) wired, "
    f"{len(wf_files)} workflow file(s) scanned)"
)
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

	wok() { # <label> — the workflow dir as currently written must pass
		check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 ||
			{ echo "self-test FAIL: rejected $1" >&2; return 1; }
	}
	wbad() { # <label> — the workflow dir as currently written must fail
		rc=0; check "$tmp/wf" "$tmp/.trivyignore.yaml" >/dev/null 2>&1 || rc=$?
		[ "$rc" -ne 0 ] || { echo "self-test FAIL: accepted $1" >&2; return 1; }
	}

	# No workflow passes --ignorefile: the file is read by nothing.
	printf 'jobs:\n  scan:\n    steps:\n      - run: trivy image alpine\n' >"$tmp/wf/scan.yaml"
	wbad "an ignore file no workflow reads" || return 1

	# ── The mutation the repository-wide check could not see ────────────
	# Two workflows, both scanning; the flag is dropped from ONE. The old shape
	# asked "does SOME workflow pass --ignorefile" and answered yes, so deleting
	# the flag from the BLOCKING gate alone left this gate green. Every scan must
	# now carry its own reference.
	printf 'jobs:\n  daily:\n    steps:\n      - run: |\n          trivy image --ignorefile .trivyignore.yaml alpine\n' \
		>"$tmp/wf/daily.yaml"
	printf 'jobs:\n  gate:\n    steps:\n      - run: |\n          trivy image --ignorefile .trivyignore.yaml --exit-code 1 alpine\n' \
		>"$tmp/wf/scan.yaml"
	wok "two workflows that both wire the ignore file" || return 1
	printf 'jobs:\n  gate:\n    steps:\n      - run: |\n          trivy image --exit-code 1 alpine\n' \
		>"$tmp/wf/scan.yaml"
	wbad "the blocking gate dropping --ignorefile while another workflow keeps it" || return 1
	rm -f "$tmp/wf/daily.yaml"

	# Same failure one level down: two steps in ONE job, only one wired.
	printf 'jobs:\n  scan:\n    steps:\n      - name: report\n        run: |\n          trivy image --ignorefile .trivyignore.yaml --format sarif alpine\n      - name: gate\n        run: |\n          trivy image --exit-code 1 alpine\n' \
		>"$tmp/wf/scan.yaml"
	wbad "an unwired scan step alongside a wired one in the same job" || return 1

	# The action shape has the same per-step rule: two trivy-action steps, one
	# missing `trivyignores:`. This is the literal mutation reviewed on #1034.
	printf 'jobs:\n  scan:\n    steps:\n      - name: report\n        uses: aquasecurity/trivy-action@abc\n        with:\n          trivyignores: .trivyignore.yaml\n      - name: gate\n        uses: aquasecurity/trivy-action@abc\n        with:\n          exit-code: 1\n' \
		>"$tmp/wf/scan.yaml"
	wbad "a trivy-action gate step with no trivyignores: input" || return 1

	# ── Declared exemptions ─────────────────────────────────────────────
	# A scan that must apply no suppressions says so, with a reason. The shape is
	# the real one: a wired gate plus an exempt unfiltered inventory beside it.
	local wired_step='      - name: gate\n        run: |\n          trivy image --ignorefile .trivyignore.yaml alpine\n'
	# shellcheck disable=SC2059 # the format string is built above, on purpose.
	printf "jobs:\n  scan:\n    steps:\n${wired_step}      # trivy-ignorefile-exempt: the unfiltered inventory is the copy of record and must show every finding\n      - name: inventory\n        run: |\n          trivy image --format json alpine\n" \
		>"$tmp/wf/scan.yaml"
	wok "a scan with a declared, argued exemption" || return 1

	# ...but the reason is mandatory and length-checked, or the marker becomes a
	# one-word way to switch the gate off.
	# shellcheck disable=SC2059
	printf "jobs:\n  scan:\n    steps:\n${wired_step}      # trivy-ignorefile-exempt: later\n      - name: inventory\n        run: |\n          trivy image --format json alpine\n" \
		>"$tmp/wf/scan.yaml"
	wbad "an exemption with no real reason" || return 1

	# An exemption on one step must not excuse the step ABOVE it. The end mark of
	# a YAML block mapping lands on the following step's first line, so a span
	# read naively leaks the marker upwards. A wired step is kept in the file so
	# that the "every scan is exempt" rule cannot fail this case for the wrong
	# reason and hide a leak.
	# shellcheck disable=SC2059
	printf "jobs:\n  scan:\n    steps:\n${wired_step}      - name: unwired\n        run: |\n          trivy image --exit-code 1 alpine\n      # trivy-ignorefile-exempt: the unfiltered inventory is the copy of record and must show every finding\n      - name: inventory\n        run: |\n          trivy image --format json alpine\n" \
		>"$tmp/wf/scan.yaml"
	wbad "an unwired scan sitting above another step's exemption" || return 1

	# Every scan exempt means the ignore file is still read by nothing.
	printf 'jobs:\n  scan:\n    steps:\n      # trivy-ignorefile-exempt: the unfiltered inventory is the copy of record and must show every finding\n      - name: inventory\n        run: |\n          trivy image --format json alpine\n' \
		>"$tmp/wf/scan.yaml"
	printf 'vulnerabilities:\n  - id: CVE-2023-45853\n    statement: "%s"\n    expired_at: %s\n' "$good_stmt" "$future" \
		>"$tmp/.trivyignore.yaml"
	wbad "entries in an ignore file whose every scan is exempt" || return 1
	printf 'vulnerabilities: []\n' >"$tmp/.trivyignore.yaml"

	# ── Classification ──────────────────────────────────────────────────
	# Installing, warming the database, printing the version and re-rendering a
	# report are not scans, and must not demand an ignore file. All four appear
	# in the real image-scan workflow.
	printf 'jobs:\n  scan:\n    steps:\n      - name: setup\n        run: |\n          tar -xzf /tmp/trivy.tgz -C /tmp trivy\n          sudo install -m 0755 /tmp/trivy /usr/local/bin/trivy\n          trivy --version\n          trivy image --download-db-only\n          trivy convert --format sarif --output out.sarif out.json\n      - name: gate\n        run: |\n          trivy image --ignorefile .trivyignore.yaml alpine\n' \
		>"$tmp/wf/scan.yaml"
	wok "install, --version, --download-db-only and convert as non-scans" || return 1

	# A `\`-continuation is one invocation: the flag two lines below the command
	# still wires it. Every real scan in this repository is written that way.
	printf 'jobs:\n  scan:\n    steps:\n      - run: |\n          trivy image --quiet \\\n            --ignore-unfixed \\\n            --ignorefile .trivyignore.yaml \\\n            alpine\n' \
		>"$tmp/wf/scan.yaml"
	wok "an invocation split across continuation lines" || return 1

	# An unrecognised subcommand is an error, not a pass. A gate that shrugs at
	# what it cannot parse is the failure mode this file exists to prevent. The
	# wired step beside it keeps the "no trivy scan anywhere" rule from failing
	# this case for the wrong reason.
	# shellcheck disable=SC2059
	printf "jobs:\n  scan:\n    steps:\n${wired_step}      - name: future\n        run: |\n          trivy futurecmd --ignorefile .trivyignore.yaml alpine\n" \
		>"$tmp/wf/scan.yaml"
	wbad "a trivy subcommand the gate cannot classify" || return 1

	# TRIVY_IGNOREFILE in the environment wires a scan just as the flag does.
	printf 'jobs:\n  scan:\n    steps:\n      - env:\n          TRIVY_IGNOREFILE: .trivyignore.yaml\n        run: |\n          trivy image alpine\n' \
		>"$tmp/wf/scan.yaml"
	wok "an ignore file passed through TRIVY_IGNOREFILE" || return 1

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

	# ...but a flag genuinely inside a TRIVY action's `args:` DOES count.
	printf 'jobs:\n  a:\n    steps:\n      - uses: aquasecurity/trivy-action@abc\n        with:\n          args: image --ignorefile .trivyignore.yaml alpine\n' \
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
