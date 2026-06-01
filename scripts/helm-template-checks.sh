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
  --set secretKey='leoflow-tmpl-check-secret-key-32' \
  --set agentTLS.serverCertSecret='leoflow-agent-tls-fixture')

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

# Migration Job must be hardened the same way as the Deployment (#174):
# restricted-PSA clusters require runAsNonRoot+numeric runAsUser on EVERY
# pod, including pre-install hooks. Scope the rendering to just the Job
# template so we don't accidentally validate the Deployment's hardening
# here (that lives in the same rendered output but is asserted above by
# proxy of the env contract).
JOB_RENDERED=$(helm template leoflow-test "$CHART" \
  --set database.url='postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://r:6379/0' \
  --set auth.jwtSecret='helm-template-check-jwt-fixture' \
  --set secretKey='leoflow-tmpl-check-secret-key-32' \
  --set agentTLS.serverCertSecret='leoflow-agent-tls-fixture' \
  --show-only templates/migration-job.yaml)

expect_in_job() {
  local needle="$1"
  local description="$2"
  if ! grep -qF -- "$needle" <<<"$JOB_RENDERED"; then
    echo "FAIL: migrate Job missing $description ($needle)" >&2
    fail=1
  else
    echo "OK:   migrate Job has $description"
  fi
}
expect_in_job 'runAsNonRoot: true' "runAsNonRoot:true (restricted PSA gate)"
expect_in_job 'runAsUser: 65532'   "runAsUser:65532 (distroless nonroot UID)"

# Regression guard for PR #171: the PoC values fixture (the recipe in
# helm/leoflow/examples/) shipped with a 34-byte secretKey, which
# silently broke ParseKey (AES-256 requires exactly 32 bytes) and made
# Connection management 503 for anyone following the recipe verbatim.
# Validate every YAML fixture under helm/leoflow/examples/ that sets
# `secretKey:` so a future fixture edit can't repeat the same class of
# bug. The check is plain bash — no python / yq dependency.
for fixture in helm/leoflow/examples/*.yaml; do
  [ -f "$fixture" ] || continue
  fixture_key=$(grep -E '^secretKey: ' "$fixture" 2>/dev/null \
    | head -1 \
    | sed -E 's/^secretKey: *//; s/^["'\'']?//; s/["'\'']?$//; s/ *#.*$//')
  if [ -n "$fixture_key" ]; then
    if [ "${#fixture_key}" -eq 32 ]; then
      echo "OK:   $(basename "$fixture") secretKey is exactly 32 bytes (AES-256)"
    else
      echo "FAIL: $(basename "$fixture") secretKey is ${#fixture_key} bytes, want 32 — ParseKey would reject this and the recipe would break Connection management" >&2
      fail=1
    fi
  fi
done

if [ "$fail" -ne 0 ]; then
  echo
  echo "helm-template-checks: one or more contracts unmet (see FAIL lines above)" >&2
  exit 1
fi
echo
echo "helm-template-checks: all contracts met"
