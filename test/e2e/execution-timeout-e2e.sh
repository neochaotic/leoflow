#!/usr/bin/env bash
#
# End-to-end test for `execution_timeout` on a real Kubernetes cluster (k3d),
# #925 + #930. This is the seam no unit test can reach: it needs a real kubelet,
# because the kubelet counts activeDeadlineSeconds from pod.Status.StartTime —
# a field only a kubelet stamps.
#
# Why a real cluster is the only proof. Two clocks bound a task that declares
# execution_timeout_seconds: the AGENT's (started inside its execute step) and
# the KUBELET's (activeDeadlineSeconds from StartTime, stamped before the image
# pull). The agent owns the SEMANTIC timeout — it is the only one that can say
# "execution_timeout: task exceeded Xs limit". The kubelet's is a backstop for
# an agent that can no longer enforce anything, so it must never fire first;
# when it does, the pod is gone and the reason degrades to what Kubernetes saw
# from outside, which cannot name a timeout. A unit test can pin each half, but
# only a kubelet can run the race.
#
# The scenario declares `execution_timeout_seconds: 10` on a task that sleeps
# far past it, with the image PRE-LOADED into the cluster so the startup delta
# is a few seconds and the run stays fast.
#
# What is red before #925, precisely. Assertion 3 is red UNCONDITIONALLY: the
# pod deadline was the declared timeout itself, so 10 rather than 220, on any
# host. Assertion 1 — the operator-visible consequence assertion 3 exists to
# produce — is red wherever the startup the agent's clock does not cover
# exceeds the kubelet's deadline-check granularity, which is every real
# cluster (the image pull and volume mount dominate there, and the kubelet then
# killed at 10s from StartTime while the agent would only fire at 13-15s, so
# the operator got the generic container-terminated reason). On a warm local
# k3d with the image already imported that startup is sub-second, so assertion
# 1 alone is a coin flip HERE — which is exactly why assertion 3 asserts the
# arithmetic directly rather than inferring it from the outcome.
#
# Four assertions:
#   1. the timed-out task instance's error_message names `execution_timeout:`
#      (served as `failure_reason` on the API) — the agent won the race;
#   2. the pod's DURABLE OUTCOME RECORD carries the same diagnosis (#930), so
#      the reason survives a report that is never delivered — the control plane
#      unreachable across the timeout, or SIGTERM landing mid-retry;
#   3. a pod created through the REAL DISPATCH PATH carries
#      activeDeadlineSeconds == declared timeout + startup headroom + effective
#      termination grace — locking the seam through dispatch, not just the pod
#      builder, which a unit test already covers;
#   4. an agent frozen after RUNNING has its attempt settled by the AGENT-LOST
#      REAPER within ~90-120s, instead of lingering to the pod deadline (which
#      for a task declaring no timeout is the 24h credential ceiling).
#
# Requirements: k3d, kubectl, docker, jq, curl, the golang-migrate CLI
# (`migrate`), `make build`, and a running dev Postgres/Redis (`make dev-up`).
# Run from the repository root. On Linux/CI set
# LEOFLOW_E2E_HOST_ADDR=host.k3d.internal.
#
# Usage: test/e2e/execution-timeout-e2e.sh
set -euo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${LEOFLOW_E2E_CLUSTER:-leoflow-timeout-e2e}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
HTTP_PORT="${LEOFLOW_E2E_HTTP_PORT:-8080}"
GRPC_PORT="${LEOFLOW_E2E_GRPC_PORT:-9091}"
METRICS_PORT="${LEOFLOW_E2E_METRICS_PORT:-9090}"
PY_VERSION="${LEOFLOW_E2E_PY_VERSION:-3.11}"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-timeout-dag:dev"
DAG_ID="timeoutdag"
API="http://localhost:${HTTP_PORT}"
NS=leoflow
# The server's OWN database, not the Lite dev database `leoflow db migrate`
# targets. This is the default `database.url` the server boots with, migrated
# here with the golang-migrate CLI exactly as the k3d CI jobs do.
DATABASE_URL="${DATABASE_URL:-postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable}"
WORKDIR="$(mktemp -d)"
# Every curl is bounded: an unreachable control plane must surface as a named
# failure here, not as a hang that a later assertion misattributes.
CURL_MAX_TIME=15

