#!/usr/bin/env bash
#
# Delete an experiment cluster, and tell the truth about what is left.
#
# The node TTL set at provision time removes the expensive part on its own. This
# removes the rest, and more importantly it VERIFIES: a teardown that reports
# success without checking is how a cluster survives a script that claimed to
# have deleted it.
#
# Safe to run twice, and safe to run against a cluster that is already gone.
#
# Usage:
#   test/gcp/teardown.sh [CLUSTER]        # defaults to .gcp-experiment-cluster
#   test/gcp/teardown.sh --all            # every cluster this repo labelled
#   test/gcp/teardown.sh --list           # what exists, and what it is costing
#   test/gcp/teardown.sh --self-test
set -euo pipefail

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m    !!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 2; }

LABEL_SELECTOR="purpose=leoflow-experiment"

# is_ours refuses to delete anything this repo did not create. A teardown script
# that takes a cluster name and deletes it is one typo away from removing
# something that matters, and the label is the only thing that distinguishes an
# experiment from production.
# Labels arrive as key=value pairs joined by commas, so each pair is compared
# WHOLE. A substring match would accept purpose=leoflow-experiment-anything,
# which is a different label naming a different cluster, and this function's
# only job is deciding what may be deleted.
is_ours() { # <labels-as-printed-by-gcloud>
  local pair old_ifs
  old_ifs="$IFS"; IFS=','
  for pair in $1; do
    if [ "$pair" = "purpose=leoflow-experiment" ]; then
      IFS="$old_ifs"; return 0
    fi
  done
  IFS="$old_ifs"
  return 1
}

self_test() {
  local fail=0
  _t() { if "$@"; then echo "  ok   $*"; else echo "  FAIL $*"; fail=1; fi; }
  _f() { if "$@"; then echo "  FAIL (should have refused) $*"; fail=1; else echo "  ok   refused: $*"; fi; }

  _t is_ours "purpose=leoflow-experiment,experiment=netpol"
  _t is_ours "experiment=netpol,purpose=leoflow-experiment"
  _f is_ours "purpose=production"
  _f is_ours ""
  _f is_ours "purpose=leoflow-experiment-lookalike-suffix-only"
  _f is_ours "notpurpose=leoflow-experiment"
  _t is_ours "a=b,purpose=leoflow-experiment,c=d"

  [ "$fail" = "0" ] && { echo "teardown self-test: ok"; return 0; }
  return 1
}

[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

PROJECT="${GCP_PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}"
ZONE="${GCP_ZONE:-$(gcloud config get-value compute/zone 2>/dev/null || true)}"
[ -n "$PROJECT" ] && [ "$PROJECT" != "(unset)" ] || die "set GCP_PROJECT or 'gcloud config set project'"
[ -n "$ZONE" ] && [ "$ZONE" != "(unset)" ] || die "set GCP_ZONE or 'gcloud config set compute/zone'"

list_ours() {
  gcloud container clusters list --project "$PROJECT" --zone "$ZONE" \
    --filter "resourceLabels.purpose=leoflow-experiment" \
    --format="table(name,status,currentNodeCount,resourceLabels.experiment,resourceLabels['expires-after'],createTime)" 2>/dev/null
}

delete_one() { # <cluster>
  local c="$1" labels
  labels="$(gcloud container clusters describe "$c" --project "$PROJECT" --zone "$ZONE" \
              --format='value(resourceLabels)' 2>/dev/null || true)"
  if [ -z "$labels" ]; then
    ok "$c is already gone"
    return 0
  fi
  is_ours "$labels" || die "$c does not carry $LABEL_SELECTOR. This script only deletes clusters it created; delete it by hand if you are sure."

  log "deleting $c"
  gcloud container clusters delete "$c" --project "$PROJECT" --zone "$ZONE" --quiet \
    || warn "delete reported an error; verifying below rather than trusting it"

  # Verify. A teardown that trusts its own exit code is how a cluster outlives
  # the script that said it removed it.
  if gcloud container clusters describe "$c" --project "$PROJECT" --zone "$ZONE" >/dev/null 2>&1; then
    die "$c STILL EXISTS after delete. It is billing right now. Delete it by hand: gcloud container clusters delete $c --zone $ZONE"
  fi
  ok "$c deleted and verified gone"
}

case "${1:-}" in
  --list)
    log "experiment clusters in $PROJECT/$ZONE"
    list_ours
    echo
    warn "Anything listed is billing. The node TTL expires the nodes; the control plane bills until the CLUSTER is deleted."
    exit 0 ;;
  --all)
    names="$(gcloud container clusters list --project "$PROJECT" --zone "$ZONE" \
               --filter "resourceLabels.purpose=leoflow-experiment" --format='value(name)' 2>/dev/null || true)"
    [ -n "$names" ] || { ok "nothing labelled $LABEL_SELECTOR in $PROJECT/$ZONE"; exit 0; }
    for n in $names; do delete_one "$n"; done
    rm -f .gcp-experiment-cluster
    exit 0 ;;
  "")
    [ -f .gcp-experiment-cluster ] || die "no cluster named and no .gcp-experiment-cluster file. Try --list."
    CLUSTER="$(cat .gcp-experiment-cluster)" ;;
  *) CLUSTER="$1" ;;
esac

delete_one "$CLUSTER"
rm -f .gcp-experiment-cluster

echo
log "remaining experiment clusters:"
list_ours
