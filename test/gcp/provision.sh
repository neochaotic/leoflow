#!/usr/bin/env bash
#
# Provision a throwaway GKE cluster for the three experiments a local k3d cannot
# run, and make it impossible to forget.
#
# The cost of these experiments is not the experiment. A four-hour window at the
# default shape is about USD 2; the same cluster left up for a month is about
# USD 360, and ten nodes left for a month is about USD 1,040. Both figures come
# from estimate_usd below, not from prose, so they cannot drift apart from what
# the script prints. That is two orders of magnitude, so every guardrail here is
# aimed at the second number, not the first.
#
# THE GUARDRAIL THAT MATTERS is --max-run-duration on the NODE POOL. It is a
# node-pool flag: `gcloud container clusters create` does not accept it, so the
# TTL is applied by a second call after the cluster exists, and read back before
# the script reports success. If it cannot be applied, the cluster is deleted
# immediately rather than left unbounded.
#
# What the API promises is that the nodes stop existing: NodeConfig.maxRunDuration
# is documented as "the maximum duration for the nodes to exist. If unspecified,
# the nodes can exist indefinitely." Whether GKE then replaces an expired node to
# hold the pool at its target size has NOT been verified on a live cluster, so
# treat the TTL as the backstop it is and not as a substitute for the teardown.
# Everything else below is defense in depth on top of it.
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

# The header up to `set -euo pipefail`, rather than a hard-coded line range: a
# range silently stops printing the Usage block the first time the header grows,
# and -h is exactly the moment nobody is watching for that. The self-test checks
# the result still contains the usage lines.
usage() { sed -n '2,/^set -euo pipefail/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

# ---------------------------------------------------------------- pure helpers

# hours_of turns 90m / 4h / 2d into a decimal number of hours. The TTL is both
# printed in the estimate and handed to --max-run-duration, so one parser keeps
# the number the operator approved and the number Google enforces identical.
hours_of() {
  local num unit
  case "$1" in
    *h) num="${1%h}"; unit=h ;;
    *m) num="${1%m}"; unit=m ;;
    *s) num="${1%s}"; unit=s ;;
    *d) num="${1%d}"; unit=d ;;
    *)  return 1 ;;
  esac
  # A positive decimal and nothing else. awk reads "abch" as 0 and "-2h" as a
  # negative, and both used to sail through: only the UPPER bound was checked, so
  # a mistyped TTL became a zero or negative --max-run-duration handed to Google
  # rather than a refusal here. An unreadable duration is not a duration.
  case "$num" in
    ''|*[!0-9.]*) return 1 ;;
  esac
  awk -v v="$num" 'BEGIN{ exit !(v + 0 > 0) }' || return 1
  case "$unit" in
    h) awk -v v="$num" 'BEGIN{printf "%.4f", v}' ;;
    m) awk -v v="$num" 'BEGIN{printf "%.4f", v/60}' ;;
    s) awk -v v="$num" 'BEGIN{printf "%.4f", v/3600}' ;;
    d) awk -v v="$num" 'BEGIN{printf "%.4f", v*24}' ;;
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

# ------------------------------------------------------- the gcloud invocations

# The argv is built in a function, and never inline at the call site, so the
# self-test can read it without creating anything. That is the only way to check
# a flag before a bill does, and the first version of this script needed it: it
# passed --max-run-duration to `container clusters create`, where that flag DOES
# NOT EXIST. It is a node-pool flag (`container node-pools create|update`), so
# the create would have failed outright and the guardrail the whole directory is
# built around was never going to be applied at all.
build_create_args() { # <cluster>
  CREATE_ARGS=(container clusters create "$1"
    --project "$PROJECT" --zone "$ZONE"
    --num-nodes "$NODES" --machine-type "$MACHINE"
    --no-enable-autoupgrade --no-enable-autorepair
    --labels "purpose=leoflow-experiment,experiment=$EXPERIMENT,expires-after=$TTL")
  if [ "$SPOT" = "1" ]; then
    CREATE_ARGS+=(--spot)
  fi
  if [ "$EXPERIMENT" = "netpol" ]; then
    # #1089's rows are unprovable on a CNI that accepts NetworkPolicy and ignores
    # it, which is exactly what k3d does. Dataplane V2 is the whole reason this
    # experiment needs a cloud cluster at all.
    CREATE_ARGS+=(--enable-dataplane-v2)
  fi
  return 0
}

# DEFAULT_POOL is the node pool `clusters create` makes for us. The TTL is put on
# it in a second call because that is where the flag lives.
DEFAULT_POOL="default-pool"

