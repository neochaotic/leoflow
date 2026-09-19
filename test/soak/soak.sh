#!/usr/bin/env bash
#
# soak.sh: the Leoflow long-running resilience battery.
#
# One command provisions everything, runs a realistic scheduled workload, asserts
# invariants continuously, collects the evidence and tears down. It is safe to
# leave unattended: it has a wall-clock ceiling, a disk budget and a kill switch,
# and every artifact it produces is written as it goes, not at the end.
#
#   bash test/soak/soak.sh                          # 30 min, no faults
#   bash test/soak/soak.sh --duration 12h --faults standard
#   bash test/soak/soak.sh --duration 6m --faults selftest-red   # must go RED
#
# What it runs: test/soak/dags, six DAG projects on cron schedules, on
# `leoflow lite` with the subprocess executor against a dedicated Postgres
# container. No cloud, no cluster, no paid service. See test/soak/README.md for
# the design, the measurements and the cost budget.
#
# Exit codes: 0 every invariant held; 1 at least one violation; 2 the harness
# itself could not run.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SOAK_DIR="$ROOT/test/soak"
COMPOSE="$SOAK_DIR/docker-compose.soak.yaml"

# ── Defaults. Every one of these is a budget decision; see README.md. ─────────
DURATION="30m"
FAULTS="none"
LABEL="soak"
API_PORT="${SOAK_API_PORT:-18700}"
FIXTURE_PORT="${SOAK_FIXTURE_PORT:-18701}"
PG_PORT="${SOAK_PG_PORT:-55433}"
SAMPLE_INTERVAL="${SOAK_SAMPLE_INTERVAL:-10s}"
ROWS="${SOAK_ROWS:-400000}"
FANOUT_ROWS="${SOAK_FANOUT_ROWS:-200000}"
LONG_SECONDS="${SOAK_LONG_SECONDS:-420}"
MAX_DB_BYTES="${SOAK_MAX_DB_BYTES:-$((4 * 1024 * 1024 * 1024))}"
MAX_DATA_BYTES="${SOAK_MAX_DATA_BYTES:-$((4 * 1024 * 1024 * 1024))}"
MIN_FREE_BYTES="${SOAK_MIN_FREE_BYTES:-$((10 * 1024 * 1024 * 1024))}"
OUT_ROOT="${SOAK_OUT_ROOT:-$ROOT/.soak}"
KEEP_DB=0
PURGE=0
FRESH=1

usage() {
  sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'USAGE'

Flags:
  --duration D      wall-clock ceiling (default 30m, hard max 72h)
  --faults PLAN     none | standard | selftest-red | "kind:dur:offset,..."
  --label NAME      label recorded in every sample (default soak)
  --out DIR         evidence directory (default .soak/<timestamp>)
  --keep-db         do not stop the Postgres container on teardown
  --purge           also delete the Postgres volume on teardown (destroys history)
  --no-fresh        keep the existing soak database instead of resetting it
  --rows N          rows the ingest DAG generates per run (default 400000)
  -h, --help        this text

Fault kinds:
  pgpause:DUR       docker pause the soak Postgres for DUR, then unpause
  sigstop:DUR       SIGSTOP the control-plane process for DUR, then SIGCONT
  restart           SIGKILL the control plane and start it again
  overrun:DUR       declare a 45s fault window but pause Postgres for DUR.
                    This is the deliberately-red self test: a real fault that
                    outlives the window the harness claimed for it.
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --duration) DURATION="$2"; shift 2 ;;
    --faults)   FAULTS="$2"; shift 2 ;;
    --label)    LABEL="$2"; shift 2 ;;
    --out)      OUT_DIR="$2"; shift 2 ;;
    --rows)     ROWS="$2"; shift 2 ;;
    --keep-db)  KEEP_DB=1; shift ;;
    --purge)    PURGE=1; shift ;;
    --no-fresh) FRESH=0; shift ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 2 ;;
  esac
done

OUT_DIR="${OUT_DIR:-$OUT_ROOT/$(date -u +%Y%m%dT%H%M%SZ)-$LABEL}"
mkdir -p "$OUT_DIR" || { echo "cannot create $OUT_DIR" >&2; exit 2; }
FAULTS_FILE="$OUT_DIR/faults.jsonl"
: > "$FAULTS_FILE"