# ── The activeDeadlineSeconds arithmetic under assertion (assertion 3) ────────
# TASK_TIMEOUT is the DAG's declaration. STARTUP_HEADROOM is podStartupHeadroom
# (internal/executor/kubernetes.go), which equals defaultDispatchLostThreshold =
# 3m. GRACE_TERM is the effective termination grace the deadline budgets: the
# task declares none, so podDeadlineGraceTerm falls back to the Kubernetes
# default the kubelet itself applies (corev1.DefaultTerminationGracePeriodSeconds
# = 30). Spelled out rather than folded into one number so a change to any rung
# of the arithmetic fails here with the arithmetic visible.
TASK_TIMEOUT=10
STARTUP_HEADROOM=180
GRACE_TERM=30
WANT_DEADLINE=$((TASK_TIMEOUT + STARTUP_HEADROOM + GRACE_TERM))

# ── The agent-lost window under assertion (assertion 4) ──────────────────────
# defaultAgentLostThreshold is 90s and the leader's maintenance loop sweeps
# every reconcileInterval = 30s, so a reap lands 90-120s after the last
# heartbeat (which is at most one 15s heartbeat interval before the freeze).
# The bounds are deliberately loose on both sides: the LOWER bound catches a
# reaper that fired before its threshold (a false reap of a live task), the
# UPPER bound is what separates "the reaper settled it" from "it lingered to the
# pod deadline" — 24h for a task that declares no timeout, so any minute-scale
# ceiling proves the point.
REAP_MIN=45
REAP_MAX=240
# defaultSettlingGrace: NO reaper fires until this long after the process takes
# leadership, so the freeze must not happen inside that window or assertion 4
# would measure the gate rather than the reaper.
SETTLING_GRACE=180

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
dump_pods() {
  printf '\033[1;33m--- task pods (namespace %s) ---\033[0m\n' "$NS" >&2
  kubectl get pods -n "$NS" -o wide >&2 2>&1 || true
  for p in $(kubectl get pods -n "$NS" -o name 2>/dev/null); do
    printf '\033[1;33m--- describe %s ---\033[0m\n' "$p" >&2
    kubectl describe -n "$NS" "$p" >&2 2>&1 | tail -30 || true
    printf '\033[1;33m--- logs %s ---\033[0m\n' "$p" >&2
    kubectl logs -n "$NS" "$p" --all-containers --tail=60 >&2 2>&1 || true
  done
  printf '\033[1;33m--- server.log tail ---\033[0m\n' >&2
  tail -40 "$WORKDIR/server.log" >&2 2>&1 || true
}
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

