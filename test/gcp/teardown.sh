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
#   test/gcp/teardown.sh --leftovers      # what SURVIVES a cluster delete
#   test/gcp/teardown.sh --self-test
#
# Deleting the cluster is not the end of the bill. A persistent disk whose PVC
# outlived it, a forwarding rule from a Service of type LoadBalancer, a reserved
# address, and any Artifact Registry repository a runner pushed DAG images to all
# live OUTSIDE the cluster and survive its deletion. --leftovers lists them; it
# deletes nothing, because what is safe to remove is a judgement about a project
# this script does not own.
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
# Labels arrive as key=value pairs, and each pair is compared WHOLE. A substring
# match would accept purpose=leoflow-experiment-anything, which is a different
# label naming a different cluster, and this function's only job is deciding what
# may be deleted.
#
# The separator is SEMICOLON, not comma: `--format='value(resourceLabels)'`
# renders a map as `experiment=netpol;expires-after=4h;purpose=leoflow-experiment`
# (checked against the SDK's own resource printer, not assumed). Splitting on
# commas alone left the whole printed map as one token, so this returned 1 for
# every cluster the provision script had just labelled and the teardown refused
# to delete anything it created. Both separators are accepted, because the
# printer's rendering is not a contract either of us owns.
is_ours() { # <labels-as-printed-by-gcloud>
  local pair old_ifs
  old_ifs="$IFS"; IFS=';,'
  for pair in $1; do
    if [ "$pair" = "purpose=leoflow-experiment" ]; then
      IFS="$old_ifs"; return 0
    fi
  done
  IFS="$old_ifs"
  return 1
}

# teardown_action is the whole decision, kept apart from the gcloud calls that
# feed it so it can be tested without an account. It answers with one of three
# words and nothing else.
#
# "exists" and "carries our label" are asked separately on purpose: an empty
# label string used to stand for both, so a cluster that existed and carried no
# labels was reported as already gone. A teardown whose job is to be believed
# about what is still billing must not have a path that says "gone" about
# something it never looked at.
teardown_action() { # <exists: yes|no> <labels-as-printed-by-gcloud>
  if [ "$1" != "yes" ]; then
    echo gone
    return 0
  fi
  if is_ours "$2"; then
    echo delete
    return 0
  fi
  echo refuse
}

# stub_gcloud_tests drives delete_one end to end against a stub gcloud on PATH.
#
# The unit cases above prove the decision; this proves the WIRING, which is where
# the real defect was: is_ours was correct against its own fixtures while the
# projection feeding it printed a different shape, so the guard refused every
# cluster this script had just created and no test could see it. The stub emits
# what gcloud emits.
#
# It also proves the negative: a cluster that is not ours is refused WITHOUT a
# delete being issued. A guard that refuses after the fact is not a guard.
stub_gcloud_tests() {
  local tmp calls out rc
  tmp="$(mktemp -d)"
  cat > "$tmp/gcloud" <<'STUB'
#!/usr/bin/env bash
# A stub standing in for gcloud. It records every invocation and answers the two
# questions delete_one asks: does this cluster exist, and what are its labels?
echo "$*" >> "$STUB_CALLS"
case "$*" in
  *"clusters describe"*"value(resourceLabels)"*)
    [ -f "$STUB_DELETED" ] && exit 1
    printf '%s
