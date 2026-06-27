#!/usr/bin/env bash
#
# End-to-end test for mixing dbt with operators in one DAG (ADR 0043) on a real
# k3d cluster. A dag.py wires real operators around a dbt_group(); the compiler
# merges them into one dag.json, and the scheduler runs operators AND per-model
# dbt pods in the right order, against a shared Postgres warehouse.
#
# Layout: the dbt project lives at the DAG root (project: .) alongside dag.py, so
# each dbt pod finds dbt_project.yml in its working dir.
#
# Requirements: k3d, kubectl, docker, jq, curl, dbt, python3, `make build`, dev DB.
# On Linux/CI: LEOFLOW_E2E_HOST_ADDR=host.k3d.internal and PYTHONPATH=parser. A
# stale ~/.leoflow parser cache (issue #400) can be bypassed with
# LEOFLOW_E2E_PARSER_CMD.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${LEOFLOW_E2E_CLUSTER:-leoflow-dbt-mix-e2e}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-host.docker.internal}"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
GRPC_PORT="${LEOFLOW_E2E_GRPC_PORT:-9091}"
METRICS_PORT="${LEOFLOW_E2E_METRICS_PORT:-9090}"
WH_PORT="${LEOFLOW_E2E_WAREHOUSE_PORT:-15544}"
PY_VERSION="${LEOFLOW_E2E_PY_VERSION:-3.11}"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-dbt-mix-dag:dev"
DAG_ID="mix"
WAREHOUSE="leoflow-dbt-mix-wh"
API="http://localhost:${HTTP_PORT}"
WORKDIR="$(mktemp -d)"
# shellcheck disable=SC2206
PARSER_CMD=(${LEOFLOW_E2E_PARSER_CMD:+--parser-cmd "$LEOFLOW_E2E_PARSER_CMD"})

fail() { echo "FAIL: $*" >&2; exit 1; }
log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

SERVER_PID=""
cleanup() {
  set +e
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1
  docker rm -f "$WAREHOUSE" >/dev/null 2>&1
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl dbt python3; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done
[ -x "$ROOT/bin/leoflow" ] && [ -x "$ROOT/bin/leoflow-server" ] || fail "build first: make build"

log "Starting the shared Postgres warehouse on :${WH_PORT}"
docker rm -f "$WAREHOUSE" >/dev/null 2>&1 || true
docker run -d --name "$WAREHOUSE" -p "${WH_PORT}:5432" \
  -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=wh postgres:16-alpine >/dev/null
for _ in $(seq 1 30); do docker exec "$WAREHOUSE" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done

# The DAG = a dbt project at the root + a dag.py that wires operators around it.
PROJ="$WORKDIR/$DAG_ID"
mkdir -p "$PROJ/models" "$PROJ/seeds"
cat >"$PROJ/dag.py" <<'PY'
from leoflow import dbt_group
from airflow.providers.standard.operators.bash import BashOperator
from airflow.sdk import DAG

with DAG("mix", schedule="@daily"):
    pre = BashOperator(task_id="pre", bash_command="echo pre")
    models = dbt_group("analytics")
    post = BashOperator(task_id="post", bash_command="echo post")
    pre >> models >> post
PY
cat >"$PROJ/leoflow.yaml" <<'YAML'
schema_version: "1.0"
dag_id: mix
dbt_groups:
  analytics:
    project: .
    manifest: target/manifest.json
    granularity: node
YAML
cat >"$PROJ/dbt_project.yml" <<'YAML'
name: 'shop'
version: '1.0.0'
profile: 'shop'
model-paths: ["models"]
seed-paths: ["seeds"]
models: { shop: { +materialized: table } }
YAML
cat >"$PROJ/profiles.yml" <<YAML
shop:
  target: dev
  outputs:
    dev: { type: postgres, host: ${HOST_ADDR}, port: ${WH_PORT}, user: postgres, password: pw, dbname: wh, schema: public, threads: 4 }
YAML
printf 'id,v\n1,10\n2,20\n3,30\n' >"$PROJ/seeds/raw.csv"
echo "select id, v from {{ ref('raw') }}" >"$PROJ/models/stg.sql"
echo "select id, sum(v) as total from {{ ref('stg') }} group by id" >"$PROJ/models/mart.sql"

log "Generating the manifest"
( cd "$PROJ" && DBT_PROFILES_DIR="$PROJ" dbt parse >/dev/null 2>&1 ) || fail "dbt parse failed"

cat >"$PROJ/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
USER root
RUN pip install --no-cache-dir "dbt-postgres==1.9.*"
COPY . /home/leoflow/
ENV DBT_PROFILES_DIR=/home/leoflow
RUN chown -R leoflow:leoflow /home/leoflow
USER leoflow
WORKDIR /home/leoflow
DOCKER

log "Building the leoflow base image"
docker build -q -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" >/dev/null

log "Creating k3d cluster + namespace"
k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
k3d cluster create "$CLUSTER" --wait >/dev/null
kubectl create namespace leoflow >/dev/null

log "Starting the control plane"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${METRICS_PORT}"
"$ROOT/bin/leoflow-server" >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
sleep 5

log "Compiling (parser merges the operators with the dbt_group) + building the image"
"$ROOT/bin/leoflow" compile "$PROJ" "${PARSER_CMD[@]}" --image "$DAG_IMAGE" --build --dockerfile Dockerfile -o "$PROJ/dag.json"
log "Asserting the merge: operators + namespaced dbt tasks wired together"
for want in pre post analytics__raw analytics__stg analytics__mart; do
  jq -e --arg t "$want" '.tasks[] | select(.task_id==$t)' "$PROJ/dag.json" >/dev/null || fail "missing task $want"
done
jq -e '.tasks[] | select(.task_id=="analytics__raw") | .depends_on | index("pre")' "$PROJ/dag.json" >/dev/null \
  || fail "group root analytics__raw is not wired to the upstream operator 'pre'"
jq -e '.tasks[] | select(.task_id=="post") | .depends_on | index("analytics__mart")' "$PROJ/dag.json" >/dev/null \
  || fail "downstream operator 'post' is not wired to the group leaf"
k3d image import "$BASE_IMAGE" "$DAG_IMAGE" --cluster "$CLUSTER" >/dev/null

log "Pushing + triggering"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"
"$ROOT/bin/leoflow" push "$PROJ/dag.json" --server "$API" --token "$TOKEN"
RUN_ID="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"

log "Waiting for all tasks (operators + dbt pods) to succeed"
deadline=$(( $(date +%s) + 300 ))
while :; do
  states="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" | jq -r '.task_instances[].state')"
  echo "  states: $(echo "$states" | tr '\n' ' ')"
  if echo "$states" | grep -qE 'failed|upstream_failed'; then
    echo "--- server.log tail ---"; tail -30 "$WORKDIR/server.log"; fail "a task failed"
  fi
  if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success'; then break; fi
  [ "$(date +%s)" -lt "$deadline" ] || fail "timeout"
  sleep 6
done

log "Asserting the mart materialized (dbt pods ran in the merged DAG)"
rows="$(docker exec "$WAREHOUSE" psql -U postgres -d wh -tAc 'select count(*) from public.mart;' | tr -d '[:space:]')"
[ "$rows" = "3" ] || fail "mart row count = $rows, want 3"

log "dbt + operators mixed e2e passed"
