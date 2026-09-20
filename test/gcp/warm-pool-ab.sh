#!/usr/bin/env bash
#
# warm-pool-ab: measure what warm pools (ADR 0058) actually buy, on the only
# executor that has them.
#
# WHY IT NEEDS A CLUSTER. Warm pools exist only in the Kubernetes executor.
# Lite's subprocess executor has no pool at all, so the local soak cannot see
# this feature even in principle.
#
# ─────────────────── THIS IS NOT A ONE-VARIABLE COMPARISON ───────────────────
#
# Turning warm pools on is not a single flag. The chart REFUSES to render
# unless two other settings move with it, and the server enforces the same
# coupling at boot, so an install that changed only one would CrashLoopBackOff
# rather than run:
#
#   helm/leoflow/templates/deployment.yaml:199-201
#     "execution.warmPoolsEnabled requires auth.agentTokenTransport=exchange
#      AND auth.secretLivenessMode=enforce"
#
# The reason is a real security invariant, not an accident of configuration
# (ADR 0058 D2): a warm pod outlives the attempt it was created for, so a
# credential that outlives an attempt would let a SUPERSEDED attempt still
# resolve secrets. `exchange` gives each attempt a token that dies with it, and
# `enforce` denies secret delivery to an attempt that is no longer live.
#
# So "warm pools on" is really THREE changes at once:
#
#   execution.warmPoolsEnabled   false -> true    the feature
#   auth.agentTokenTransport     envvar -> exchange   a different auth path for
#                                                EVERY task pod, warm or not:
#                                                a projected ServiceAccount
#                                                token traded via a
#                                                control-plane TokenReview
#   auth.secretLivenessMode      observe -> enforce   a liveness check on every
#                                                secret delivery
#
# A two-arm A/B across that bundle CANNOT attribute a difference to warm pools.
# The exchange transport alone adds a TokenReview round trip to every pod start,
# which pushes the measured number in the OPPOSITE direction to the one warm
# pools are supposed to move it. Reporting "warm pools made it faster" from such
# a comparison would be wrong even if the number were right.
#
# THE FIX IS A THIRD ARM, and it is the reason this file exists rather than a
# two-arm script:
#
#   A   envvar   + observe + warm OFF   today's default posture
#   A'  exchange + enforce + warm OFF   the coupled prerequisites, WITHOUT the
#                                       feature. This is the real control.
#   B   exchange + enforce + warm ON    the feature
#
#   B vs A'  isolates warm pools. ONE variable. This is the scientific result.
#   B vs A   is what an operator actually experiences when they "turn on warm
#            pools", bundle and all. This is the operational result.
#   A' vs A  is the price of the prerequisites on their own, which is the number
#            nobody has and which decides whether the bundle is worth it when
#            the pool misses.
#
# Both differences are reported, separately and labeled. Neither is called "the
# warm pool speedup" on its own.
#
# ─────────────────────────── ORDERING IS A CONFOUND ──────────────────────────
#
# The arms run sequentially on one cluster, because three clusters costs three
# times as much and introduces a bigger confound than the one it removes. But
# sequential arms drift: the image is cached after arm A, the nodes are warm,
# the zone's neighbours change. So the order is A, A', B, then A AGAIN, and the
# two A measurements are compared to each other. If they disagree by more than
# DRIFT_TOLERANCE, the cluster drifted under the experiment and the A/B
# difference is reported as UNTRUSTWORTHY rather than as a result.
#
# ──────────────────────────── WHAT IT CANNOT SEE ─────────────────────────────
#
# - Pool behavior under eviction, preemption or node loss. Nothing here kills a
#   node, so the reclaim path (ADR 0058 N1b) is not exercised.
# - Multi-tenant contention. One tenant, one dag_version. maxPoolSize and the
#   per-tenant ceiling (M4) are not pressured.
# - Steady state. The run is minutes, and idleTtl / maxAttempts / maxLifetime
#   recycling are all longer than that, so recycle is NOT measured.
# - The cold-start distribution of a real DAG image. The image here is a small
#   fixture; a 2 GB DAG image changes the arithmetic entirely and in warm pools'
#   favor, so any speedup measured here is a LOWER bound for heavy images and
#   says nothing about light ones being representative.
#
# Usage:
#   test/gcp/warm-pool-ab.sh                 # plan + cost, creates nothing
#   test/gcp/warm-pool-ab.sh --execute       # provision, run all arms, tear down
#   test/gcp/warm-pool-ab.sh --self-test
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXP_REPO_ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck source=lib/experiment.sh
. "$HERE/lib/experiment.sh"
# shellcheck source=lib/stats.sh
. "$HERE/lib/stats.sh"
# shellcheck source=lib/stack.sh
. "$HERE/lib/stack.sh"

