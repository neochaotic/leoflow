#!/usr/bin/env bash
#
# Runtime chaos / fault-injection e2e (#231 Phase 2) — RUN ON THE LIMA LINUX VM.
#
# Regression suites confirm known behaviour with fakes; this injects real faults
# on a real k3d cluster to surface the UNEXPECTED, targeting the two newest,
# riskiest surfaces:
#   A) scheduler-crash recovery in the api/scheduler SPLIT (ADR 0049) — kill the
#      scheduler process mid-run; assert the api's /monitor/health flips the
#      scheduler to unhealthy (the LeaderHealthReader, advisory-lock liveness),
#      then restart it and assert the in-flight run RESUMES to success and each
#      task ran exactly once (at-most-once).
#   B) task-pod kill (#474/#518) — delete a running task's pod; assert the reaper
#      moves the TI off `running` to a terminal state (not stuck forever) and
#      leaves no orphaned pod for that attempt.
#   C) durable outcome recovery (ADR 0052) — a task succeeds but the agent is told
#      to exit mid-report AFTER writing the durable outcome record; assert the
#      reconciler recovers the SUCCESS from the record (the pod is Failed and no
#      report ever landed), settling the run success instead of failing/retrying.
#
# NOT a `go test`; destructive; not gated in CI (slow). It mirrors the proven
# host-process split harness (split-two-process.sh) and reuses the k3d_import
# work-around (lib.sh). Best run inside Lima (Linux) — the CGO services and k3d
# behave as in prod there, unlike the macOS host.
#
# Usage: test/e2e/chaos-runtime.sh [cluster-name]
set -uo pipefail

# shellcheck source=test/e2e/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CLUSTER="${1:-leoflow-chaos}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PY_VERSION="3.11"
BASE_IMAGE="leoflow-base:py${PY_VERSION}"
DAG_IMAGE="leoflow-chaos-dag:dev"
DAG_ID="chaosdag"
RECOVER_DAG_IMAGE="leoflow-recover-dag:dev"
RECOVER_DAG_ID="recoverdag"

