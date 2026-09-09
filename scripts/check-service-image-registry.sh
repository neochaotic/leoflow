#!/usr/bin/env bash
# Service-container images must not come from a registry that rate-limits
# anonymous pulls by source IP.
#
# GitHub-hosted runners share a small pool of egress IPs across every customer,
# so `public.ecr.aws` returns `toomanyrequests: Rate exceeded` on a schedule we
# do not control. That failure lands in `Initialize containers` — BEFORE
# actions/checkout and before any step we write — so no retry, cache or guard
# inside the job can touch it, and it fires even on pull requests that change
# only markdown. It cost ~13% of main runs (#1007).
#
# The replacement is mirror.gcr.io, Google's unauthenticated pull-through cache
# for Docker Hub official images. This trades one third-party for another, and
# that is the honest description of it: what changes is the failure mode, since
# mirror.gcr.io does not impose the shared-source-IP anonymous limit that is
# actually biting us.
#
# Two copies of a registry hostname with nothing between them is how these rot,
# so this is the gate. If a genuinely better mirror appears, change ALLOWED and
# the swap becomes one edit.
#
# Usage: scripts/check-service-image-registry.sh [--self-test]
set -euo pipefail

# Registries that must never appear as a service-container image.
BANNED_RE='public\.ecr\.aws'

scan() { # <dir>
	local dir="$1" hits
	hits=$(grep -rnE "image:[[:space:]]*[\"']?${BANNED_RE}" "$dir" || true)
	if [ -n "$hits" ]; then
		echo "Service-container images from a source-IP-rate-limited registry (#1007):" >&2
		echo "$hits" >&2
		echo >&2
		echo "Use mirror.gcr.io/library/<image> instead. These pulls happen in" >&2
		echo "'Initialize containers', before any step, so a retry cannot rescue them." >&2
		return 1
	fi
	return 0
}

self_test() {
	local tmp rc
	tmp=$(mktemp -d); trap 'rm -rf "$tmp"' RETURN

	# A clean tree passes.
	printf 'services:\n  db:\n    image: mirror.gcr.io/library/postgres:16\n' > "$tmp/ok.yaml"
	if ! scan "$tmp" 2>/dev/null; then echo "self-test FAIL: clean tree rejected" >&2; return 1; fi

	# The banned registry is caught.
	printf 'services:\n  db:\n    image: public.ecr.aws/docker/library/postgres:16\n' > "$tmp/bad.yaml"
	rc=0; scan "$tmp" >/dev/null 2>&1 || rc=$?
	if [ "$rc" -eq 0 ]; then echo "self-test FAIL: banned registry accepted" >&2; return 1; fi

	# A quoted form is caught too — the thing an author writes when they copy
	# from a registry UI, and the shape a naive grep for `image: public` misses.
	rm -f "$tmp/bad.yaml"
	printf 'services:\n  db:\n    image: "public.ecr.aws/docker/library/redis:7"\n' > "$tmp/quoted.yaml"
	rc=0; scan "$tmp" >/dev/null 2>&1 || rc=$?
	if [ "$rc" -eq 0 ]; then echo "self-test FAIL: quoted banned registry accepted" >&2; return 1; fi

	# A mention in a COMMENT must not fail the gate — this file and #1007's own
	# notes name the registry on purpose, and a gate that cannot survive being
	# documented gets deleted.
	rm -f "$tmp/quoted.yaml"
	printf '# we used to pull from public.ecr.aws/docker/library/postgres:16\nservices:\n  db:\n    image: mirror.gcr.io/library/postgres:16\n' > "$tmp/comment.yaml"
	if ! scan "$tmp" 2>/dev/null; then echo "self-test FAIL: a comment mentioning the registry failed the gate" >&2; return 1; fi

	echo "check-service-image-registry self-test: ok"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit $?
fi

scan "$(dirname "$0")/../.github/workflows"
echo "service-container registries ok"
