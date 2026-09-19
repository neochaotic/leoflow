#!/usr/bin/env bash
#
# Provision a throwaway GKE cluster for the three experiments a local k3d cannot
# run, and make it impossible to forget.
#
# The cost of these experiments is not the experiment. A four-hour window is
# roughly a dollar; a cluster nobody deleted is roughly a hundred to two hundred
# a month. That is two orders of magnitude, so every guardrail here is aimed at
# the second number, not the first.
#
# THE GUARDRAIL THAT MATTERS is --max-run-duration on the node pool: the nodes
# carry their own expiry and Google enforces it. If this script dies, if the
# laptop sleeps, if the teardown never runs, the expensive part still goes away.
# Everything else below is defence in depth on top of that one property.
#
# Nothing about the account is in this file. Project, zone and billing come from
# the environment or from `gcloud config`, and the script refuses to guess.
#
# Usage:
#   test/gcp/provision.sh --experiment pod-per-task [--nodes 3] [--ttl 4h]
#   test/gcp/provision.sh --experiment warm-pool-ab
#   test/gcp/provision.sh --experiment netpol          # needs Dataplane V2
#   test/gcp/provision.sh --dry-run --experiment pod-per-task   # print the plan + cost
#   test/gcp/provision.sh --self-test
set -euo pipefail

EXPERIMENT=""
NODES=3
MACHINE="e2-standard-4"
TTL="4h"
DRY_RUN=0
SPOT=0
NAME_PREFIX="leoflow-exp"

# List prices, us-central1, on-demand, USD/hour. They are ESTIMATES used to
# print a number before anything is created, not a contract: confirm against the
# pricing calculator for the region you actually use. They are here so the
# script can refuse a plan that is obviously wrong, and so the operator sees a
# figure before approving rather than after being billed.
# A case rather than an associative array: macOS ships bash 3.2, which has none,
# and a script that only runs on the maintainer's other shell is a script that
# does not run.
machine_hourly() {
  case "$1" in
    e2-standard-2) echo "0.067" ;;
    e2-standard-4) echo "0.134" ;;
    e2-standard-8) echo "0.268" ;;
    *) return 1 ;;
  esac
}
GKE_MGMT_HOURLY="0.10"
# Spot is typically 60-91% off. The low end is used so an estimate errs high.
SPOT_MULTIPLIER="0.40"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m    !!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 2; }

usage() { sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; }

# ---------------------------------------------------------------- pure helpers

# hours_of turns 90m / 4h / 2d into a decimal number of hours. The TTL is both
# printed in the estimate and handed to --max-run-duration, so one parser keeps
# the number the operator approved and the number Google enforces identical.
hours_of() {
  case "$1" in
    *h) awk -v v="${1%h}" 'BEGIN{printf "%.4f", v}' ;;
    *m) awk -v v="${1%m}" 'BEGIN{printf "%.4f", v/60}' ;;
    *s) awk -v v="${1%s}" 'BEGIN{printf "%.4f", v/3600}' ;;
    *d) awk -v v="${1%d}" 'BEGIN{printf "%.4f", v*24}' ;;
    *)  return 1 ;;
  esac
}

seconds_of() {
  local h; h="$(hours_of "$1")" || return 1
  awk -v h="$h" 'BEGIN{printf "%d", h*3600}'
}

# estimate_usd is deliberately a function of the inputs alone, so --dry-run and
# the real run cannot print different numbers.
estimate_usd() { # <nodes> <machine> <ttl> <spot 0|1>
  local nodes=$1 machine=$2 ttl=$3 spot=$4 rate hours mult
  rate="$(machine_hourly "$machine")" || return 1
  hours="$(hours_of "$ttl")" || return 1
  mult=1
  [ "$spot" = "1" ] && mult="$SPOT_MULTIPLIER"
  awk -v n="$nodes" -v r="$rate" -v h="$hours" -v m="$mult" -v mgmt="$GKE_MGMT_HOURLY" \
    'BEGIN{printf "%.2f", (n*r*m + mgmt) * h}'
}