# `leoflow lite` sets LEOFLOW_DATABASE_URL itself, unconditionally, to the DSN it
# derives from the running user's HOME (internal/cli/dev.go sharedServerEnv +
# internal/cli/dsn.go). A soak therefore cannot hand Lite a database URL. What it
# CAN do is give Lite a private HOME containing a `db-port` file, which is the
# supported knob Lite uses to find its datastore. That is how the soak gets its
# own Postgres on its own port without touching the developer's leoflow_dev.
#
# The private HOME is persistent on purpose: the per-DAG venvs cost minutes and
# gigabytes to rebuild, and a weekend battery should not pay that on every start.
SOAK_HOME="${SOAK_HOME:-$HOME/.leoflow-soak}"
mkdir -p "$SOAK_HOME/.leoflow/dev"
printf '%s' "$PG_PORT" > "$SOAK_HOME/.leoflow/dev/db-port"
export SOAK_DATA_DIR="${SOAK_DATA_DIR:-$SOAK_HOME/data}"
mkdir -p "$SOAK_DATA_DIR"

WORKSPACE="$SOAK_HOME/workspace"
DB_URL="postgres://leoflow:leoflow@127.0.0.1:${PG_PORT}/leoflow_dev?sslmode=disable"
API="http://127.0.0.1:${API_PORT}"

LITE_PID=""; FIXTURE_PID=""; MONITOR_PID=""; FAULT_PID=""; WATCHDOG_PID=""
VERDICT_CODE=2

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m    !!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 2; }