SERVER_PID=""
cleanup() {
  set +e
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

# api GETs a path with the admin bearer, bounded.
api() { curl -fsS --max-time "$CURL_MAX_TIME" -H "Authorization: Bearer $TOKEN" "$API$1"; }

# task_field prints one field of one task instance in the run, or empty.
task_field() {
  api "/api/v2/dags/$DAG_ID/dagRuns/$RUN_ID/taskInstances" \
    | jq -r --arg t "$1" --arg f "$2" '.task_instances[] | select(.task_id==$t) | .[$f] // ""'
}

# task_pod prints the name of the task pod dispatched for one task_id, or empty.
task_pod() {
  kubectl get pods -n "$NS" -l "leoflow.io/task-id=$1" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true
}

for tool in k3d kubectl docker jq curl migrate; do
  command -v "$tool" >/dev/null || fail "missing required tool: $tool"
done
[ -x "$ROOT/bin/leoflow" ] && [ -x "$ROOT/bin/leoflow-server" ] || fail "build first: make build"

log "Migrating the SERVER's database ($DATABASE_URL)"
# `leoflow db migrate` is hardcoded to the Lite dev database and is NOT the tool
# for this one; the server's schema is owned by migrations/ + golang-migrate,
# exactly as the k3d CI jobs apply it.
migrate -path "$ROOT/migrations" -database "$DATABASE_URL" up 2>&1 | tail -5 \
  || fail "migrating the server database failed — is the dev Postgres up (make dev-up)?"

log "Scaffolding the timeout DAG project"
PROJ="$WORKDIR/$DAG_ID"
mkdir -p "$PROJ"
# Pin the DAG image to the host arch (see e2e.sh): the loader defaults to
# linux/amd64, which fails FROM an arm64 base on a Lima/dev host → ErrImagePull.
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >"$PROJ/leoflow.yaml" <<YAML
schema_version: "1.0"
dag_id: ${DAG_ID}
build:
  platforms:
    - ${HOST_PLATFORM}
defaults:
  # No retries: a retried timeout would re-dispatch and make the assertions race
  # a second attempt's row.
  retries: 0
tasks:
  sleeper:
    execution_timeout_seconds: ${TASK_TIMEOUT}
YAML
# The body sleeps IN-PROCESS rather than shelling out to sleep(1): the agent runs
# the user process under exec.CommandContext with the default cancel (an
# immediate SIGKILL to the direct child only), and its Wait also waits for the
# stdout pipe to close. A shelled-out grandchild would survive the kill holding
# that pipe, so the agent would block past its own deadline and the kubelet
# would win the race for a reason that has nothing to do with #925.
cat >"$PROJ/dag.py" <<'PY'
"""timeoutdag — locks the execution_timeout race against the kubelet."""
from __future__ import annotations

import time

from airflow.sdk import DAG, task


@task
def sleeper() -> None:
    # Declares execution_timeout_seconds: 10 (leoflow.yaml) and runs far past it.
    # The agent's own clock must interrupt this, and the operator must see the
    # timeout named.
    print("sleeper: running past the declared execution_timeout", flush=True)
    time.sleep(600)


@task
def keeper() -> None:
    # Declares NO timeout, so its pod deadline is the attempt-credential ceiling
    # (24h). Once its agent is frozen mid-run, the agent-lost reaper is the only
    # thing that can settle the attempt.
    print("keeper: running until its agent is frozen", flush=True)
    time.sleep(3600)


with DAG("timeoutdag", schedule="@daily", catchup=False, tags=["e2e"]):
    sleeper()
    keeper()
PY
cat >"$PROJ/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building the leoflow base image"
docker build --provenance=false -q -f "$ROOT/runtime/Dockerfile" \
  --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT" >/dev/null \
  || fail "building the base image failed"

log "Creating k3d cluster '$CLUSTER' + the $NS namespace"
k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
k3d cluster create "$CLUSTER" --wait >/dev/null || fail "k3d cluster create failed"
kubectl create namespace "$NS" >/dev/null || fail "creating the $NS namespace failed"

log "Starting the control plane (agents dial ${HOST_ADDR}:${GRPC_PORT})"
export LEOFLOW_AUTH_JWT_SECRET="e2e-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"
export LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${HTTP_PORT}"
export LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${METRICS_PORT}"
export LEOFLOW_DATABASE_URL="$DATABASE_URL"
"$ROOT/bin/leoflow-server" >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
# Wait on /readyz with a NAMED cause. Without this a control plane that never
# boots (a busy port, an unmigrated database) surfaces minutes later as "the
# task never ran", which points at the executor instead of at the boot.
LEADER_AT=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 5 "$API/readyz" >/dev/null 2>&1; then LEADER_AT="$(date +%s)"; break; fi
  kill -0 "$SERVER_PID" 2>/dev/null \
    || fail "the control plane exited during boot; server.log tail:
$(tail -30 "$WORKDIR/server.log")"
  sleep 1
done
[ "$LEADER_AT" != 0 ] || fail "the control plane never became ready on $API/readyz after 60s; server.log tail:
$(tail -30 "$WORKDIR/server.log")"

log "Compiling + building the DAG image"
"$ROOT/bin/leoflow" compile "$PROJ" --image "$DAG_IMAGE" --build --dockerfile Dockerfile \
  -o "$PROJ/dag.json" || fail "leoflow compile failed"
jq -e --argjson want "$TASK_TIMEOUT" \
  '.tasks[] | select(.task_id=="sleeper") | select(.execution_timeout_seconds==$want)' \
  "$PROJ/dag.json" >/dev/null \
  || fail "the compiled sleeper task does not carry execution_timeout_seconds=${TASK_TIMEOUT}"

# PRE-LOAD the image. The startup delta the agent's clock does not cover is
# dominated by the image pull; importing keeps it to a few seconds, which is
# what makes a 10s declared timeout a fair race rather than a coin flip.
log "Importing the images into the cluster (pre-loaded: no pull at dispatch)"
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE"

log "Pushing + triggering"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" \
  --username admin@leoflow.local --password admin)" || fail "minting an admin token failed"
"$ROOT/bin/leoflow" push "$PROJ/dag.json" --server "$API" --token "$TOKEN" || fail "leoflow push failed"
RUN_ID="$(curl -fsS --max-time "$CURL_MAX_TIME" -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id')" || fail "triggering the run failed"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ] || fail "no dag_run_id returned"

