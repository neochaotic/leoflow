#!/usr/bin/env bash
# Container images pulled by a workflow must come from a registry that does not
# rate-limit anonymous pulls by source IP.
#
# GitHub-hosted runners share a small pool of egress IPs across every customer.
# `public.ecr.aws` returned `toomanyrequests: Rate exceeded` on ~13% of main
# runs, and bare Docker Hub is tighter still (100 pulls / 6h per IPv4 or IPv6
# /64). Both failures land in `Initialize containers` — BEFORE actions/checkout
# and before any step we write — so no retry, cache or guard inside the job can
# reach them, and they fire even on pull requests that change only markdown
# (#1007).
#
# This is an ALLOWLIST, not a denylist. A denylist naming public.ecr.aws would
# wave through `image: postgres:16`, which is the likeliest drift (someone
# pastes it out of docker-compose.dev.yaml) and is rate-limited by the same
# mechanism.
#
# The tree is PARSED, not grepped. An earlier version of this file grepped for
# `image:\s*public\.ecr\.aws` and was close to inert: it missed an env
# indirection, a folded scalar, a job-level `container:`, an uppercase key, and
# a `docker pull` in a run: step — five real references, exit 0 — while failing
# on a COMMENT that mentioned the registry. Same fail-open shape
# check-lite-prepull-matches-compose.sh already documents and fixes by parsing.
#
# Usage: scripts/check-service-image-registry.sh [--self-test]
#        scripts/check-service-image-registry.sh <workflow-dir> [<workflow-dir>...]
set -euo pipefail

scan() { # <dir>...
	python3 - "$@" <<'PY'
import sys, pathlib, re
try:
    import yaml
except ImportError:
    sys.exit("PyYAML is required (pip install pyyaml)")

# Registries whose anonymous pulls are not rate-limited by shared source IP.
# ghcr.io and the mirror are fine; a bare `postgres:16` is Docker Hub and is not.
# quay.io is a distinct registry with its own limits, not the shared-IP
# Docker Hub / ECR Public pool this gate exists for.
ALLOWED_PREFIXES = ("mirror.gcr.io/", "ghcr.io/", "quay.io/")

MATRIX_REF = re.compile(r"^[$][{][{]\s*matrix[.]([A-Za-z0-9_-]+)\s*[}][}]$")

def resolve(img, job):
    """Expand `${{ matrix.<key> }}` against this job's literal matrix values.

    Without this the gate can only say "unresolvable" for the shape release.yaml
    actually uses (a distro matrix feeding `container:`), which is the shape
    where a flake is most expensive. Returns a list, because a matrix is a fan-out.
    """
    m = MATRIX_REF.match(img.strip())
    if not m:
        return [img]
    key = m.group(1)
    matrix = ((job.get("strategy") or {}).get("matrix") or {})
    vals = []
    for entry in (matrix.get("include") or []):
        if isinstance(entry, dict) and isinstance(entry.get(key), str):
            vals.append(entry[key])
    direct = matrix.get(key)
    if isinstance(direct, list):
        vals += [v for v in direct if isinstance(v, str)]
    return vals or [img]

def offenders(doc, where):
    """Yield (where, image) for every container image the runner pulls before
    our steps: job-level `container:` and every `services.*.image`."""
    for job_id, job in (doc.get("jobs") or {}).items():
        if not isinstance(job, dict):
            continue
        c = job.get("container")
        raw = c if isinstance(c, str) else (c.get("image") if isinstance(c, dict) else None)
        if isinstance(raw, str):
            for img in resolve(raw, job):
                yield (f"{where}: jobs.{job_id}.container", img)
        for svc_id, svc in (job.get("services") or {}).items():
            raw = svc.get("image") if isinstance(svc, dict) else svc
            if isinstance(raw, str):
                for img in resolve(raw, job):
                    yield (f"{where}: jobs.{job_id}.services.{svc_id}.image", img)

bad, scanned = [], 0
for arg in sys.argv[1:]:
    d = pathlib.Path(arg)
    if not d.is_dir():
        sys.exit(f"not a directory: {d} — the scan target moved, refusing to report clean")
    files = sorted(list(d.glob("*.yaml")) + list(d.glob("*.yml")))
    if not files:
        sys.exit(f"no workflow files under {d} — refusing to report clean")
    for f in files:
        scanned += 1
        doc = yaml.safe_load(f.read_text()) or {}
        if not isinstance(doc, dict):
            continue
        for where, img in offenders(doc, f.name):
            # An expression is only as good as what it resolves to; we cannot
            # evaluate it here, so flag it for a human rather than pass it.
            if re.search(r"\$\{\{", img):
                bad.append((where, img, "unresolvable expression — inline the image or add it here"))
            elif not img.startswith(ALLOWED_PREFIXES):
                bad.append((where, img, "not from an allowed registry"))

if bad:
    print("Container images the runner pulls before any step, from a registry", file=sys.stderr)
    print("that rate-limits anonymous pulls by shared source IP (#1007):", file=sys.stderr)
    for where, img, why in bad:
        print(f"  {where} = {img}\n      {why}", file=sys.stderr)
    print("", file=sys.stderr)
    print(f"Allowed prefixes: {', '.join(ALLOWED_PREFIXES)}", file=sys.stderr)
    print("These pulls happen in 'Initialize containers', so no retry can rescue them.", file=sys.stderr)
    sys.exit(1)
print(f"container registries ok ({scanned} workflow file(s) scanned)")
PY
}