build_ttl_args() { # <cluster>
  TTL_ARGS=(container node-pools update "$DEFAULT_POOL"
    --cluster "$1" --project "$PROJECT" --zone "$ZONE"
    --max-run-duration "${TTL_SECONDS}s" --quiet)
  return 0
}

build_ttl_check_args() { # <cluster>
  TTL_CHECK_ARGS=(container node-pools describe "$DEFAULT_POOL"
    --cluster "$1" --project "$PROJECT" --zone "$ZONE"
    --format "value(config.maxRunDuration)")
  return 0
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

# has_flag reports whether an argv carries a flag, matching the WHOLE word so
# --max-run-duration can never be found inside --max-run-duration-something.
has_flag() { # <flag> <argv...>
  local want="$1" a; shift
  for a in "$@"; do
    [ "$a" = "$want" ] && return 0
    case "$a" in "$want"=*) return 0 ;; esac
  done
  return 1
}

# flags_known_to_gcloud checks every flag in an argv against that subcommand's
# own --help. This is the check that was missing: a flag on the wrong subcommand
# looks exactly like a flag on the right one until the API refuses it, and the
# refusal arrives at the moment you are trying to create something.
#
# Skipped, loudly, when gcloud is not installed, because a gate that needs a
# cloud SDK on CI is a gate that quietly stops running.
flags_known_to_gcloud() { # <subcommand words as one string> <argv...>
  local sub="$1" help a flagname; shift
  if ! command -v gcloud >/dev/null 2>&1; then
    echo "  ..   skipped: no gcloud on PATH, so '$sub' flags were not checked against its help"
    return 0
  fi
  # shellcheck disable=SC2086 # $sub is several words (a gcloud subcommand path)
  help="$(gcloud $sub --help 2>/dev/null)" || true
  if [ -z "$help" ]; then
    echo "  ..   skipped: 'gcloud $sub --help' produced nothing"
    return 0
  fi
  local bad=0
  for a in "$@"; do
    case "$a" in
      --*) flagname="${a%%=*}" ;;
      *) continue ;;
    esac
    # A here-string and not a pipe into grep -q: with `set -o pipefail`, grep -q
    # exits on the first match, printf dies of SIGPIPE, and the pipeline reports
    # 141 for a flag that IS documented. Every flag whose first mention is early
    # in the help then reads as missing.
    #
    # The pattern is bounded on both sides so --disk cannot be satisfied by
    # --disk-size, which is the same whole-token rule the teardown's label guard
    # follows.
    if grep -qE -- "(^|[^A-Za-z0-9-])${flagname}([^A-Za-z0-9-]|$)" <<<"$help"; then
      continue
    fi
    echo "  FAIL gcloud $sub does not accept $flagname"
    bad=1
  done
  [ "$bad" = "0" ] && { echo "  ok   every flag passed to 'gcloud $sub' exists in its help"; return 0; }
  return 1
}

argv_self_test() {
  local PROJECT=stub-project ZONE=stub-zone TTL_SECONDS=14400
  build_create_args "leoflow-exp-netpol-01011200"
  build_ttl_args "leoflow-exp-netpol-01011200"
  build_ttl_check_args "leoflow-exp-netpol-01011200"

  # The defect this whole section exists for. --max-run-duration belongs to
  # `container node-pools`, and `container clusters create` rejects it, so a
  # create carrying it fails and nothing is ever provisioned with a TTL.
  if has_flag --max-run-duration "${CREATE_ARGS[@]}"; then
    echo "  FAIL the cluster create carries --max-run-duration, which that command does not accept"
    fail=1
  else
    echo "  ok   the cluster create does not carry --max-run-duration (a node-pool flag)"
  fi
  if has_flag --max-run-duration "${TTL_ARGS[@]}"; then
    echo "  ok   the node-pool update carries the TTL"
  else
    echo "  FAIL nothing applies the node TTL, which is the one guardrail Google enforces for us"
    fail=1
  fi
  case " ${TTL_ARGS[*]} " in
    *" 14400s "*) echo "  ok   the TTL handed to Google is the one the estimate was printed for" ;;
    *) echo "  FAIL the node-pool update does not carry the computed TTL: ${TTL_ARGS[*]}"; fail=1 ;;
  esac
  # The claim in README.md is that a fixed pool cannot grow into a bill. That is
  # true only while nothing turns autoscaling on.
  if has_flag --enable-autoscaling "${CREATE_ARGS[@]}"; then
    echo "  FAIL the create enables autoscaling, so the pool can grow into a bill"
    fail=1
  else
    echo "  ok   autoscaling is never enabled, so the node count is the one that was priced"
  fi

  flags_known_to_gcloud "container clusters create" "${CREATE_ARGS[@]}" || fail=1
  flags_known_to_gcloud "container node-pools update" "${TTL_ARGS[@]}" || fail=1
  flags_known_to_gcloud "container node-pools describe" "${TTL_CHECK_ARGS[@]}" || fail=1
}

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

  hours_of -2h >/dev/null 2>&1 && { echo "  FAIL a negative TTL is accepted"; fail=1; } || echo "  ok   a negative TTL is refused rather than sent to Google"
  hours_of 0h >/dev/null 2>&1 && { echo "  FAIL a zero TTL is accepted"; fail=1; } || echo "  ok   a zero TTL is refused"
  hours_of abch >/dev/null 2>&1 && { echo "  FAIL a non-numeric TTL is accepted as 0"; fail=1; } || echo "  ok   a non-numeric TTL is refused rather than silently read as zero"

  argv_self_test

  case "$(usage)" in
    *"--experiment pod-per-task"*"--self-test"*) echo "  ok   -h still prints the usage block" ;;
    *) echo "  FAIL -h no longer prints the usage block; the header extraction has drifted"; fail=1 ;;
  esac

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
  node TTL       $TTL, put on the node pool after create and read back
  autoscaling    off; a fixed pool cannot grow into a bill

  estimated cost of this window     ~USD $COST
  the same cluster left for a month ~USD $FORGOTTEN

  Prices are us-central1 list estimates, not a quote. The second number is why
  the node TTL exists: if every other safeguard fails, the nodes still expire.

  The estimate covers nodes and the cluster fee, and nothing else. It does NOT
  include boot disks (GKE gives each node 100 GB of pd-balanced by default, about
  USD 0.014 an hour per node), egress, load balancers, or any persistent disk an
  experiment leaves behind. Deleting the cluster does not delete a PD that
  outlived its PVC, and leoflow's staging volumes are exactly that shape.
  Run test/gcp/teardown.sh --leftovers after a run.