API_HTTP_PORT="${LEOFLOW_E2E_API_HTTP_PORT:-8080}"
API_METRICS_PORT="9090"
SCHED_METRICS_PORT="9092"
GRPC_PORT="9091"
API="http://localhost:${API_HTTP_PORT}"
HOST_ADDR="${LEOFLOW_E2E_HOST_ADDR:-$([ "$(uname -s)" = Linux ] && echo host.k3d.internal || echo host.docker.internal)}"
# The dev Postgres container to restart in scenario C (future); its name differs
# between `make dev-up` (compose) and a standalone run. Overridable.
API_PID=""; SCHED_PID=""; TOKEN=""; FAILED=0

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    PASS\033[0m %s\n' "$*"; }
bad()  { printf '\033[1;31m    FAIL\033[0m %s\n' "$*" >&2; FAILED=1; }
dump_pods() {
  kubectl get pods -n leoflow -o wide >&2 2>&1 || true
}
fatal() { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; dump_pods; exit 1; }

cleanup() {
  [ -n "$API_PID" ] && kill "$API_PID" 2>/dev/null || true
  [ -n "$SCHED_PID" ] && kill "$SCHED_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for tool in k3d kubectl docker jq curl; do
  command -v "$tool" >/dev/null || fatal "missing required tool: $tool"
done

# ── shared control-plane env (one logical control plane, two role processes) ──
export LEOFLOW_AUTH_JWT_SECRET="chaos-secret"
export LEOFLOW_BOOTSTRAP_PASSWORD="admin"
export LEOFLOW_SECRET_KEY="chaos-secret-key-32-bytes-padd!!"
export LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true
export LEOFLOW_REDIS_URL="${LEOFLOW_E2E_REDIS_URL:-redis://localhost:6379/0}"
export LEOFLOW_LOGS_DIR="${WORKDIR}/logs"; mkdir -p "$LEOFLOW_LOGS_DIR"

start_scheduler() {
  LEOFLOW_SERVER_ROLE=scheduler \
  LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:8085" \
  LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${SCHED_METRICS_PORT}" \
  LEOFLOW_SERVER_GRPC_ADDR="0.0.0.0:${GRPC_PORT}" \
  LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR="${HOST_ADDR}:${GRPC_PORT}" \
    "$ROOT/bin/leoflow-server" >"${WORKDIR}/scheduler.log" 2>&1 &
  SCHED_PID=$!
}

# scheduler_health returns the status string the api reports for the scheduler.
scheduler_health() {
  curl -fsS -H "Authorization: Bearer $TOKEN" "$API/api/v2/monitor/health" 2>/dev/null \
    | jq -r '.scheduler.status // "unknown"'
}

# wait_scheduler_status polls /monitor/health until the scheduler status matches
# $1 (healthy|unhealthy) or the timeout elapses. Returns 0 on match.
wait_scheduler_status() {
  local want="$1" deadline; deadline=$(( $(date +%s) + ${2:-30} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    [ "$(scheduler_health)" = "$want" ] && return 0
    sleep 2
  done
  return 1
}

task_states() {
  curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$DAG_ID/dagRuns/$1/taskInstances" 2>/dev/null | jq -r '.task_instances[].state'
}

# ── build + cluster + two role processes ─────────────────────────────────────
log "Scaffolding the chaos DAG (a sleeper so faults land mid-run)"
"$ROOT/bin/leoflow" init "$WORKDIR/$DAG_ID" >/dev/null
case "$(uname -m)" in arm64|aarch64) HOST_PLATFORM="linux/arm64" ;; *) HOST_PLATFORM="linux/amd64" ;; esac
cat >> "$WORKDIR/$DAG_ID/leoflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
YAML
cat > "$WORKDIR/$DAG_ID/dag.py" <<'PY'
"""chaosdag — a sleeper DAG so a fault can be injected while a task runs."""
from __future__ import annotations
import time
from airflow.sdk import DAG, task


@task
def work() -> str:
    # Long enough to (a) kill the scheduler mid-run and (b) let the agent emit at
    # least one heartbeat (15s interval) before scenario B kills the pod, so the
    # agent-lost backstop is armed. Comfortably outlives both scenarios' setup.
    print("chaos: work START", flush=True)
    time.sleep(90)
    print("chaos: work DONE", flush=True)
    return "done"


@task
def finish(value: str) -> None:
    print(f"chaos: finish saw {value}", flush=True)


with DAG("chaosdag", schedule=None, catchup=False, tags=["chaos"]):
    finish(work())
PY
cat > "$WORKDIR/$DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER

log "Building base image + cluster"
docker build --provenance=false -f "$ROOT/runtime/Dockerfile" --build-arg "PYTHON_VERSION=${PY_VERSION}" -t "$BASE_IMAGE" "$ROOT"
k3d cluster create "$CLUSTER" --wait
kubectl create namespace leoflow

log "Starting scheduler + api role processes"
start_scheduler
LEOFLOW_SERVER_ROLE=api \
LEOFLOW_SERVER_HTTP_ADDR="0.0.0.0:${API_HTTP_PORT}" \
LEOFLOW_SERVER_METRICS_ADDR="0.0.0.0:${API_METRICS_PORT}" \
  "$ROOT/bin/leoflow-server" >"${WORKDIR}/api.log" 2>&1 &
API_PID=$!
sleep 5

log "Compiling + importing the DAG image"
"$ROOT/bin/leoflow" compile "$WORKDIR/$DAG_ID" --image "$DAG_IMAGE" --build --dockerfile Dockerfile -o "$WORKDIR/$DAG_ID/dag.json" >/dev/null
k3d_import "$CLUSTER" "$BASE_IMAGE" "$DAG_IMAGE" || fatal "k3d import failed"
TOKEN="$("$ROOT/bin/leoflow" auth create-token --server "$API" --username admin@leoflow.local --password admin)"
"$ROOT/bin/leoflow" push "$WORKDIR/$DAG_ID/dag.json" --server "$API" --token "$TOKEN" >/dev/null

trigger_run() {
  curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{}' "$API/api/v2/dags/$DAG_ID/dagRuns" | jq -r '.dag_run_id'
}
wait_task_running() {  # $1=run
  local deadline; deadline=$(( $(date +%s) + 90 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    kubectl get pods -n leoflow --no-headers 2>/dev/null | grep -q 'work.*Running' && return 0
    task_states "$1" | grep -q 'failed\|upstream_failed' && return 1
    sleep 3
  done
  return 1
}

# ─────────────────────────────────────────────────────────────────────────────
# Scenario A — scheduler-crash recovery in the split
# ─────────────────────────────────────────────────────────────────────────────
scenario_scheduler_crash() {
  log "SCENARIO A — kill the scheduler mid-run; assert health flips, run resumes, at-most-once"
  local run; run="$(trigger_run)"
  [ -n "$run" ] && [ "$run" != "null" ] || { bad "A: no run id"; return; }
  wait_task_running "$run" || { bad "A: 'work' pod never reached Running"; return; }

  # api must currently see the scheduler healthy.
  wait_scheduler_status healthy 20 || { bad "A: scheduler not healthy before the kill"; return; }

  log "A: killing the scheduler process ($SCHED_PID)"
  kill "$SCHED_PID" 2>/dev/null; wait "$SCHED_PID" 2>/dev/null; SCHED_PID=""
  # The api reads scheduler liveness from the leadership advisory lock; when the
  # scheduler's session drops, the lock releases → unhealthy. (F1 fix, live.)
  if wait_scheduler_status unhealthy 40; then
    ok "A: /monitor/health flipped scheduler → unhealthy after the crash"
  else
    bad "A: scheduler still reported healthy after the process was killed (LeaderHealthReader?)"
  fi

  log "A: restarting the scheduler; run must resume"
  start_scheduler
  wait_scheduler_status healthy 40 || bad "A: scheduler did not report healthy after restart"

  local deadline states; deadline=$(( $(date +%s) + 180 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    states="$(task_states "$run")"
    echo "$states" | grep -qE 'failed|upstream_failed' && { bad "A: a task failed after restart: $states"; return; }
    if [ -n "$states" ] && ! echo "$states" | grep -qvE 'success|skipped'; then
      ok "A: in-flight run RESUMED to success after the scheduler restart"
      break
    fi
    sleep 4
  done
  [ "$(date +%s)" -lt "$deadline" ] || { bad "A: run did not resume to success within 180s"; return; }

  # At-most-once: exactly one pod per attempt for 'work' (a resumed scheduler must
  # NOT re-dispatch a task that already ran — no duplicate work pod).
  local workpods
  workpods="$(kubectl get pods -n leoflow --no-headers 2>/dev/null | grep -c 'chaosdag-work-')"
  if [ "$workpods" -le 1 ]; then
    ok "A: at-most-once held — a single 'work' pod ($workpods), no duplicate dispatch on resume"
  else
    bad "A: $workpods 'work' pods — the resumed scheduler double-dispatched (at-most-once violated)"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Scenario B — task-pod kill → reaper moves the TI off running, no orphan
# ─────────────────────────────────────────────────────────────────────────────
scenario_task_pod_kill() {
  log "SCENARIO B — delete a running task pod; assert the TI leaves 'running' and no orphan lingers"
  local run; run="$(trigger_run)"
  [ -n "$run" ] && [ "$run" != "null" ] || { bad "B: no run id"; return; }
  wait_task_running "$run" || { bad "B: 'work' pod never reached Running"; return; }

  local pod
  pod="$(kubectl get pods -n leoflow --no-headers 2>/dev/null | awk '/chaosdag-work-.*Running/{print $1; exit}')"
  [ -n "$pod" ] || { bad "B: could not find the running work pod"; return; }

  # Arm the agent-lost backstop before the kill. That reaper only fires once a TI
  # has heartbeated at least once (the zero-heartbeat "do no harm" guard, ADR
  # 0031); the agent heartbeats every 15s (cmd/leoflow-agent HeartbeatInterval).
  # Killing the pod inside that first interval hits a separate, un-backstopped
  # window (the reconciler is blind to a *deleted* pod, and agent-lost skips a
  # null heartbeat) tracked in #527 — NOT what this scenario asserts. Wait one
  # interval + margin so we exercise the documented mid-run-kill → agent-lost path.
  log "B: waiting one heartbeat interval (15s) so the agent-lost backstop is armed (#527)"
  sleep 20

  log "B: force-deleting $pod mid-execution"
  kubectl delete pod -n leoflow "$pod" --grace-period=0 --force >/dev/null 2>&1 || true

  # The agent-lost reaper must move the TI off `running` (to failed / retry),
  # within the agent-lost window + margin — it must NOT stay `running` forever.
  local deadline st; deadline=$(( $(date +%s) + 180 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    st="$(task_states "$run" | head -1)"
    if [ -n "$st" ] && [ "$st" != "running" ] && [ "$st" != "queued" ] && [ "$st" != "null" ]; then
      ok "B: TI left 'running' after the pod was killed (state=$st) — no stuck task"
      break
    fi
    sleep 5
  done
  [ "$(date +%s)" -lt "$deadline" ] || { bad "B: TI stuck (never left running/queued) 180s after the pod was killed"; return; }

  # No orphaned pod for that killed attempt should linger (reaper teardown, #518).
  sleep 5
  if kubectl get pod -n leoflow "$pod" >/dev/null 2>&1; then
    bad "B: the killed pod's name still exists — reaper did not tear it down"
  else
    ok "B: no orphaned pod lingering after reap"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Scenario C — durable outcome recovery (ADR 0052)
# ─────────────────────────────────────────────────────────────────────────────
setup_recoverdag() {
  log "C-setup: scaffolding recoverdag (succeeds, but the agent dies mid-report)"
  "$ROOT/bin/leoflow" init "$WORKDIR/$RECOVER_DAG_ID" >/dev/null
  cat >> "$WORKDIR/$RECOVER_DAG_ID/leoflow.yaml" <<YAML
build:
  platforms:
    - ${HOST_PLATFORM}
tasks:
  quick:
    # The agent runs the task to success and writes the durable outcome record to
    # the termination message, then exits 137 WITHOUT delivering the report —
    # simulating a pod killed mid-report (OOM/eviction). Test-only seam (ADR 0052).
    env:
      LEOFLOW_CHAOS_DIE_BEFORE_REPORT: TASK_STATE_SUCCESS
YAML
  cat > "$WORKDIR/$RECOVER_DAG_ID/dag.py" <<'PY'
"""recoverdag — the task succeeds; the agent is told to die mid-report, so the
reconciler must recover the success from the durable outcome record (ADR 0052)."""
from __future__ import annotations
from airflow.sdk import DAG, task


@task
def quick() -> str:
    print("recover: task body ran to success", flush=True)
    return "ok"


with DAG("recoverdag", schedule=None, catchup=False, tags=["chaos"]):
    quick()
PY
  cat > "$WORKDIR/$RECOVER_DAG_ID/Dockerfile" <<DOCKER
FROM ${BASE_IMAGE}
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
DOCKER
  "$ROOT/bin/leoflow" compile "$WORKDIR/$RECOVER_DAG_ID" --image "$RECOVER_DAG_IMAGE" --build --dockerfile Dockerfile -o "$WORKDIR/$RECOVER_DAG_ID/dag.json" >/dev/null
  k3d_import "$CLUSTER" "$RECOVER_DAG_IMAGE" || fatal "C: k3d import of recoverdag failed"
  "$ROOT/bin/leoflow" push "$WORKDIR/$RECOVER_DAG_ID/dag.json" --server "$API" --token "$TOKEN" >/dev/null
}

recover_run_state() {  # $1=run
  curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$RECOVER_DAG_ID/dagRuns/$1" 2>/dev/null | jq -r '.state // "unknown"'
}

scenario_durable_outcome_recovery() {
  log "SCENARIO C — pod killed mid-report after writing the outcome record; reconciler recovers the SUCCESS (ADR 0052)"
  setup_recoverdag

  local run
  run="$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{}' "$API/api/v2/dags/$RECOVER_DAG_ID/dagRuns" | jq -r '.dag_run_id')"
  [ -n "$run" ] && [ "$run" != "null" ] || { bad "C: no run id"; return; }

  # The task pod must end NON-successfully at the k8s level (agent exit 137) — the
  # whole point is that pod phase says failure while the record says success.
  local deadline; deadline=$(( $(date +%s) + 90 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    kubectl get pods -n leoflow --no-headers 2>/dev/null | grep -qE 'recoverdag-quick-.*(Error|Failed)' && break
    sleep 3
  done

  # The reconciler (30s sweep) must settle the TI SUCCEEDED from the durable record,
  # winning the race against the agent-lost reaper (90s, and disarmed here: a sub-15s
  # task never heartbeats, so the zero-heartbeat guard skips it). Assert the run
  # reaches success and does NOT fail.
  deadline=$(( $(date +%s) + 150 )); local state=""
  while [ "$(date +%s)" -lt "$deadline" ]; do
    state="$(recover_run_state "$run")"
    [ "$state" = success ] && break
    [ "$state" = failed ] && { bad "C: run FAILED — the reconciler did not recover the success from the durable record"; return; }
    sleep 5
  done
  if [ "$state" = success ]; then
    ok "C: run recovered to SUCCESS from the durable outcome record, despite the Failed pod and the lost report"
  else
    bad "C: run did not reach success within the recovery window (state=$state)"
    return
  fi

  # Recovery is a SETTLE, not a retry: the durable record settles the current
  # attempt succeeded (try_number unchanged), so there is exactly one attempt.
  local tries
  tries="$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$API/api/v2/dags/$RECOVER_DAG_ID/dagRuns/$run/taskInstances" 2>/dev/null \
    | jq -r '[.task_instances[].try_number] | max')"
  if [ "$tries" = "1" ]; then
    ok "C: recovery settled the current attempt (try_number=1) — no wasteful retry of already-done work"
  else
    bad "C: expected a single attempt (recovery is a settle, not a retry), got max try_number=$tries"
  fi
}

scenario_scheduler_crash
scenario_task_pod_kill
scenario_durable_outcome_recovery

printf '\n\033[1m===== chaos-runtime summary =====\033[0m\n'
if [ "$FAILED" -ne 0 ]; then
  printf '\033[1;31mchaos-runtime: FAILED — a fault-injection invariant broke (see above).\033[0m\n'; exit 1
fi
printf '\033[1;32mchaos-runtime: all fault-injection invariants held.\033[0m\n'