self_test() {
	local tmp rc; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' RETURN
	mkdir -p "$tmp/wf"

	printf 'jobs:\n  a:\n    services:\n      db:\n        image: mirror.gcr.io/library/postgres:16\n' >"$tmp/wf/ok.yaml"
	scan "$tmp/wf" >/dev/null || { echo "self-test FAIL: allowed registry rejected" >&2; return 1; }

	# Every shape the grep version missed. Each must fail ON ITS OWN, or a
	# single catch would mask the other four.
	local shapes=(
'jobs:\n  a:\n    services:\n      db:\n        image: public.ecr.aws/docker/library/postgres:16\n|plain service image'
'jobs:\n  a:\n    container: ubuntu:24.04\n|job-level container (bare Docker Hub)'
'jobs:\n  a:\n    container:\n      image: public.ecr.aws/docker/library/ubuntu:24.04\n|container mapping'
'jobs:\n  a:\n    services:\n      db:\n        image: >-\n          public.ecr.aws/docker/library/postgres:16\n|folded scalar'
'jobs:\n  a:\n    services:\n      db:\n        image: ${{ env.PG_IMAGE }}\n|unresolvable expression'
'jobs:\n  a:\n    services:\n      db:\n        image: postgres:16\n|bare Docker Hub'
	)
	local entry body label
	for entry in "${shapes[@]}"; do
		body="${entry%%|*}"; label="${entry##*|}"
		rm -f "$tmp/wf"/*.yaml
		# shellcheck disable=SC2059
		printf "$body" >"$tmp/wf/bad.yaml"
		rc=0; scan "$tmp/wf" >/dev/null 2>&1 || rc=$?
		[ "$rc" -ne 0 ] || { echo "self-test FAIL: accepted $label" >&2; return 1; }
	done

	# A COMMENT naming the registry must NOT fail. The grep version failed here,
	# and its self-test claimed otherwise because the fixture comment happened
	# to lack the literal "image:". Use a comment that DOES contain it.
	rm -f "$tmp/wf"/*.yaml
	printf '# we used to write image: public.ecr.aws/docker/library/postgres:16 here\njobs:\n  a:\n    services:\n      db:\n        image: mirror.gcr.io/library/postgres:16\n' >"$tmp/wf/comment.yaml"
	scan "$tmp/wf" >/dev/null 2>&1 || { echo "self-test FAIL: a comment naming the registry failed the gate" >&2; return 1; }

	# A missing or empty scan target must NOT report clean — that is how a gate
	# silently stops gating after a directory move.
	rc=0; scan "$tmp/gone" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: a missing directory reported clean" >&2; return 1; }
	mkdir -p "$tmp/empty"
	rc=0; scan "$tmp/empty" >/dev/null 2>&1 || rc=$?
	[ "$rc" -ne 0 ] || { echo "self-test FAIL: an empty directory reported clean" >&2; return 1; }

	echo "check-service-image-registry self-test: ok"
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
if [ "$#" -gt 0 ]; then scan "$@"; exit $?; fi
scan "$(dirname "$0")/../.github/workflows"
