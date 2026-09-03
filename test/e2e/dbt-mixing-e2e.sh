#!/usr/bin/env bash
#
# End-to-end test for mixing dbt with operators in one DAG (ADR 0043) on a real
# k3d cluster. A dag.py wires real operators around a dbt_group(); the compiler
# merges them into one dag.json, and the scheduler runs operators AND per-model
# dbt pods in the right order, against a shared Postgres warehouse.
#
# Layout: the dbt project is a subdir (transform/) next to dag.py — the realistic
# mixing layout. Each dbt command is scoped with --project-dir (#401) so the pod
# finds dbt_project.yml regardless of its working dir.
#
# Requirements: k3d, kubectl, docker, jq, curl, dbt, python3, `make build`, dev DB.
# On Linux/CI: LEOFLOW_E2E_HOST_ADDR=host.k3d.internal and PYTHONPATH=parser. A
# stale ~/.leoflow parser cache (issue #400) can be bypassed with
# LEOFLOW_E2E_PARSER_CMD.
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${LEOFLOW_E2E_CLUSTER:-leoflow-dbt-mix-e2e}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
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

# dump_pods prints the task pods, a describe tail for those not Running, and
# their logs — the evidence for diagnosing a failed task before the cleanup trap
# deletes the cluster. Without it a failed run leaves only the server log, which
# says nothing about why a pod exited.
dump_pods() {
  printf '\033[1;33m--- task pods (namespace leoflow) ---\033[0m\n' >&2
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n leoflow -o name 2>/dev/null); do
    phase="$(kubectl get -n leoflow "$p" -o jsonpath='{.status.phase}' 2>/dev/null)"
    if [ "$phase" != "Running" ]; then
      printf '\033[1;33m--- describe %s (%s) ---\033[0m\n' "$p" "$phase" >&2
      kubectl describe -n leoflow "$p" >&2 2>&1 | tail -25 || true
    fi
    printf '\033[1;33m--- logs %s ---\033[0m\n' "$p" >&2
    kubectl logs -n leoflow "$p" --all-containers --tail=80 >&2 2>&1 || true
  done
}
fail() { echo "FAIL: $*" >&2; dump_pods; exit 1; }
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
mkdir -p "$PROJ/transform/models" "$PROJ/transform/seeds"
cat >"$PROJ/dag.py" <<'PY'
from leoflow import dbt_group
from airflow.providers.standard.operators.bash import BashOperator
from airflow.sdk import DAG

with DAG("mix", schedule="@daily"):
    pre = BashOperator(task_id="pre", bash_command="echo pre")
    models = dbt_group("transform")
    post = BashOperator(task_id="post", bash_command="echo post")
    pre >> models >> post
PY
cat >"$PROJ/leoflow.yaml" <<'YAML'
schema_version: "1.0"
dag_id: mix
dbt_groups:
  transform:
    project: ./transform
    manifest: target/manifest.json
    granularity: node
YAML
# Pin the DAG image to the host arch (see e2e.sh): the loader defaults to
# linux/amd64, which fails FROM an arm64 base on a Lima/dev host → ErrImagePull.
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >>"$PROJ/leoflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
cat >"$PROJ/transform/dbt_project.yml" <<'YAML'
name: 'shop'
version: '1.0.0'
profile: 'shop'
model-paths: ["models"]
seed-paths: ["seeds"]
models: { shop: { +materialized: table } }
YAML
cat >"$PROJ/transform/profiles.yml" <<YAML
shop:
  target: dev
  outputs:
    dev: { type: postgres, host: ${HOST_ADDR}, port: ${WH_PORT}, user: postgres, password: pw, dbname: wh, schema: public, threads: 4 }
YAML
printf 'id,v\n1,10\n2,20\n3,30\n' >"$PROJ/transform/seeds/raw.csv"
echo "select id, v from {{ ref('raw') }}" >"$PROJ/transform/models/stg.sql"
echo "select id, sum(v) as total from {{ ref('stg') }} group by id" >"$PROJ/transform/models/mart.sql"

log "Generating the manifest"
( cd "$PROJ/transform" && DBT_PROFILES_DIR="$PROJ/transform" dbt parse >/dev/null 2>&1 ) || fail "dbt parse failed"

cat >"$PROJ/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
USER root
RUN pip install --no-cache-dir "dbt-postgres==1.9.*"
COPY . /home/leoflow/
ENV DBT_PROFILES_DIR=/home/leoflow/transform
RUN chown -R 65532:65532 /home/leoflow
# Numeric UID so PodSecurity runAsNonRoot (on by default) admits the pod: the
# kubelet cannot verify a login NAME. Matches the base image's USER 65532:65532.
USER 65532:65532
WORKDIR /home/leoflow
DOCKER

log "Building the leoflow base image"
docker build --provenance=false -q -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" >/dev/null

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
for want in pre post transform__raw transform__stg transform__mart; do
  jq -e --arg t "$want" '.tasks[] | select(.task_id==$t)' "$PROJ/dag.json" >/dev/null || fail "missing task $want"
done
jq -e '.tasks[] | select(.task_id=="transform__raw") | .depends_on | index("pre")' "$PROJ/dag.json" >/dev/null \
  || fail "group root transform__raw is not wired to the upstream operator 'pre'"
jq -e '.tasks[] | select(.task_id=="post") | .depends_on | index("transform__mart")' "$PROJ/dag.json" >/dev/null \
  || fail "downstream operator 'post' is not wired to the group leaf"
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE"

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