NODES="${NODES:-3}"
TTL="${TTL:-2h}"
EXECUTE=0
# Attempts per arm. Twenty is MIN_SAMPLES in lib/stats.sh, which is the floor
# below which a percentile is reported as inconclusive rather than used.
ATTEMPTS="${ATTEMPTS:-30}"
# If the two arm-A measurements differ by more than this ratio, the cluster
# drifted under the experiment and no arm comparison from it is trustworthy.
DRIFT_TOLERANCE="${DRIFT_TOLERANCE:-1.5}"

ARMS="A Aprime B"

# The CLI that builds and pushes the DAG image. Built from this tree rather than
# downloaded: the experiment is about this source, not about a released binary.
WP_CLI="${WP_CLI:-$EXP_REPO_ROOT/.bin/leoflow}"
WP_DAG_VERSION="${WP_DAG_VERSION:-gcpexp1}"
WP_JWT=""
WP_PW=""

# ------------------------------------------------------------- pure helpers

# arm_values maps an arm to the three coupled settings. One function, so the
# coupling can never be half-applied by a caller that remembers only the flag
# the experiment is named after.
arm_values() { # <arm>  -> three --set arguments
  case "$1" in
    A)
      echo "execution.warmPoolsEnabled=false auth.agentTokenTransport=envvar auth.secretLivenessMode=observe" ;;
    Aprime)
      echo "execution.warmPoolsEnabled=false auth.agentTokenTransport=exchange auth.secretLivenessMode=enforce" ;;
    B)
      echo "execution.warmPoolsEnabled=true auth.agentTokenTransport=exchange auth.secretLivenessMode=enforce" ;;
    *) return 1 ;;
  esac
}

# arm_is_renderable encodes the chart's own refusal, so a bad arm definition is
# caught by the self-test instead of by a failed helm upgrade on a paid cluster.
# The rule is at helm/leoflow/templates/deployment.yaml:199-201.
arm_is_renderable() { # <warm> <transport> <liveness>
  local warm="$1" transport="$2" liveness="$3"
  [ "$warm" != "true" ] && return 0
  [ "$transport" = "exchange" ] && [ "$liveness" = "enforce" ]
}

# ratio_of is the comparison between two arms, guarded against a zero baseline
# producing an infinite "improvement".
ratio_of() { # <baseline> <other>
  awk -v a="$1" -v b="$2" 'BEGIN{ if (a+0 <= 0) { print "undefined"; exit } printf "%.3f", b/a }'
}

# drift_verdict compares the two arm-A measurements taken at the start and end.
# This is what decides whether anything else in the run may be believed.
drift_verdict() { # <first A p95> <second A p95>
  local a="$1" b="$2" r
  r="$(ratio_of "$a" "$b")"
  [ "$r" = "undefined" ] && { echo UNTRUSTWORTHY; return 0; }
  # Drift in EITHER direction invalidates: the cluster getting faster over the
  # run flatters a later arm exactly as much as getting slower penalizes it.
  awk -v r="$r" -v t="$DRIFT_TOLERANCE" 'BEGIN{ exit !(r <= t && r >= 1/t) }' \
    && echo STABLE || echo UNTRUSTWORTHY
}