' "$STUB_LABELS"; exit 0 ;;
  *"clusters describe"*)
    [ -f "$STUB_DELETED" ] && exit 1
    exit 0 ;;
  *"clusters delete"*)
    : > "$STUB_DELETED"; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$tmp/gcloud"

  # Ours: deleted, and the post-delete verification sees it gone.
  local PROJECT=stub-project ZONE=stub-zone
  calls="$tmp/calls-ours"
  STUB_CALLS="$calls" STUB_DELETED="$tmp/deleted-ours"     STUB_LABELS="experiment=netpol;expires-after=4h;purpose=leoflow-experiment"     PATH="$tmp:$PATH" bash -c 'true' # keep shellcheck honest about the exports below
  out="$(STUB_CALLS="$calls" STUB_DELETED="$tmp/deleted-ours"          STUB_LABELS="experiment=netpol;expires-after=4h;purpose=leoflow-experiment"          PATH="$tmp:$PATH" delete_one leoflow-exp-netpol-01011200 2>&1)" && rc=0 || rc=$?
  if [ "${rc:-0}" = "0" ] && printf '%s' "$out" | grep -q "deleted and verified gone"; then
    echo "  ok   a cluster labelled the way gcloud prints it is deleted and verified"
  else
    echo "  FAIL delete_one refused or failed on a cluster this script created: $out"
    fail=1
  fi

  # Not ours: refused, and no delete was issued.
  calls="$tmp/calls-theirs"
  out="$(STUB_CALLS="$calls" STUB_DELETED="$tmp/deleted-theirs"          STUB_LABELS="purpose=production;team=platform"          PATH="$tmp:$PATH" delete_one someone-elses-cluster 2>&1)" && rc=0 || rc=$?
  if [ "${rc:-0}" != "0" ] && [ -f "$calls" ] && grep -q "clusters describe" "$calls" && ! grep -q "clusters delete" "$calls"; then
    echo "  ok   a cluster this script did not create is refused, and no delete is issued"
  else
    echo "  FAIL delete_one did not refuse a foreign cluster (rc=$rc, calls: $(cat "$calls"))"
    fail=1
  fi

  rm -rf "$tmp"
}

self_test() {
  local fail=0
  _t() { if "$@"; then echo "  ok   $*"; else echo "  FAIL $*"; fail=1; fi; }
  _f() { if "$@"; then echo "  FAIL (should have refused) $*"; fail=1; else echo "  ok   refused: $*"; fi; }
  _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  # The shape gcloud ACTUALLY prints. Verified against the SDK's own resource
  # printer: value(resourceLabels) joins pairs with SEMICOLONS, not commas.
  #   experiment=netpol;expires-after=4h;purpose=leoflow-experiment
  # The comma cases below are a hand-written fixture, which is what let a
  # comma-only split look correct while the guard refused every cluster this
  # script created.
  _t is_ours "experiment=netpol;expires-after=4h;purpose=leoflow-experiment"
  _t is_ours "purpose=leoflow-experiment;experiment=netpol"
  _f is_ours "purpose=leoflow-experiment-lookalike;experiment=netpol"
  _t is_ours "purpose=leoflow-experiment,experiment=netpol"
  _t is_ours "experiment=netpol,purpose=leoflow-experiment"
  _f is_ours "purpose=production"
  _f is_ours ""
  _f is_ours "purpose=leoflow-experiment-lookalike-suffix-only"
  _f is_ours "notpurpose=leoflow-experiment"
  _t is_ours "a=b,purpose=leoflow-experiment,c=d"

  # The three-way decision, separated from the gcloud calls that feed it. The
  # middle case is the one worth having: a cluster that EXISTS and carries no
  # labels used to be reported "already gone", because an empty label string was
  # read as absence. This script's whole job is not lying about what is still
  # billing, so an unreadable cluster refuses rather than reassures.
  _eq "$(teardown_action no "")" "gone" "a cluster that does not exist is gone"
  _eq "$(teardown_action yes "experiment=netpol;purpose=leoflow-experiment")" "delete" "ours, so delete it"
  _eq "$(teardown_action yes "")" "refuse" "exists but carries no labels: refuse, never call it gone"
  _eq "$(teardown_action yes "purpose=production")" "refuse" "someone else's cluster is refused"

  stub_gcloud_tests

  [ "$fail" = "0" ] && { echo "teardown self-test: ok"; return 0; }
  return 1
}

list_ours() {
  gcloud container clusters list --project "$PROJECT" --zone "$ZONE" \
    --filter "resourceLabels.purpose=leoflow-experiment" \
    --format="table(name,status,currentNodeCount,resourceLabels.experiment,resourceLabels['expires-after'],createTime)" 2>/dev/null
}

cluster_exists() { # <cluster>
  gcloud container clusters describe "$1" --project "$PROJECT" --zone "$ZONE" >/dev/null 2>&1
}