# forgotten_usd is the number that actually matters: what a cluster costs if the
# teardown never happens. It is printed next to the experiment cost on purpose,
# because the ratio between them is the whole argument for the TTL.
forgotten_usd() { # <nodes> <machine> <spot 0|1>
  estimate_usd "$1" "$2" "720h" "$3"
}

# ---------------------------------------------------------------- args

while [ $# -gt 0 ]; do
  case "$1" in
    --experiment) EXPERIMENT="$2"; shift 2 ;;
    --nodes)      NODES="$2"; shift 2 ;;
    --machine)    MACHINE="$2"; shift 2 ;;
    --ttl)        TTL="$2"; shift 2 ;;
    --spot)       SPOT=1; shift ;;
    --dry-run)    DRY_RUN=1; shift ;;
    --self-test)  self_test_requested=1; shift ;;
    -h|--help)    usage; exit 0 ;;
    *) die "unknown flag: $1" ;;
  esac
done

self_test() {
  local fail=0
  local _eq; _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  _eq "$(hours_of 4h)"   "4.0000"  "4h parses"
  _eq "$(hours_of 90m)"  "1.5000"  "90m parses"
  _eq "$(hours_of 2d)"   "48.0000" "2d parses"
  _eq "$(seconds_of 4h)" "14400"   "4h in seconds, which is what Google is given"
  hours_of 4 >/dev/null 2>&1 && { echo "  FAIL a unitless TTL is accepted"; fail=1; } || echo "  ok   a unitless TTL is refused rather than guessed"

  # 3 x 0.134 + 0.10 = 0.502/h, over 4h = 2.008
  _eq "$(estimate_usd 3 e2-standard-4 4h 0)" "2.01" "the estimate is the arithmetic, not a vibe"
  _eq "$(estimate_usd 0 e2-standard-4 4h 0)" "0.40" "zero nodes still bills the control plane"
  estimate_usd 3 e2-nonexistent 4h 0 >/dev/null 2>&1 && { echo "  FAIL an unknown machine type is priced"; fail=1; } || echo "  ok   an unknown machine type refuses to be priced"

  # The ratio the TTL exists for: a month of the same cluster.
  four_h="$(estimate_usd 3 e2-standard-4 4h 0)"
  month="$(forgotten_usd 3 e2-standard-4 0)"
  awk -v a="$four_h" -v b="$month" 'BEGIN{exit !(b > a*100)}' \
    && echo "  ok   a forgotten cluster is 100x the experiment, which is why the TTL is not optional" \
    || { echo "  FAIL the forgotten-cluster figure is not dominating; the estimate is wrong"; fail=1; }

  _eq "$(estimate_usd 3 e2-standard-4 4h 1)" "1.04" "spot discounts the nodes and not the control plane, which is most of a short window"

  [ "$fail" = "0" ] && { echo "provision self-test: ok"; return 0; }
  return 1
}

[ "${self_test_requested:-0}" = "1" ] && { self_test; exit $?; }

# ---------------------------------------------------------------- validation

case "$EXPERIMENT" in
  pod-per-task|warm-pool-ab|netpol) ;;
  "") die "--experiment is required (pod-per-task | warm-pool-ab | netpol)" ;;
  *)  die "unknown experiment: $EXPERIMENT" ;;
esac

# A ceiling on the ceiling. The maintainer authorized up to ten nodes; this
# refuses eleven rather than trusting a typo, because the failure mode of a
# fat-fingered --nodes is a bill, not an error.
[ "$NODES" -ge 1 ] 2>/dev/null || die "--nodes must be a positive integer"
[ "$NODES" -le 10 ] || die "--nodes $NODES exceeds the authorized ceiling of 10. Raising it is a conversation, not a flag."