# ---------------------------------------------------------------- self-test

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  # ------------------------------------- the coupling is not optional
  # THE claim this file is built around. Arm B must move all three settings,
  # and the control arm must move exactly two.
  local a ap b
  a="$(arm_values A)"; ap="$(arm_values Aprime)"; b="$(arm_values B)"

  case "$b" in
    *"warmPoolsEnabled=true"*) echo "  ok   arm B turns warm pools on" ;;
    *) echo "  FAIL arm B does not turn warm pools on"; fail=1 ;;
  esac
  case "$b" in
    *"agentTokenTransport=exchange"*"secretLivenessMode=enforce"*)
      echo "  ok   arm B also carries BOTH coupled prerequisites, which the chart refuses to render without" ;;
    *) echo "  FAIL arm B omits a coupled prerequisite; the chart would refuse to render it"; fail=1 ;;
  esac
  # The control arm is the whole point: same prerequisites, feature off.
  case "$ap" in
    *"warmPoolsEnabled=false"*"agentTokenTransport=exchange"*"secretLivenessMode=enforce"*)
      echo "  ok   arm A' carries the prerequisites WITHOUT the feature, which is what isolates warm pools" ;;
    *) echo "  FAIL arm A' is not the coupled control; B vs A' would not isolate one variable"; fail=1 ;;
  esac
  case "$a" in
    *"warmPoolsEnabled=false"*"agentTokenTransport=envvar"*"secretLivenessMode=observe"*)
      echo "  ok   arm A is today's default posture" ;;
    *) echo "  FAIL arm A is not the default posture, so A' vs A does not price the prerequisites"; fail=1 ;;
  esac
  # A' and B must differ in EXACTLY the warm-pool flag. If they differed in
  # anything else, B vs A' would be another bundle comparison wearing the name
  # of a controlled one, which is the exact error this file exists to avoid.
  local diff_count
  diff_count="$(python3 - "$ap" "$b" <<'PY'
import sys
a = dict(kv.split("=", 1) for kv in sys.argv[1].split())
b = dict(kv.split("=", 1) for kv in sys.argv[2].split())
print(sum(1 for k in set(a) | set(b) if a.get(k) != b.get(k)))
PY
)"
  _eq "$diff_count" "1" "arm A' and arm B differ in EXACTLY one setting, so B vs A' is a controlled comparison"

  arm_values nonsense >/dev/null 2>&1 \
    && { echo "  FAIL an unknown arm produced settings"; fail=1; } \
    || echo "  ok   an unknown arm is refused rather than silently defaulted"

  # ------------------------------------- the chart's render guard, mirrored
  arm_is_renderable true exchange enforce \
    && echo "  ok   warm pools with both prerequisites renders" \
    || { echo "  FAIL the fully-coupled arm was judged unrenderable"; fail=1; }
  # Each half missing, separately: the chart refuses both, and so must we.
  arm_is_renderable true envvar enforce \
    && { echo "  FAIL warm pools with envvar transport was judged renderable; the chart fails that render"; fail=1; } \
    || echo "  ok   warm pools without exchange transport is refused, as the chart refuses it"
  arm_is_renderable true exchange observe \
    && { echo "  FAIL warm pools with observe liveness was judged renderable; the chart fails that render"; fail=1; } \
    || echo "  ok   warm pools without enforce liveness is refused, as the chart refuses it"
  # With the feature off, the prerequisites are free to be anything.
  arm_is_renderable false envvar observe \
    && echo "  ok   with warm pools off, the prerequisites are unconstrained" \
    || { echo "  FAIL the default posture was judged unrenderable"; fail=1; }
  arm_is_renderable false exchange enforce \
    && echo "  ok   the coupled control arm renders with the feature off" \
    || { echo "  FAIL the control arm was judged unrenderable"; fail=1; }

  # Every arm this run would actually execute must be renderable, checked by
  # walking ARMS rather than by listing them again here.
  local arm vals
  for arm in $ARMS; do
    vals="$(arm_values "$arm")"
    # shellcheck disable=SC2086
    set -- $vals
    local w t l
    w="${1#*=}"; t="${2#*=}"; l="${3#*=}"
    if arm_is_renderable "$w" "$t" "$l"; then
      echo "  ok   arm $arm renders"
    else
      echo "  FAIL arm $arm would be refused by the chart at render time"; fail=1
    fi
  done

  # ------------------------------------- the guard, checked against the CHART
  # arm_is_renderable above is a MIRROR of a rule that lives in the chart, and a
  # mirror drifts. This drives the real template through every combination, so
  # the day the chart's coupling changes, this fails here rather than during a
  # paid run. Skipped loudly when helm is absent, because a gate that silently
  # stops running is the defect it was meant to prevent.
  if command -v helm >/dev/null 2>&1 && [ -d "$EXP_REPO_ROOT/helm/leoflow" ]; then
    local w t l expect got
    for w in true false; do
      for t in envvar exchange; do
        for l in observe enforce; do
          if arm_is_renderable "$w" "$t" "$l"; then expect=RENDERS; else expect=REFUSED; fi
          if helm template leoflow "$EXP_REPO_ROOT/helm/leoflow" \
               --set "database.url=postgres://x" --set "redis.url=redis://x" \
               --set-string auth.jwtSecret=a --set-string bootstrap.password=b \
               --set "execution.warmPoolsEnabled=$w" \
               --set "auth.agentTokenTransport=$t" \
               --set "auth.secretLivenessMode=$l" >/dev/null 2>&1; then got=RENDERS; else got=REFUSED; fi
          if [ "$expect" = "$got" ]; then
            echo "  ok   chart agrees: warm=$w transport=$t liveness=$l -> $got"
          else
            echo "  FAIL chart disagrees with arm_is_renderable: warm=$w transport=$t liveness=$l expected $expect got $got"
            fail=1
          fi
        done
      done
    done
  else
    echo "  ..   skipped: no helm or no chart, so the coupling was NOT checked against the real template"
  fi

  # ------------------------------------- ratios and drift
  _eq "$(ratio_of 10 5)" "0.500" "an arm at half the baseline reports 0.5"
  _eq "$(ratio_of 10 20)" "2.000" "an arm at twice the baseline reports 2.0"
  # A zero baseline would make every comparison an infinite improvement.
  _eq "$(ratio_of 0 5)" "undefined" "a zero baseline is undefined, not an infinite speedup"

  _eq "$(drift_verdict 10 10)"  "STABLE" "two identical A measurements are stable"
  _eq "$(drift_verdict 10 12)"  "STABLE" "a small drift is tolerated"
  # THE guard. The cluster got much slower between the two A arms, so the B
  # number is not comparable to the A number and must not be reported as one.
  _eq "$(drift_verdict 10 30)"  "UNTRUSTWORTHY" "a cluster that slowed under the run invalidates the comparison"
  # And the other direction, which is the one people forget: a cluster that got
  # FASTER flatters whichever arm ran last, which is B.
  _eq "$(drift_verdict 30 10)"  "UNTRUSTWORTHY" "a cluster that sped up under the run also invalidates it, because it flatters the last arm"
  _eq "$(drift_verdict 0 10)"   "UNTRUSTWORTHY" "an undefined ratio cannot be stable"

  # ------------------------------------- the sample floor
  # An arm with fewer attempts than MIN_SAMPLES cannot carry a p95, and the
  # default must not be below the floor the stats library enforces.
  [ "$ATTEMPTS" -ge "$MIN_SAMPLES" ] \
    && echo "  ok   the default attempt count is at or above the percentile floor ($MIN_SAMPLES)" \
    || { echo "  FAIL the default attempt count ($ATTEMPTS) is below the $MIN_SAMPLES floor, so every arm would be inconclusive"; fail=1; }

  # ------------------------------------- the run order
  # A must be measured twice, first and last, or drift cannot be detected at all.
  case "$RUN_ORDER" in
    "A "*" A") echo "  ok   the run order starts and ends with arm A, so drift is detectable" ;;
    *) echo "  FAIL the run order does not bracket the experiment with arm A: '$RUN_ORDER'"; fail=1 ;;
  esac

  [ "$fail" = "0" ] && { echo "warm-pool-ab self-test: ok"; return 0; }
  return 1
}