# ── Assertions 1-3: the sleeper races the kubelet and loses nothing ──────────
log "Waiting for 'sleeper' to fail on its execution_timeout"
POD_DEADLINE=""
POD_RECORD=""
deadline=$(( $(date +%s) + 300 ))
while :; do
  # Capture the pod's spec + durable record WHILE the pod exists: the reconciler
  # garbage-collects a finished pod after its grace period, and these two are the
  # only place assertions 2 and 3 can be read from.
  pod="$(task_pod sleeper)"
  if [ -n "$pod" ]; then
    [ -n "$POD_DEADLINE" ] || POD_DEADLINE="$(kubectl get pod -n "$NS" "$pod" \
      -o jsonpath='{.spec.activeDeadlineSeconds}' 2>/dev/null || true)"
    [ -n "$POD_RECORD" ] || POD_RECORD="$(kubectl get pod -n "$NS" "$pod" \
      -o jsonpath='{range .status.containerStatuses[?(@.name=="task")]}{.state.terminated.message}{end}' 2>/dev/null || true)"
  fi
  state="$(task_field sleeper state)"
  echo "  sleeper=$state pod=${pod:-none} deadline=${POD_DEADLINE:-?}"
  case "$state" in
    failed) break ;;
    success) fail "'sleeper' succeeded — it must be interrupted by its execution_timeout" ;;
  esac
  [ "$(date +%s)" -lt "$deadline" ] || fail "timeout waiting for 'sleeper' to fail"
  sleep 5
done

REASON="$(task_field sleeper failure_reason)"
log "sleeper error_message: ${REASON:-<empty>}"
case "$REASON" in
  *execution_timeout:*) : ;;
  *) fail "sleeper error_message = '${REASON}', want it to contain 'execution_timeout:' — the kubelet won the race, so the agent's diagnosis was lost (#925)" ;;
esac

log "Asserting the DURABLE outcome record carries the same diagnosis (#930)"
[ -n "$POD_RECORD" ] || fail "the sleeper pod left no termination-message record; a lost report would settle as a bare exit code"
case "$POD_RECORD" in
  *execution_timeout*) log "durable record: $POD_RECORD" ;;
  *) fail "durable outcome record = '${POD_RECORD}', want it to carry the execution_timeout reason (#930)" ;;
esac

log "Asserting the dispatched pod's activeDeadlineSeconds == ${TASK_TIMEOUT}+${STARTUP_HEADROOM}+${GRACE_TERM}"
[ -n "$POD_DEADLINE" ] || fail "never observed the sleeper pod's activeDeadlineSeconds (pod GC'd before it was read?)"
[ "$POD_DEADLINE" = "$WANT_DEADLINE" ] \
  || fail "activeDeadlineSeconds = ${POD_DEADLINE}, want ${WANT_DEADLINE} (declared ${TASK_TIMEOUT} + headroom ${STARTUP_HEADROOM} + grace ${GRACE_TERM}) — dispatch is not applying the #925 arithmetic"

