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
# Address task pods dial to reach the host control plane's gRPC. On Docker
# Desktop (macOS/Windows) host.docker.internal resolves to the host; k3d does
# not inject host.k3d.internal into CoreDNS there. Override for Linux/CI.
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-host.docker.internal}"
SERVER_PID=""

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
# dump_pods prints the task pods and their logs — invaluable for diagnosing a
# failed task before the cleanup trap deletes the cluster.
dump_pods() {
  printf '\033[1;33m--- task pods (namespace leoflow) ---\033[0m\n' >&2
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n leoflow -o name 2>/dev/null); do
    printf '\033[1;33m--- logs %s ---\033[0m\n' "$p" >&2
    kubectl logs -n leoflow "$p" --all-containers --tail=80 >&2 2>&1 || true
  done
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

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
# A real Airflow-SDK DAG (the parser requires an actual DAG object, not a bare
# function). Two tasks in sequence prove pod-per-task AND cross-pod ordering:
# each runs in its own pod whose agent reports state over gRPC.
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
"""e2edag — Leoflow pod-per-task smoke DAG."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def extract() -> str:
    print("hello from leoflow e2e: extract")
    return "payload-42"


@task
def transform(value: str) -> str:
    # Consuming extract's output proves TaskFlow value passing end-to-end (#51).
    print(f"transform received: {value}")
    return value.upper()


with DAG("e2edag", schedule="@daily", catchup=False, tags=["e2e"]):
    transform(extract())
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

log "Creating the 'leoflow' namespace (where task pods are created)"
kubectl create namespace leoflow

log "Starting the control plane (agents dial ${HOST_ADDR}:${GRPC_PORT})"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
# The default logs.dir (/var/log/leoflow) is not writable by a normal user, which
# made the server's log sink fail to open and surfaced as a bare stream EOF on the
# agent (#36). Use a writable temp dir so pod logs actually land on disk.
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
"$ROOT/bin/leoflow-server" &
SERVER_PID=$!
sleep 5

log "Compiling, building, and importing the DAG image"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" \
  --build --dockerfile Dockerfile -o "$WORKDIR/$DAG_ID/dag.json"
k3d image import "$BASE_IMAGE" "$DAG_IMAGE" --cluster "$CLUSTER"

log "Pushing the DAG"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --username admin@leoflow.local --password admin)"
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --token "$TOKEN"

log "Triggering a run"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"
log "run = $RUN_ID"

log "Waiting for all task instances to succeed"
deadline=$(( $(date +%s) + 300 ))
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  log "task states: [$(echo "$states" | tr '\n' ' ')] pods: [$(kubectl get pods -n leoflow --no-headers 2>/dev/null | awk '{print $1"="$3}' | tr '\n' ' ')]"
  echo "$states" | grep -qE 'failed|upstream_failed' && fail "a task failed: $states"
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
    log "all tasks terminal-success: $states"
    break
  fi
  [ "$(date +%s)" -gt "$deadline" ] && fail "timed out; last states: ${states:-<none>}"
  sleep 3
done

log "Asserting task logs were shipped from the pod (#36)"
read -r FIRST_TASK FIRST_TRY < <(curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" \
  | jq -r '.task_instances[0] | "\(.task_id) \(.try_number)"')
LOG_PATH="$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/$FIRST_TASK/logs"
log_body=""
for try in "$FIRST_TRY" 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" "$LOG_PATH/$try" 2>/dev/null || true)"
  if [ -n "$body" ] && ! echo "$body" | grep -q '"status":404'; then
    log_body="$body"
    log "logs for $FIRST_TASK (try=$try): $(echo "$body" | head -1 | cut -c1-100)"
    break
  fi
done
[ -n "$log_body" ] || fail "no logs shipped from pod for task '$FIRST_TASK' — agent log streaming broke (#36)"

# And the structured JSON view (the UI drill-down) must parse with content.
struct="$(curl -fsS -H "Authorization: Bearer $TOKEN" -H "Accept: application/json" "$LOG_PATH/$FIRST_TRY" 2>/dev/null \
  || curl -fsS -H "Authorization: Bearer $TOKEN" -H "Accept: application/json" "$LOG_PATH/0" 2>/dev/null || true)"
if echo "$struct" | jq -e '.content | length > 0' >/dev/null 2>&1; then
  log "structured logs OK: $(echo "$struct" | jq -r '.content | length') content items (#43)"
else
  fail "structured JSON logs missing content (#43)"
fi

log "Asserting TaskFlow value passing (#51): transform received extract's output"
tlog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/transform/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "transform received: payload-42"; then tlog="$body"; break; fi
done
[ -n "$tlog" ] || fail "transform did not receive extract's output — TaskFlow value passing broken (#51)"
log "value passing OK: transform received payload-42"

log "E2E passed"
