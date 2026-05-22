#!/usr/bin/env bash
#
# End-to-end smoke test for Leoflow on a local Kubernetes cluster (k3d).
#
# Exercises the full pod-path: build the base + DAG images, import them into
# k3d, run the control plane on the host against the dev Postgres/Redis
# (`make dev-up`), push and trigger a DAG, and assert every task instance reaches
# 'success' — i.e. each task ran in a real pod whose agent reported state over
# gRPC.
#
# Requirements: k3d, kubectl, docker, jq, curl, the leoflow binaries
# (`make build`), and a running dev database (`make dev-up`). Developer/CI tool,
# not part of `go test`; production e2e runs against a real cluster via Helm.
#
# Usage: test/e2e/e2e.sh [cluster-name]
set -euo pipefail

CLUSTER="${1:-leoflow-e2e}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-e2e-dag:dev"
DAG_ID="e2edag"
API="http://localhost:8080"
GRPC_PORT="9091"
SERVER_PID=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done

log "Scaffolding a minimal DAG project ($DAG_ID)"
"$ROOT/bin/leoflow" init "$WORKDIR/$DAG_ID"
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
def hello():
    print("hello from leoflow e2e")
    return {"ok": True}
PY
cat > "$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building base and DAG images"
docker build -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"

log "Creating k3d cluster '$CLUSTER'"
k3d cluster create "$CLUSTER" --wait

log "Starting the control plane (agents dial host.k3d.internal:${GRPC_PORT})"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin123"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="host.k3d.internal:${GRPC_PORT}"
"$ROOT/bin/leoflow-server" &
SERVER_PID=$!
sleep 5

log "Compiling, building, and importing the DAG image"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" \
  --build --dockerfile Dockerfile -o "$WORKDIR/$DAG_ID/dag.json"
k3d image import "$BASE_IMAGE" "$DAG_IMAGE" --cluster "$CLUSTER"

log "Pushing the DAG"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --username admin@leoflow.local --password admin123)"
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --token "$TOKEN"

log "Triggering a run"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"
log "run = $RUN_ID"

log "Waiting for all task instances to succeed"
deadline=$(( $(date +%s) + 180 ))
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  echo "$states" | grep -qE 'failed|upstream_failed' && fail "a task failed: $states"
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
    log "all tasks terminal-success: $states"
    break
  fi
  [ "$(date +%s)" -gt "$deadline" ] && fail "timed out; last states: ${states:-<none>}"
  sleep 3
done

log "E2E passed"