# ── Assertion 4: a frozen agent is settled by the agent-lost reaper ──────────
log "Waiting for 'keeper' to reach running"
deadline=$(( $(date +%s) + 300 ))
while [ "$(task_field keeper state)" != running ]; do
  st="$(task_field keeper state)"
  case "$st" in failed|success) fail "'keeper' reached $st before it could be frozen" ;; esac
  [ "$(date +%s)" -lt "$deadline" ] || fail "timeout waiting for 'keeper' to reach running"
  sleep 3
done
KEEPER_POD="$(task_pod keeper)"
[ -n "$KEEPER_POD" ] || fail "could not find the keeper pod"

# No reaper fires inside the leader-settling grace, so wait it out first —
# otherwise this measures the gate opening, not the reaper's threshold.
elapsed=$(( $(date +%s) - LEADER_AT ))
if [ "$elapsed" -lt "$((SETTLING_GRACE + 15))" ]; then
  waitfor=$(( SETTLING_GRACE + 15 - elapsed ))
  log "Waiting ${waitfor}s for the leader-settling grace to elapse before freezing the agent"
  sleep "$waitfor"
fi

# FREEZE the agent rather than killing it. SIGKILL would end the container, the
# pod would go terminal, and the RECONCILER would settle it in one 30s sweep —
# a different mechanism entirely. SIGSTOP leaves the pod Running with an agent
# that heartbeats no more, which is exactly the shape the agent-lost reaper
# exists for (a wedged or partitioned agent). It has to come from the k3d NODE's
# PID namespace: the agent is the pod container's PID 1, and the kernel ignores
# SIGSTOP sent to a namespace's init from inside that namespace, so a
# `kubectl exec ... kill -STOP 1` would be silently dropped.
log "Freezing the keeper pod's agent (SIGSTOP from the k3d node)"
NODE="k3d-${CLUSTER}-server-0"
CID="$(kubectl get pod -n "$NS" "$KEEPER_POD" \
  -o jsonpath='{range .status.containerStatuses[?(@.name=="task")]}{.containerID}{end}')"
CID="${CID#*://}"
[ -n "$CID" ] || fail "could not read the keeper pod's container id"
AGENT_PID="$(docker exec "$NODE" crictl inspect -o json "$CID" 2>/dev/null | jq -r '.info.pid // empty')"
[ -n "$AGENT_PID" ] || fail "could not resolve the agent's host pid for container $CID via crictl on $NODE"
docker exec "$NODE" /bin/sh -c "kill -STOP $AGENT_PID" || fail "SIGSTOP of the agent (pid $AGENT_PID) failed"
FROZE_AT="$(date +%s)"
log "Agent frozen (host pid $AGENT_PID); expecting the agent-lost reaper in ${REAP_MIN}-${REAP_MAX}s"

deadline=$(( FROZE_AT + REAP_MAX ))
while :; do
  state="$(task_field keeper state)"
  now="$(date +%s)"
  echo "  keeper=$state  t+$(( now - FROZE_AT ))s"
  [ "$state" = failed ] && break
  [ "$now" -lt "$deadline" ] \
    || fail "'keeper' was still '$state' ${REAP_MAX}s after its agent stopped heartbeating — the agent-lost reaper did not settle it, so the attempt lingers to the pod deadline (the 24h credential ceiling)"
  sleep 5
done
REAP_SECS=$(( $(date +%s) - FROZE_AT ))
KEEPER_REASON="$(task_field keeper failure_reason)"
log "keeper settled at t+${REAP_SECS}s with error_message: ${KEEPER_REASON:-<empty>}"
[ "$REAP_SECS" -ge "$REAP_MIN" ] \
  || fail "'keeper' was settled ${REAP_SECS}s after the freeze, under the ${REAP_MIN}s floor — a reaper firing before its threshold would also false-reap live tasks"
case "$KEEPER_REASON" in
  *agent_lost*) : ;;
  *) fail "keeper error_message = '${KEEPER_REASON}', want it to name agent_lost — something other than the agent-lost reaper settled the attempt" ;;
esac

log "execution_timeout e2e passed (agent won the race, the diagnosis is durable, dispatch applies the deadline arithmetic, and a frozen agent is reaped in ${REAP_SECS}s)"