PLAN

if [ "$SPOT" = "1" ]; then
  warn "Spot nodes can be preempted, and a preemption looks exactly like the infra failure these experiments measure."
  warn "For a resilience run that is a confound, not a saving. Drop --spot unless you are only smoke-testing the scripts."
fi

[ "$DRY_RUN" = "1" ] && { log "dry run: nothing created"; exit 0; }

# ---------------------------------------------------------------- create

if [ "$EXPERIMENT" = "netpol" ]; then
  log "netpol: Dataplane V2 on, because a CNI that does not enforce makes the assertion pass vacuously"
fi

build_create_args "$CLUSTER"
build_ttl_args "$CLUSTER"
build_ttl_check_args "$CLUSTER"

log "creating $CLUSTER in $ZONE (project $PROJECT)"
gcloud "${CREATE_ARGS[@]}" || die "cluster create failed; nothing to tear down"

# Recorded before anything else can go wrong, so the teardown can find it by
# name even if the next step is what fails.
printf '%s\n' "$CLUSTER" > .gcp-experiment-cluster

# The TTL is a SECOND call because --max-run-duration is a node-pool flag;
# `clusters create` rejects it outright. A cluster whose nodes have no expiry is
# the thing this directory exists to prevent, so if the TTL cannot be applied and
# read back, the cluster goes away now rather than becoming a bill nobody is
# watching.
log "applying the $TTL node TTL to $DEFAULT_POOL (nodes may be recreated to pick it up)"
if ! gcloud "${TTL_ARGS[@]}"; then
  warn "the node TTL could not be applied; deleting the cluster rather than leaving it unbounded"
  "$(dirname "$0")/teardown.sh" "$CLUSTER" || die "DELETE $CLUSTER BY HAND NOW: gcloud container clusters delete $CLUSTER --zone $ZONE"
  die "node TTL not applied; the cluster was deleted"
fi

APPLIED_TTL="$(gcloud "${TTL_CHECK_ARGS[@]}" 2>/dev/null || true)"
if [ -z "$APPLIED_TTL" ]; then
  warn "the node pool reports no maxRunDuration, so nothing would expire on its own"
  "$(dirname "$0")/teardown.sh" "$CLUSTER" || die "DELETE $CLUSTER BY HAND NOW: gcloud container clusters delete $CLUSTER --zone $ZONE"
  die "node TTL not readable back; the cluster was deleted"
fi

ok "cluster up, and the node pool reports maxRunDuration=$APPLIED_TTL"
cat <<NEXT

  Delete it the moment you are done, do not wait for the TTL:

      test/gcp/teardown.sh $CLUSTER

  The node TTL removes the expensive part. The control plane keeps billing at
  about USD $GKE_MGMT_HOURLY an hour until the CLUSTER is deleted, so the TTL is
  the safety net and the teardown is still the job.

NEXT