delete_one() { # <cluster>
  local c="$1" labels exists
  exists=no
  cluster_exists "$c" && exists=yes
  labels=""
  if [ "$exists" = "yes" ]; then
    labels="$(gcloud container clusters describe "$c" --project "$PROJECT" --zone "$ZONE" \
                --format='value(resourceLabels)' 2>/dev/null || true)"
  fi
  case "$(teardown_action "$exists" "$labels")" in
    gone)
      ok "$c is already gone"
      return 0 ;;
    refuse)
      # A refusal returns rather than exits, so one foreign lookalike cannot
      # abort a --all sweep half way through. The caller decides: naming a
      # cluster explicitly and being refused is fatal, being refused inside a
      # sweep is a skip. It matters because the label filter that selects the
      # sweep is not exact: gcloud documents `key = value` as equivalent to the
      # word-match `:` for some APIs, so a cluster labelled
      # purpose=leoflow-experiment-something can be selected and must then be
      # stepped over rather than kill the run.
      warn "$c does not carry $LABEL_SELECTOR (labels: ${labels:-none}); leaving it alone"
      return 1 ;;
  esac

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

# The self-test runs with no account and no environment, so it is dispatched
# after the functions it exercises are defined and before anything reads
# GCP_PROJECT. A gate that needs credentials is a gate that does not run.
[ "${1:-}" = "--self-test" ] && { self_test; exit $?; }

PROJECT="${GCP_PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}"
ZONE="${GCP_ZONE:-$(gcloud config get-value compute/zone 2>/dev/null || true)}"
[ -n "$PROJECT" ] && [ "$PROJECT" != "(unset)" ] || die "set GCP_PROJECT or 'gcloud config set project'"
[ -n "$ZONE" ] && [ "$ZONE" != "(unset)" ] || die "set GCP_ZONE or 'gcloud config set compute/zone'"

# leftovers reports what a cluster delete does NOT remove. It only lists: the
# same label that makes deleting a cluster safe does not exist on a disk left by
# a PVC, so there is nothing here to tell ours from a project's own resources,
# and a teardown that guesses at that is worse than one that prints a list.
leftovers() {
  log "unattached persistent disks in $ZONE (a PVC that outlived its cluster leaves one; leoflow's staging volumes are this shape)"
  gcloud compute disks list --project "$PROJECT" \
    --filter "zone:($ZONE) AND -users:*" \
    --format "table(name,sizeGb,type,creationTimestamp)" || warn "could not list disks"
  echo
  log "forwarding rules (a Service of type LoadBalancer leaves these behind)"
  gcloud compute forwarding-rules list --project "$PROJECT" \
    --format "table(name,IPAddress,region,target)" || warn "could not list forwarding rules"
  echo
  log "reserved static addresses (billed while reserved and unused)"
  gcloud compute addresses list --project "$PROJECT" \
    --format "table(name,address,status,region)" || warn "could not list addresses"
  echo
  log "artifact registry repositories (a runner that pushes DAG images creates one, and nothing here deletes it)"
  gcloud artifacts repositories list --project "$PROJECT" \
    --format "table(name,format,createTime)" || warn "could not list repositories (the artifactregistry API may be off)"
  echo
  warn "Nothing above was deleted. Read the list, then remove what is yours."
}

case "${1:-}" in
  --leftovers)
    leftovers
    exit 0 ;;
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
    skipped=0
    for n in $names; do delete_one "$n" || skipped=$((skipped + 1)); done
    rm -f .gcp-experiment-cluster
    if [ "$skipped" != "0" ]; then
      warn "$skipped cluster(s) were left alone because they are not labelled as ours. They are still billing; --list shows them."
      exit 1
    fi
    exit 0 ;;
  "")
    [ -f .gcp-experiment-cluster ] || die "no cluster named and no .gcp-experiment-cluster file. Try --list."
    CLUSTER="$(cat .gcp-experiment-cluster)" ;;
  *) CLUSTER="$1" ;;
esac

delete_one "$CLUSTER" || die "$CLUSTER was not deleted (see above). Delete it by hand if you are sure it is yours."
rm -f .gcp-experiment-cluster

echo
log "remaining experiment clusters:"
list_ours