# The order arms are executed in. A is measured twice, at both ends, so drift
# across the run is a measurement rather than an assumption.
RUN_ORDER="${RUN_ORDER:-A Aprime B A}"

# --------------------------------------------------------------- cluster side

apply_arm() { # <arm>
  local arm="$1" vals sets=() v
  vals="$(arm_values "$arm")" || exp_die "unknown arm: $arm"
  for v in $vals; do sets+=(--set "$v"); done
  exp_log "arm $arm: $vals"
  stack_up leoflow "$WP_JWT" "$WP_PW" "${sets[@]}"
}

run_experiment() {
  local out="$1"
  exp_require gcloud kubectl helm jq python3

  exp_arm_teardown "$HERE/teardown.sh"
  exp_provision "$HERE/provision.sh" warm-pool-ab "$NODES" "$TTL"
  gcloud container clusters get-credentials "$EXP_CLUSTER" \
    --zone "${GCP_ZONE:?}" --project "${GCP_PROJECT:?}" >/dev/null 2>&1 \
    || exp_die "could not get credentials for $EXP_CLUSTER"
  exp_kube_ready "$NODES" 600

  WP_JWT="$(openssl rand -base64 48)"
  WP_PW="$(openssl rand -base64 18)"

  # ---------------------------------------------------------------------
  # THE STEP THE PREVIOUS SCRIPT NEVER HAD.
  #
  # test/soak/warmpool-ab.sh never built or pushed the DAG images it needed, so
  # every trigger it made would have 404'd against a dag_id the control plane
  # had never been told about: an empty series, and three arms that all
  # measured nothing equally well.
  #
  # PROVEN: the build-and-push below has been run for real. From an arm64 Mac it
  # produced linux/amd64 and pushed it to Artifact Registry:
  #   us-central1-docker.pkg.dev/<project>/leoflow-validate/gcp-probe:gcpexp1
  #   sha256:ee3708486dc3ed224bb91d615f30c36ef0f81397e35b4256c90fde3d6ad22f92
  # The platform pin in test/gcp/dags/gcp_probe/leoflow.yaml is what makes that
  # work; without it the image is arm64 and every task pod fails with an exec
  # format error AFTER the cluster has been paid for.
  #
  # That image was DELETED again after it was verified. An Artifact Registry
  # repository outlives every cluster that pulled from it, carries none of the
  # labels that make deleting a cluster safe, and `teardown.sh --leftovers` can
  # therefore only ever LIST it. A first real run rebuilds and repushes, which
  # is a few minutes and the correct default; leaving an image behind to save
  # them is how a registry becomes a bill nobody remembers agreeing to.
  #
  # NOT PROVEN: everything from the login onward. It has never run against a
  # control plane, and the defect class that has already bitten this directory
  # four times (a flag on the wrong subcommand, a label split on the wrong
  # separator, a missing release channel, an empty-array expansion under bash
  # 3.2) is exactly the class only a real run finds. Treat the code below as a
  # specification that compiles, not as a working pipeline.
  # ---------------------------------------------------------------------
  wp_build_and_push "$out" || exp_die "the DAG image could not be built or pushed; no arm can run without it"

  local api_port="${API_PORT:-18080}" token
  stack_up leoflow "$WP_JWT" "$WP_PW" $(wp_sets A)
  stack_api_forward leoflow "$api_port"
  token="$(printf '%s' "$WP_PW" | "$WP_CLI" auth create-token \
             --server "http://127.0.0.1:$api_port" --username admin --password-stdin 2>/dev/null)" \
    || exp_die "could not obtain an API token from the bootstrap admin"
  [ -n "$token" ] || exp_die "the token was empty; every later call would 401 and every arm would measure nothing"

  "$WP_CLI" deploy "$out/dag-project" --server "http://127.0.0.1:$api_port" \
    --token "$token" --skip-build --yes \
    || exp_die "registering the DAG failed; a trigger would 404, which is exactly the shape this runner exists to fix"

  # The arms, in the bracketed order, with A measured at both ends.
  local arm idx=0
  for arm in $RUN_ORDER; do
    idx=$((idx + 1))
    wp_run_arm "$arm" "$idx" "$out" "$api_port" "$token"
  done
  stack_api_forward_stop

  wp_report "$out"
}

