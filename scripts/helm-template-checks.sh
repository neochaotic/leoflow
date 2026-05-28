#!/usr/bin/env bash
# helm-template-checks.sh — render the chart with a minimal Pro-shaped values
# set and assert the rendered output contains the env vars and Secret keys the
# control plane needs at runtime.
#
# This is the seed of the eventual chart-test CI gate (issue #143). It is
# intentionally small and shell-only so it can run in any CI runner with `helm`
# installed (or locally with `brew install helm`).
#
# Run from the repo root:
#   bash scripts/helm-template-checks.sh
set -euo pipefail

CHART="helm/leoflow"
if [ ! -f "$CHART/Chart.yaml" ]; then
  echo "helm-template-checks: $CHART not found; run from the repo root" >&2
  exit 2
fi

# Minimal Pro-shaped values: external Postgres + external Redis + a jwtSecret
# and a secretKey (the values the chart will encrypt at rest and inject as env).
# Fixture keys are NOT real credentials — only used to render the chart.
RENDERED=$(helm template leoflow-test "$CHART" \
  --set database.url='postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://r:6379/0' \
  --set auth.jwtSecret='helm-template-check-jwt-fixture' \
  --set secretKey='helm-template-check-secret-fixture-32B!!!')

fail=0
expect_substring() {
  local needle="$1"
  local description="$2"
  if ! grep -qF -- "$needle" <<<"$RENDERED"; then
    echo "FAIL: missing $description ($needle)" >&2
    fail=1
  else
    echo "OK:   $description"
  fi
}

# Each line below is a contract the chart must honour. Add to this list as new
# config keys land.
expect_substring 'name: LEOFLOW_DATABASE_URL'  "DB URL env entry in deployment"
expect_substring 'name: LEOFLOW_REDIS_URL'     "Redis URL env entry in deployment"
expect_substring 'name: LEOFLOW_AUTH_JWT_SECRET' "JWT secret env entry in deployment"
expect_substring 'name: LEOFLOW_SECRET_KEY'    "Connection encryption key env entry in deployment (ADR 0019)"
expect_substring 'jwtSecret:'                  "jwtSecret key in chart-managed Secret"
expect_substring 'secretKey:'                  "secretKey in chart-managed Secret (ADR 0019)"

if [ "$fail" -ne 0 ]; then
  echo
  echo "helm-template-checks: one or more contracts unmet (see FAIL lines above)" >&2
  exit 1
fi
echo
echo "helm-template-checks: all contracts met"