# ── Teardown. Idempotent, runs on every exit path including the kill switch. ──
cleanup() {
  local code=$?
  log "tearing down"
  for pid in "$WATCHDOG_PID" "$FAULT_PID" "$MONITOR_PID" "$LITE_PID" "$FIXTURE_PID"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null
  done
  # A paused container would survive the harness and hold its memory, so always
  # unpause before stopping, whatever state the fault injector left it in.
  docker unpause leoflow-soak-postgres >/dev/null 2>&1
  sleep 1
  for pid in "$MONITOR_PID" "$LITE_PID" "$FIXTURE_PID"; do
    [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null
  done
  collect_evidence
  if [ "$KEEP_DB" = "0" ]; then
    if [ "$PURGE" = "1" ]; then
      docker compose -f "$COMPOSE" down -v >/dev/null 2>&1 && ok "postgres stopped and volume removed"
    else
      docker compose -f "$COMPOSE" down >/dev/null 2>&1 && ok "postgres stopped (volume kept; --purge removes it)"
    fi
  fi
  echo
  log "evidence: $OUT_DIR"
  [ -f "$OUT_DIR/summary.md" ] && sed -n '1,6p' "$OUT_DIR/summary.md"
  exit "$([ "$VERDICT_CODE" != 2 ] && echo "$VERDICT_CODE" || echo "$code")"
}
trap cleanup EXIT INT TERM

collect_evidence() {
  # Everything here is best-effort: collection must never be the reason a soak
  # loses the report it already wrote.
  {
    echo "# environment"
    echo "date_utc=$(date -u +%FT%TZ)"
    echo "uname=$(uname -srm)"
    echo "go=$(go version 2>/dev/null)"
    echo "docker=$(docker --version 2>/dev/null)"
    echo "leoflow_commit=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null)"
    echo "duration=$DURATION faults=$FAULTS label=$LABEL"
    echo "rows=$ROWS fanout_rows=$FANOUT_ROWS long_seconds=$LONG_SECONDS"
  } > "$OUT_DIR/environment.txt" 2>/dev/null
  curl -fsS --max-time 5 "$API/metrics" > "$OUT_DIR/metrics-final.prom" 2>/dev/null
  curl -fsS --max-time 5 "http://127.0.0.1:${FIXTURE_PORT}/fixture/stats" > "$OUT_DIR/fixture-stats.json" 2>/dev/null
  docker exec leoflow-soak-postgres psql -U leoflow -d leoflow_dev -At -c \
    "SELECT relname, n_live_tup, pg_total_relation_size(relid) FROM pg_stat_user_tables ORDER BY 3 DESC" \
    > "$OUT_DIR/pg-table-sizes.txt" 2>/dev/null
  du -sk "$SOAK_DATA_DIR" "$SOAK_HOME/.leoflow/dev/venvs" 2>/dev/null > "$OUT_DIR/disk-usage.txt"
}

# ── Preflight. Refuse early and loudly rather than half-provisioning. ─────────
log "preflight"
for tool in docker go python3 curl jq; do
  command -v "$tool" >/dev/null || die "missing required tool: $tool"
done
docker info >/dev/null 2>&1 || die "docker is not running"
for port in "$API_PORT" "$FIXTURE_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    die "port $port is already in use; set SOAK_API_PORT / SOAK_FIXTURE_PORT"
  fi
done
# The wall-clock ceiling is enforced twice: here, and inside the monitor. A soak
# that can outlive its own budget is not a bounded experiment.
DURATION_S="$(python3 -c '
import re,sys
m=re.fullmatch(r"(\d+)([smhd])", sys.argv[1])
if not m: sys.exit("bad --duration: use 30m, 12h, 2d")
n,u=int(m.group(1)),m.group(2)
print(n*{"s":1,"m":60,"h":3600,"d":86400}[u])
' "$DURATION")" || die "bad --duration $DURATION"
[ "$DURATION_S" -le 259200 ] || die "--duration exceeds the 72h ceiling"
ok "preflight passed (duration ${DURATION_S}s)"

log "building binaries"
BIN="$SOAK_HOME/bin"; mkdir -p "$BIN"
go build -o "$BIN/leoflow" ./cmd/leoflow || die "building leoflow"
go build -o "$BIN/leoflow-server" ./cmd/leoflow-server || die "building leoflow-server"
go build -o "$BIN/leoflow-agent" ./cmd/leoflow-agent || die "building leoflow-agent"
go build -o "$BIN/soak-fixture" ./test/soak/fixture || die "building the fixture server"
go build -o "$BIN/soak-monitor" ./test/soak/monitor || die "building the monitor"
export PATH="$BIN:$PATH"
ok "binaries in $BIN"

log "starting the soak Postgres (dedicated container, 1 CPU / 1 GiB)"
docker compose -f "$COMPOSE" up -d >/dev/null 2>&1 || die "docker compose up failed"
# Two readiness checks, not one. `pg_isready` inside the container answers on the
# Unix socket, which the bootstrap postmaster brings up during initdb BEFORE the
# real server starts listening on TCP. Trusting it alone gives a green light a
# second or two too early, and the next command fails with a connection refused
# that looks like a configuration error. What matters is that the port is
# reachable from the HOST, over the name `localhost`, because that is the DSN
# `leoflow lite` builds for itself.
for _ in $(seq 1 90); do
  docker exec leoflow-soak-postgres pg_isready -U leoflow -d leoflow_dev >/dev/null 2>&1 \
    && python3 -c '
import socket, sys
s = socket.socket()
s.settimeout(2)
try:
    s.connect(("localhost", int(sys.argv[1])))
except OSError:
    sys.exit(1)
finally:
    s.close()
' "$PG_PORT" >/dev/null 2>&1 && break
  sleep 1
done
docker exec leoflow-soak-postgres pg_isready -U leoflow -d leoflow_dev >/dev/null 2>&1 \
  || die "soak Postgres did not become ready (container socket)"
# The warehouse the operator leg writes into: a separate database, so the
# workload never shares a table with Leoflow's own metadata and the two growth
# curves in the budget stay separable.
docker exec leoflow-soak-postgres psql -U leoflow -d postgres -At -c \
  "SELECT 1 FROM pg_database WHERE datname='soak_warehouse'" 2>/dev/null | grep -q 1 \
  || docker exec leoflow-soak-postgres createdb -U leoflow soak_warehouse >/dev/null 2>&1
ok "postgres up on 127.0.0.1:${PG_PORT}, warehouse database present"

export SOAK_DATABASE_URL="$DB_URL"

if [ "$FRESH" = "1" ]; then
  log "resetting the soak database (under the private HOME, so the developer's leoflow_dev is untouched)"
  # The output is captured rather than discarded: a fatal that will not say why
  # costs a whole CI round trip to diagnose, which is exactly what happened the
  # first time this ran on a clean runner.
  HOME="$SOAK_HOME" "$BIN/leoflow" db reset --yes > "$OUT_DIR/db-reset.log" 2>&1 \
    || { tail -20 "$OUT_DIR/db-reset.log" >&2; die "leoflow db reset failed (log above, full copy in $OUT_DIR/db-reset.log)"; }
  ok "database migrated and empty"
else
  HOME="$SOAK_HOME" "$BIN/leoflow" db migrate > "$OUT_DIR/db-migrate.log" 2>&1 || true
  ok "database kept (--no-fresh)"
fi
# A guard, not a formality: if HOME isolation ever stops working, this catches it
# before the soak writes a weekend of evidence into the wrong database.
actual_port="$(HOME="$SOAK_HOME" python3 -c '
import sys
print(open(sys.argv[1]).read().strip())' "$SOAK_HOME/.leoflow/dev/db-port")"
[ "$actual_port" = "$PG_PORT" ] || die "db-port isolation guard failed: $actual_port != $PG_PORT"

log "materializing the workspace"
rm -rf "$WORKSPACE"; mkdir -p "$WORKSPACE"
cp -R "$SOAK_DIR/dags/." "$WORKSPACE/"
ok "$(find "$WORKSPACE" -name leoflow.yaml | wc -l | tr -d ' ') DAG projects in $WORKSPACE"

log "starting the local HTTP fixture on 127.0.0.1:${FIXTURE_PORT}"
"$BIN/soak-fixture" --addr "127.0.0.1:${FIXTURE_PORT}" > "$OUT_DIR/fixture.log" 2>&1 &
FIXTURE_PID=$!
for _ in $(seq 1 30); do curl -fsS "http://127.0.0.1:${FIXTURE_PORT}/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
curl -fsS "http://127.0.0.1:${FIXTURE_PORT}/healthz" >/dev/null 2>&1 || die "fixture did not start"
ok "fixture serving deterministic JSON on loopback only"

start_lite() {
  # HOME points at the soak home so Lite provisions its venvs there, reads no
  # developer config.yaml (hence loopback no-auth), and resolves its datastore
  # through the db-port file written above.
  HOME="$SOAK_HOME" \
  SOAK_DATA_DIR="$SOAK_DATA_DIR" \
  SOAK_ROWS="$ROWS" SOAK_FANOUT_ROWS="$FANOUT_ROWS" SOAK_LONG_SECONDS="$LONG_SECONDS" \
  PYTHONPATH="${PYTHONPATH:-$ROOT/parser}" \
    "$BIN/leoflow" lite --no-up --executor subprocess --port "$API_PORT" "$WORKSPACE" \
      >> "$OUT_DIR/lite.log" 2>&1 &
  LITE_PID=$!
  for _ in $(seq 1 900); do
    curl -fsS "$API/readyz" >/dev/null 2>&1 && return 0
    kill -0 "$LITE_PID" 2>/dev/null || return 1
    sleep 1
  done
  return 1
}

log "booting Lite (subprocess executor). First boot provisions one venv per DAG and can take minutes."
start_lite || { tail -60 "$OUT_DIR/lite.log"; die "Lite did not become ready"; }
ok "control plane ready at $API"

log "registering managed connections (both point at loopback)"
HOME="$SOAK_HOME" "$BIN/leoflow" connections set soak_http --server "$API" \
  --conn-type http --host 127.0.0.1 --port "$FIXTURE_PORT" --schema http >/dev/null 2>&1 \
  || warn "could not create soak_http; the operator leg will fail and the soak will say so"
HOME="$SOAK_HOME" "$BIN/leoflow" connections set soak_pg --server "$API" \
  --conn-type postgres --host 127.0.0.1 --port "$PG_PORT" \
  --login leoflow --password leoflow --schema soak_warehouse >/dev/null 2>&1 \
  || warn "could not create soak_pg; the operator leg will fail and the soak will say so"
ok "soak_http and soak_pg registered"

log "waiting for all six DAGs to register"
registered=0
for _ in $(seq 1 120); do
  registered="$(curl -fsS "$API/api/v2/dags?limit=100" 2>/dev/null | jq -r '[.dags[]?.dag_id] | map(select(startswith("soak_"))) | length' 2>/dev/null || echo 0)"
  [ "${registered:-0}" -ge 6 ] && break
  sleep 2
done
# Not fatal: a DAG that fails to compile is itself a finding, and the monitor's
# import_error and cadence_starved checks will say so with evidence. Blocking the
# whole battery on it would trade a reported failure for no report at all.
[ "${registered:-0}" -ge 6 ] || warn "only $registered soak DAGs registered; the import_error and cadence checks will report the gap"
ok "$registered soak DAGs registered"

# ── Fault plan. Windows are appended to faults.jsonl as they happen, so the
#    monitor sees them live and a killed harness still leaves the record. ──────
declare_window() { # kind start_epoch end_epoch note
  python3 -c '
import json,sys,datetime
k,s,e,n=sys.argv[1],float(sys.argv[2]),float(sys.argv[3]),sys.argv[4]
iso=lambda t: datetime.datetime.fromtimestamp(t, datetime.timezone.utc).isoformat().replace("+00:00","Z")
print(json.dumps({"kind":k,"start":iso(s),"end":iso(e),"note":n}))
' "$1" "$2" "$3" "$4" >> "$FAULTS_FILE"
}

control_plane_pid() {
  # `leoflow lite` supervises a leoflow-server child; the scheduler lives there,
  # so a fault aimed at the scheduler must find the child, not the supervisor.
  pgrep -P "$LITE_PID" -f leoflow-server 2>/dev/null | head -1
}

inject_fault() { # kind duration_seconds
  local kind="$1" dur="$2" start end
  start="$(date +%s)"
  case "$kind" in
    pgpause)
      declare_window "pgpause" "$start" "$((start + dur))" "docker pause of the soak Postgres"
      docker pause leoflow-soak-postgres >/dev/null 2>&1
      sleep "$dur"
      docker unpause leoflow-soak-postgres >/dev/null 2>&1
      ;;
    overrun)
      # The deliberately-red self test. A REAL fault, declared honestly short:
      # the harness claims a 45 s window and then keeps Postgres paused far
      # longer. Nothing is faked and no assertion is relaxed; the monitor simply
      # observes an outage nobody told it about and says so. This is the shape of
      # the incident the battery exists to catch: the one we thought was over.
      declare_window "overrun" "$start" "$((start + 45))" "declared 45s, actually ${dur}s"
      docker pause leoflow-soak-postgres >/dev/null 2>&1
      sleep "$dur"
      docker unpause leoflow-soak-postgres >/dev/null 2>&1
      ;;
    sigstop)
      local pid; pid="$(control_plane_pid)"
      if [ -z "$pid" ]; then warn "sigstop: no leoflow-server child found"; return; fi
      declare_window "sigstop" "$start" "$((start + dur))" "SIGSTOP of pid $pid"
      kill -STOP "$pid" 2>/dev/null
      sleep "$dur"
      kill -CONT "$pid" 2>/dev/null
      ;;
    restart)
      local pid; pid="$(control_plane_pid)"
      declare_window "restart" "$start" "$((start + 60))" "SIGKILL and restart of the control plane"
      [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null
      kill "$LITE_PID" 2>/dev/null; sleep 3; kill -9 "$LITE_PID" 2>/dev/null
      sleep 2
      start_lite || warn "control plane did not come back after restart"
      ;;
    *) warn "unknown fault kind: $kind" ;;
  esac
  end="$(date +%s)"
  log "fault $kind ran for $((end - start))s"
}

