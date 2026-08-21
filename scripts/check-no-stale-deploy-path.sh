#!/usr/bin/env bash
# Fail if any reference to the old `deploy/gke` path survives.
#
# `deploy/gke` was renamed to `deploy/k8s` (#690) because the recipe is
# cloud-agnostic — it runs unchanged on EKS/GKE/AKS. A stale `deploy/gke`
# reference in a script comment, README, docs link, or .gitignore points at a
# directory that no longer exists: scripts `cd` into nothing, doc links 404.
# Nothing else catches this — the rename leaves the old string valid-looking
# text that no build step resolves — so it needs a gate.
set -euo pipefail

cd "$(dirname "$0")/.."

# Search the tracked tree only (respects .gitignore, skips .git). This script
# names the old path itself, so exclude it from its own scan.
if git grep -n "deploy/gke" -- ':!scripts/check-no-stale-deploy-path.sh' >/tmp/stale-deploy-path.$$ 2>/dev/null; then
	echo "FAIL: stale 'deploy/gke' references remain (renamed to deploy/k8s, #690):"
	cat /tmp/stale-deploy-path.$$
	rm -f /tmp/stale-deploy-path.$$
	echo
	echo "Update each to 'deploy/k8s'."
	exit 1
fi
rm -f /tmp/stale-deploy-path.$$

echo "OK: no stale 'deploy/gke' references"
