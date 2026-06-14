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
# HTTP port is overridable (LEOFLOW_E2E_HTTP_PORT) so a developer whose 8080 is
# already taken — a forwarded VM, another server — can run without a conflict. CI
# uses the default.
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
API="http://localhost:${HTTP_PORT}"
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
# Declare the sensor's provider so the ADR 0040 A5 compile check passes; the
# Dockerfile below installs it into the image.
sed -i.bak 's/^dependencies: \[\]/dependencies: [apache-airflow-providers-http]/' \
  "$WORKDIR/$DAG_ID/leoflow.yaml" && rm -f "$WORKDIR/$DAG_ID/leoflow.yaml.bak"
# A real Airflow-SDK DAG (the parser requires an actual DAG object, not a bare
# function). Two tasks in sequence prove pod-per-task AND cross-pod ordering:
# each runs in its own pod whose agent reports state over gRPC.
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
"""e2edag — Leoflow pod-per-task smoke DAG."""
from __future__ import annotations

from airflow.sdk import DAG, task
# A provider SENSOR (poke mode) — captured generically as type=airflow_operator
# (ADR 0040). HttpSensor pokes an endpoint until it returns 200; we point it at
# the control plane's own public /readyz, so it succeeds on the first poke and
# exercises the FULL operator chain end-to-end: scheduler -> pod -> agent
# `--operator` -> runtime run_operator -> execute(), plus connection delivery.
from airflow.providers.http.sensors.http import HttpSensor


@task
def extract() -> str:
    print("hello from leoflow e2e: extract")
    return "payload-42"


@task
def transform(value: str) -> str:
    # Consuming extract's output proves TaskFlow value passing end-to-end (#51).
    print(f"transform received: {value}")
    return value.upper()


@task
def read_var(_value: str, ds=None) -> None:
    # The agent delivers Admin Variables as AIRFLOW_VAR_* env (#54 / ADR 0021).
    import os
    print("e2e variable:", os.environ.get("AIRFLOW_VAR_E2E_GREETING"))
    # A @task gets the run context too (ADR 0040 native parity): `ds` is injected by
    # name (a string when injected, None if injection regressed). Assert + log it.
    assert ds is not None, "@task did not receive 'ds' from the run context"
    print("e2e context ds:", ds)


@task
def endpoint_name() -> str:
    # A value-returning upstream the operator consumes via ti.xcom_pull below.
    return "readyz"


with DAG("e2edag", schedule="@daily", catchup=False, tags=["e2e"]):
    tail = read_var(transform(extract()))
    target = endpoint_name()
    # A real provider sensor (poke mode) downstream of the @task chain. Proves a
    # type=airflow_operator task runs in its own pod and resolves a managed
    # Connection (AIRFLOW_CONN_CP) — the generic-executor path. Its endpoint is
    # CHAINED from an upstream operator's return_value via ti.xcom_pull (ADR 0040),
    # so this exercises the real chaining wire: the agent must authorize fetching a
    # depends_on upstream AND deliver a NON-EMPTY upstream-xcom map (the case a
    # None-returning upstream would not, which let a kwarg-collision regression slip
    # past CI once). If chaining breaks, the endpoint renders empty and the poke fails.
    probe = HttpSensor(task_id="probe", http_conn_id="cp",
                       endpoint="{{ ti.xcom_pull('endpoint_name') }}",
                       poke_interval=2, timeout=60)
    tail >> probe
    target >> probe
PY
cat > "$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
# The DAG uses a provider sensor; install its provider into the image (this is
# what connectors:/dependencies: do in a real project).
RUN pip install --no-cache-dir apache-airflow-providers-http
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
# Allow secret delivery over the e2e's plaintext gRPC (dev only; prod uses TLS,
# issue #58) so the agent can fetch Admin Variables/Connections (#54).
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
# Connections are encrypted at rest (ADR 0019); the server refuses to store one
# without a key. Set a 32-byte key so the operator's Connection can be created.
export LEOFLOW_SECRET_KEY="e2e-secret-key-32-bytes-padding!"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
"$ROOT/bin/leoflow-server" &
SERVER_PID=$!
sleep 5

log "Compiling, building, and importing the DAG image"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" \
  --build --dockerfile Dockerfile -o "$WORKDIR/$DAG_ID/dag.json"
log "Asserting the sensor compiled to type=airflow_operator (ADR 0040 generic path)"
jq -e '.tasks[] | select(.task_id=="probe" and .type=="airflow_operator")' \
  "$WORKDIR/$DAG_ID/dag.json" >/dev/null || fail "probe did not compile to airflow_operator"
log "Asserting probe chains from endpoint_name (ti.xcom_pull, ADR 0040)"
jq -e '.tasks[] | select(.task_id=="probe") | .depends_on | index("endpoint_name")' \
  "$WORKDIR/$DAG_ID/dag.json" >/dev/null || fail "probe does not depend on endpoint_name (chaining wire missing)"
k3d image import "$BASE_IMAGE" "$DAG_IMAGE" --cluster "$CLUSTER"

log "Pushing the DAG"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --server "$API" --token "$TOKEN"

log "Creating an Admin Variable for the runtime-delivery check (#54)"
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"key":"e2e_greeting","value":"hi-from-admin"}' "$API/api/v2/variables" >/dev/null

log "Creating the 'cp' http Connection the operator sensor pokes (ADR 0040)"
# Point it at the host control plane (the pod reaches the host the same way the
# agent dials gRPC). HttpSensor pokes http://${HOST_ADDR}:${HTTP_PORT}/readyz.
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"connection_id\":\"cp\",\"conn_type\":\"http\",\"host\":\"${HOST_ADDR}\",\"port\":${HTTP_PORT},\"schema\":\"http\"}" \
  "$API/api/v2/connections" >/dev/null

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

log "Asserting Admin Variable delivery to the task (#54): read_var sees AIRFLOW_VAR_*"
vlog=""
for try in 0 1 2; do
  body="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances/read_var/logs/$try" 2>/dev/null || true)"
  if echo "$body" | grep -q "e2e variable: hi-from-admin"; then vlog="$body"; break; fi
done
[ -n "$vlog" ] || fail "read_var did not see the Admin Variable — var/conn runtime delivery broken (#54)"
log "variable delivery OK: read_var saw hi-from-admin"

log "Asserting @task run-context injection (ADR 0040): read_var received 'ds'"
echo "$vlog" | grep -q "e2e context ds:" || fail "read_var did not receive ds from the run context — @task context injection broken"
log "@task context OK: read_var received ds"

log "E2E passed"