fault_plan_for() {
  case "$1" in
    none) echo "" ;;
    # Spread across the run: an outage a quarter in, a frozen scheduler past
    # halfway, a hard restart near the end. Offsets are percentages of the run.
    standard) echo "pgpause:45:25 sigstop:45:55 restart:0:80" ;;
    # 300 s of real outage behind a 45 s declared window.
    selftest-red) echo "overrun:300:30" ;;
    *) echo "$1" | tr ',' ' ' | tr ':' ':' ;;
  esac
}

run_fault_plan() {
  local plan; plan="$(fault_plan_for "$FAULTS")"
  [ -z "$plan" ] && return 0
  local t0; t0="$(date +%s)"
  for spec in $plan; do
    local kind dur pct at
    kind="${spec%%:*}"; spec="${spec#*:}"
    dur="${spec%%:*}"; pct="${spec##*:}"
    at=$(( DURATION_S * pct / 100 ))
    while [ "$(( $(date +%s) - t0 ))" -lt "$at" ]; do
      sleep 2
      kill -0 "$MONITOR_PID" 2>/dev/null || return 0
    done
    log "injecting fault: $kind (${dur}s) at +$(( $(date +%s) - t0 ))s"
    inject_fault "$kind" "$dur"
  done
}

log "starting the monitor (samples every $SAMPLE_INTERVAL, ceiling $DURATION)"
"$BIN/soak-monitor" \
  --db "$DB_URL" --api "$API" --out "$OUT_DIR" \
  --duration "$DURATION" --interval "$SAMPLE_INTERVAL" \
  --faults "$FAULTS_FILE" --data-dir "$SOAK_DATA_DIR" --label "$LABEL" \
  --max-db-bytes "$MAX_DB_BYTES" --max-data-bytes "$MAX_DATA_BYTES" --min-free-bytes "$MIN_FREE_BYTES" \
  > "$OUT_DIR/monitor.log" 2>&1 &
MONITOR_PID=$!
ok "monitor pid $MONITOR_PID, evidence in $OUT_DIR"

# The kill switch: an independent watchdog that stops everything at the ceiling
# plus five minutes of grace, so a hung monitor cannot turn an unattended run
# into an open-ended one.
( sleep "$((DURATION_S + 300))"; kill -TERM $$ 2>/dev/null ) &
WATCHDOG_PID=$!

run_fault_plan &
FAULT_PID=$!

log "soaking. tail -f $OUT_DIR/monitor.log, or read $OUT_DIR/summary.md at any time."
wait "$MONITOR_PID"
VERDICT_CODE=$?
log "monitor exited with $VERDICT_CODE"