TTL_SECONDS="$(seconds_of "$TTL")" || die "--ttl must carry a unit (90m, 4h, 2d), so it cannot be misread"
[ "$TTL_SECONDS" -le 86400 ] || die "--ttl $TTL exceeds 24h. These are bounded experiments; a longer one is a decision to take deliberately."
machine_hourly "$MACHINE" >/dev/null 2>&1 || die "no price known for $MACHINE, so the estimate would be a guess. Add it to machine_hourly() or pick a priced type."

PROJECT="${GCP_PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}"
ZONE="${GCP_ZONE:-$(gcloud config get-value compute/zone 2>/dev/null || true)}"
[ -n "$PROJECT" ] && [ "$PROJECT" != "(unset)" ] || die "set GCP_PROJECT or 'gcloud config set project'. This script never guesses which account it is spending."
[ -n "$ZONE" ] && [ "$ZONE" != "(unset)" ] || die "set GCP_ZONE or 'gcloud config set compute/zone'. Zonal on purpose: a regional control plane multiplies the nodes across zones."

CLUSTER="${NAME_PREFIX}-${EXPERIMENT}-$(date +%m%d%H%M)"
COST="$(estimate_usd "$NODES" "$MACHINE" "$TTL" "$SPOT")"
FORGOTTEN="$(forgotten_usd "$NODES" "$MACHINE" "$SPOT")"

cat <<PLAN

  experiment     $EXPERIMENT
  cluster        $CLUSTER  (zonal, $ZONE)
  nodes          $NODES x $MACHINE$([ "$SPOT" = "1" ] && echo "  (spot)")
  node TTL       $TTL, enforced by Google via --max-run-duration
  autoscaling    off; a fixed pool cannot grow into a bill

  estimated cost of this window     ~USD $COST
  the same cluster left for a month ~USD $FORGOTTEN

  Prices are us-central1 list estimates, not a quote. The second number is why
  the node TTL exists: if every other safeguard fails, the nodes still expire.

PLAN

if [ "$SPOT" = "1" ]; then
  warn "Spot nodes can be preempted, and a preemption looks exactly like the infra failure these experiments measure."
  warn "For a resilience run that is a confound, not a saving. Drop --spot unless you are only smoke-testing the scripts."
fi

[ "$DRY_RUN" = "1" ] && { log "dry run: nothing created"; exit 0; }

# ---------------------------------------------------------------- create

DATAPLANE=()
if [ "$EXPERIMENT" = "netpol" ]; then
  # #1089's rows are unprovable on a CNI that accepts NetworkPolicy and ignores
  # it, which is exactly what k3d does. Dataplane V2 is the whole reason this
  # experiment needs a cloud cluster at all.
  DATAPLANE=(--enable-dataplane-v2)
  log "netpol: Dataplane V2 on, because a CNI that does not enforce makes the assertion pass vacuously"
fi

SPOT_FLAG=()
[ "$SPOT" = "1" ] && SPOT_FLAG=(--spot)

log "creating $CLUSTER in $ZONE (project $PROJECT)"
gcloud container clusters create "$CLUSTER" \
  --project "$PROJECT" --zone "$ZONE" \
  --num-nodes "$NODES" --machine-type "$MACHINE" \
  --max-run-duration "${TTL_SECONDS}s" \
  --no-enable-autoupgrade --no-enable-autorepair \
  --labels "purpose=leoflow-experiment,experiment=$EXPERIMENT,expires-after=$TTL" \
  "${SPOT_FLAG[@]}" "${DATAPLANE[@]}" \
  || die "cluster create failed; nothing to tear down"

ok "cluster up, nodes expire after $TTL whatever happens to this script"
printf '%s\n' "$CLUSTER" > .gcp-experiment-cluster
cat <<NEXT

  Delete it the moment you are done, do not wait for the TTL:

      test/gcp/teardown.sh $CLUSTER

  The node TTL removes the expensive part. The control plane keeps billing at
  about USD $GKE_MGMT_HOURLY an hour until the CLUSTER is deleted, so the TTL is
  the safety net and the teardown is still the job.

NEXT
