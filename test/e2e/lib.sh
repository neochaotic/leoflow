#!/usr/bin/env bash
# Shared helpers for the k3d end-to-end scripts.

# k3d_import imports images into a k3d cluster, working around a k3d bug where
# `k3d image import` prints "Successfully imported" even when the in-node
# containerd import actually failed ("ctr: open /k3d/images/...tar: no such file
# or directory") and exits 0. A bare import then silently leaves the cluster
# WITHOUT the image, so every task pod ErrImagePulls and the e2e fails far
# downstream with a confusing symptom. This retries and treats the import as
# successful only when k3d BOTH exits 0 AND wrote no error line (the ERRO /
# "failed to import" it emits to stderr despite the misleading success line),
# failing loud instead of proceeding into ErrImagePull.
#
# Usage: k3d_import <cluster> <image> [image...]
k3d_import() {
  local cluster="$1"; shift
  local attempt out rc
  for attempt in 1 2 3; do
    out="$(k3d image import "$@" --cluster "$cluster" 2>&1)" && rc=0 || rc=$?
    if [ "$rc" -eq 0 ] && ! printf '%s\n' "$out" | grep -qiE 'failed to import|no such file or directory|ERRO\['; then
      return 0
    fi
    printf '%s\n' "$out" >&2
    printf 'k3d_import: attempt %d hit the import flake (k3d misreported success, rc=%s); retrying...\n' "$attempt" "$rc" >&2
    sleep 3
  done
  printf 'FATAL: k3d image import failed to land images in cluster %q after 3 attempts (the tarball-not-found flake)\n' "$cluster" >&2
  return 1
}
