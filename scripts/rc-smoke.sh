#!/usr/bin/env bash
#
# RC pre-cut smoke battery — run on the release box before cutting an RC.
#
# Runs every mechanical gate that must be green before tagging a release
# candidate, collecting a PASS/FAIL for each (it does NOT stop at the first
# failure — the point is to see everything that is red in one pass). Exits
# non-zero if any step failed. The one thing it cannot do is the human UI check;
# it prints that reminder at the end.
#
# Usage:
#   bash scripts/rc-smoke.sh              # full battery (gates + k3d e2es, ~40-60 min)
#   SKIP_E2E=1 bash scripts/rc-smoke.sh   # gates only (fast, no k3d), ~5-10 min
#
# Requires (full run): docker, go, helm, helm unittest plugin, golangci-lint,
# ruff, k3d, kubectl, jq, node. A working `make dev-up` (Postgres + Redis).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2

# Canonical --set flags the Pro chart's render guards require (external DB/Redis
# + agent TLS), so `helm lint`/template checks render instead of `fail`ing.
HELM_SET=(--set database.url=postgres://h/d --set redis.url=redis://h/0
  --set auth.jwtSecret=jwt --set agentTLS.serverCertSecret=tls --set agentTLS.caConfigMap=ca)

RESULTS=()
FAILED=0

bold() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
# run_step NAME CMD... — runs CMD, records PASS/FAIL, never aborts the battery.
run_step() {
  local name="$1"; shift
  bold "$name"
  if "$@"; then
    RESULTS+=("PASS  $name"); printf '\033[1;32m    PASS\033[0m %s\n' "$name"
  else
    RESULTS+=("FAIL  $name"); FAILED=1; printf '\033[1;31m    FAIL\033[0m %s\n' "$name"
  fi
}

# --- helpers wrapping the checks that need composition -----------------------
helm_lint()        { helm lint helm/leoflow "${HELM_SET[@]}"; }
helm_docs_fresh()  {
  command -v helm-docs >/dev/null || { echo "helm-docs not installed"; return 1; }
  helm-docs -c helm/leoflow >/dev/null 2>&1
  git diff --exit-code -- helm/leoflow/README.md
}
ui_smoke() {
  # A real ui-smoke failure must surface as FAIL; only a MISSING node is skipped.
  if ! command -v node >/dev/null; then echo "node missing; skipping ui-smoke"; return 0; fi
  node test/e2e/ui-smoke.js
}

# --- preflight ---------------------------------------------------------------
bold "Preflight"
git rev-parse --abbrev-ref HEAD | grep -qx main || echo "  (warning: not on main)"
[ -z "$(git status --porcelain)" ] || echo "  (warning: working tree not clean)"
docker info >/dev/null 2>&1 || { echo "docker is not running — aborting"; exit 2; }

# --- prerequisites (must succeed; abort if not) ------------------------------
bold "Prerequisites: dev-up + build"
make dev-up   || { echo "make dev-up failed — aborting (need Postgres+Redis)"; exit 2; }
make build    || { echo "make build failed — aborting"; exit 2; }

# --- Go + Python gates -------------------------------------------------------
run_step "golangci-lint + ruff (make lint)"        make lint
run_step "unit tests + coverage floor (make test)" make test
run_step "integration tests (make test-integration)" make test-integration
run_step "govulncheck (make vuln)"                 make vuln

# --- Helm gates --------------------------------------------------------------
run_step "helm lint"                     helm_lint
run_step "helm unittest"                 helm unittest helm/leoflow
run_step "helm-template-checks.sh"       bash scripts/helm-template-checks.sh
run_step "rbac-covers-executor.sh"       bash scripts/rbac-covers-executor.sh
run_step "check-dependabot-dirs.sh"      bash scripts/check-dependabot-dirs.sh
run_step "helm README fresh (helm-docs)" helm_docs_fresh

# --- k3d end-to-end (slow; skippable) ----------------------------------------
if [ "${SKIP_E2E:-0}" = "1" ]; then
  bold "Skipping k3d e2es (SKIP_E2E=1)"
else
  run_step "e2e operators (make e2e)"        make e2e
  run_step "e2e split two-process (make e2e-split)" make e2e-split
  run_step "e2e dbt (make e2e-dbt)"          make e2e-dbt
  run_step "ui smoke (headless SPA)"         ui_smoke
fi

# --- summary -----------------------------------------------------------------
printf '\n\033[1m===== RC smoke summary =====\033[0m\n'
for r in "${RESULTS[@]}"; do
  case "$r" in FAIL*) printf '\033[1;31m%s\033[0m\n' "$r" ;; *) printf '\033[1;32m%s\033[0m\n' "$r" ;; esac
done

printf '\n\033[1mStill required by hand (this script cannot do it):\033[0m\n'
cat <<'MANUAL'
  - Lite (all role): `leoflow lite` (or the server with no role) → open the UI,
    trigger a DAG, confirm it runs and the dashboard/logs render.
  - Pro (split): `helm install ... --set split.enabled=true
    --set logs.persistence.accessMode=ReadWriteMany` on a real cluster → confirm
    the api and scheduler Deployments come up, a run dispatches, logs render.
MANUAL

if [ "$FAILED" -ne 0 ]; then
  printf '\n\033[1;31mRC smoke: FAILED — do not cut until green.\033[0m\n'; exit 1
fi
printf '\n\033[1;32mRC smoke: all mechanical gates PASS. Do the manual UI checks above, then cut.\033[0m\n'
