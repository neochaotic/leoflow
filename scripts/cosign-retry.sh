#!/usr/bin/env bash
# Run `cosign "$@"` with retry + exponential backoff.
#
# Keyless cosign signing fetches an ambient OIDC token from the GitHub Actions
# provider on every invocation. That fetch is a network call to an external
# service and occasionally returns garbage on a momentary blip — one release cut
# died mid-sign with `fetching ambient OIDC credentials: invalid character
# 'u'...`, which drafted an otherwise-good release. A single transient failure
# should not cost a whole release, so every `cosign sign` in the release
# pipeline goes through this wrapper: the GoReleaser docker_signs hook that signs
# the control-plane server manifests AND the migrate/runtime workflow steps, so
# all image signatures retry identically.
#
# Only the invocation is retried; cosign itself is idempotent for an
# already-signed digest, so a retry after a partial success is safe.
set -euo pipefail

attempts="${COSIGN_RETRY_ATTEMPTS:-3}"
delay="${COSIGN_RETRY_DELAY:-5}"

n=1
while true; do
	if cosign "$@"; then
		exit 0
	fi
	if [ "$n" -ge "$attempts" ]; then
		echo "cosign-retry: failed after ${attempts} attempt(s): cosign $*" >&2
		exit 1
	fi
	echo "cosign-retry: attempt ${n}/${attempts} failed; retrying in ${delay}s..." >&2
	sleep "$delay"
	n=$((n + 1))
	delay=$((delay * 2))
done