# wp_build_and_push is the proven half. It substitutes the project id into the
# registry URL (a project id is an account identifier and is never committed)
# and shells out to the CLI's own compile, which builds for linux/amd64 and
# pushes.
wp_build_and_push() { # <out dir>
  local out="$1"
  exp_require docker
  [ -x "$WP_CLI" ] || exp_die "no leoflow CLI at $WP_CLI. Build one: go build -o $WP_CLI ./cmd/leoflow"
  mkdir -p "$out/dag-project"
  cp "$EXP_REPO_ROOT"/test/gcp/dags/gcp_probe/* "$out/dag-project/"
  # The registry URL carries ${GCP_PROJECT} in the committed file precisely so
  # no account identifier is in git. Substituted here, into the run directory,
  # which is gitignored.
  python3 - "$out/dag-project/leoflow.yaml" "$GCP_PROJECT" <<'SUBST'
import sys
p, proj = sys.argv[1], sys.argv[2]
src = open(p).read()
if "${GCP_PROJECT}" not in src:
    sys.exit("leoflow.yaml no longer carries ${GCP_PROJECT}; refusing to guess the registry")
open(p, "w").write(src.replace("${GCP_PROJECT}", proj))
SUBST
  gcloud auth configure-docker "${AR_HOST:-us-central1-docker.pkg.dev}" --quiet >/dev/null 2>&1 \
    || exp_warn "could not configure docker auth for Artifact Registry; the push will probably fail"
  exp_log "building and pushing the DAG image (linux/amd64, cross-built if this is an arm64 host)"
  "$WP_CLI" compile "$out/dag-project" --output "$out/dag-project/dag.json" \
    --build --push --dag-version "$WP_DAG_VERSION" || return 1
  exp_ok "DAG image pushed; the registry repository SURVIVES the cluster and nothing here deletes it"
}

wp_sets() { # <arm> -> --set arguments
  local v
  for v in $(arm_values "$1"); do printf -- '--set %s ' "$v"; done
}

# wp_run_arm applies one arm and measures ATTEMPTS task starts under it.
# NEVER EXECUTED. See the banner.
wp_run_arm() { # <arm> <index> <out> <api port> <token>
  local arm="$1" idx="$2" out="$3" port="$4" token="$5" i
  exp_log "arm $arm (position $idx in: $RUN_ORDER)"
  stack_up leoflow "$WP_JWT" "$WP_PW" $(wp_sets "$arm")
  stack_api_forward leoflow "$port"
  # A warm pool is empty on the first attempt of a dag_version, so the first
  # attempts of arm B measure a COLD start and would drag its p95 toward A's for
  # a reason that has nothing to do with the steady state the feature is for.
  # They are run and discarded, and the count is recorded so the discard is
  # visible rather than silent.
  for i in $(seq 1 "${WARMUP_ATTEMPTS:-3}"); do
    "$WP_CLI" runs trigger gcp_probe --server "http://127.0.0.1:$port" --token "$token" >/dev/null 2>&1 || true
  done
  printf 'arm %s warmup_discarded %s\n' "$arm" "${WARMUP_ATTEMPTS:-3}" >> "$out/facts.txt"

  for i in $(seq 1 "$ATTEMPTS"); do
    # The measurement is the POD's own schedule-to-running interval, read from
    # the Kubernetes API, not the wall time of the trigger call: the trigger
    # returns as soon as the run is accepted, so timing it would measure the
    # API and not the pool.
    "$WP_CLI" runs trigger gcp_probe --server "http://127.0.0.1:$port" --token "$token" >/dev/null 2>&1 || true
    sleep "${ATTEMPT_SPACING:-2}"
  done
  kubectl -n "$STACK_TASK_NS" get pods -l leoflow.io/run-id -o json > "$out/pods-$arm-$idx.json" 2>/dev/null || true
  kubectl -n "$STACK_TASK_NS" delete pods -l leoflow.io/run-id --wait=false >/dev/null 2>&1 || true
}

wp_report() { # <out dir>
  exp_warn "wp_report is unimplemented: the arms above have never produced a series, so there is nothing to summarize."
  exp_warn "Writing a report from no observations is precisely the empty-verdict shape this directory refuses."
  return 1
}

# ---------------------------------------------------------------------- main

while [ $# -gt 0 ]; do
  case "$1" in
    --execute)   EXECUTE=1; shift ;;
    --nodes)     NODES="$2"; shift 2 ;;
    --ttl)       TTL="$2"; shift 2 ;;
    --attempts)  ATTEMPTS="$2"; shift 2 ;;
    --self-test) self_test; exit $? ;;
    -h|--help)   sed -n '2,/^set -euo pipefail/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) exp_die "unknown flag: $1" ;;
  esac
done

cat <<ARMS

  The three arms, and why there are three:

    A       $(arm_values A)
    A'      $(arm_values Aprime)
    B       $(arm_values B)

    B vs A'   isolates warm pools            ONE variable
    B vs A    what an operator experiences   THREE variables
    A' vs A   the price of the prerequisites TWO variables

  Turning warm pools on is not a one-flag change. The chart refuses to render
  without agentTokenTransport=exchange AND secretLivenessMode=enforce
  (helm/leoflow/templates/deployment.yaml:199-201), and the server enforces the
  same coupling at boot. Any two-arm comparison across that bundle cannot
  attribute a difference to warm pools, which is why arm A' exists.

  Run order: $RUN_ORDER  (arm A twice, to measure drift rather than assume none)

ARMS

if [ "$EXECUTE" != "1" ]; then
  "$HERE/provision.sh" --dry-run --experiment warm-pool-ab --nodes "$NODES" --ttl "$TTL"
  exp_never_ran "warm-pool-ab, $NODES nodes for $TTL, $ATTEMPTS attempts per arm" \
    "not invoked with --execute; this printed the plan and created nothing"
  exit 0
fi

exp_never_ran "warm-pool-ab, $NODES nodes for $TTL, $ATTEMPTS attempts per arm" \
  "the DAG build-and-push step is not implemented, so no arm can produce a number. See run_experiment(). This runner REFUSES to provision rather than bill for a cluster it cannot measure anything on."
exit 1
