#!/usr/bin/env bash
# Cut a Leoflow release — one repo-owned entrypoint for the whole flow so the
# steps are not re-invented (and re-broken) by hand each time (#879).
#
# It: preflights, prepares the chart/CHANGELOG bump on a release branch, opens the
# prepare PR, waits for CI green (re-running ONLY known-transient flakes, never
# hard-failing on them), squash-merges, waits for main green on the merge commit,
# then — behind an explicit confirmation gate — tags and pushes, and watches the
# release workflows to PUBLISHED. A structured log is written for provenance.
#
# Usage:
#   scripts/cut-release.sh v0.4.4-rc.1            # interactive confirm before tag
#   scripts/cut-release.sh 0.4.4 --yes            # non-interactive (CI/automation)
#   scripts/cut-release.sh v0.4.4-rc.1 --dry-run  # preflight + print the plan, no mutation
#   scripts/cut-release.sh --self-test            # pure-logic cases, no network
#
# A `-rc.N` version keeps CHANGELOG `[Unreleased]`; a GA version (no `-rc`) moves
# `[Unreleased]` to `[X.Y.Z] - <date>` and opens a fresh `[Unreleased]`.
#
# Release-auth: the tag+push (the irreversible publish) NEVER happens without an
# explicit confirmation — the interactive prompt, or `--yes` passed deliberately.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="$ROOT/helm/leoflow/Chart.yaml"
CHANGELOG="$ROOT/CHANGELOG.md"
REPO="neochaotic/leoflow"

# Transient CI failures that are safe to rerun — never a code signal. Matches the
# classes seen in practice: registry rate-limits and 5xx, Go module-proxy stream
# resets, the shallow-fetch merge-base gate, the lite cold-start /readyz timing
# flake, a package index answering 5xx mid-resolve, and a k3d cluster that fails
# to come up. Every alternative is covered by the table in self_test(); add a
# pattern there first, with the line that motivated it, or it is not coverage.
#
# Deliberately absent: anything that also matches a failure of OUR OWN code. A
# "returned error: 5xx" alternative looks like a registry pattern but is what
# curl prints for a 5xx out of our control plane, which this harness queries
# from 60 sites — it would rerun past the commonest real regression there is.
FLAKE_RE='toomanyrequests|Rate exceeded|TLS handshake|i/o timeout|no space left|Connection reset|context deadline|Client\.Timeout|INTERNAL_ERROR|proxy\.golang\.org|stream ID [0-9]|readyz never responded|go mod download|failed to solve|returned error: 404|reserve cache|no merge base|exit code 128|HTTP Error 5[0-9][0-9]|Gateway Time-out|Cluster creation FAILED|failed Cluster Creation'

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarn:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ---- pure logic (unit-testable, no network) --------------------------------

# normalize_tag: accept "0.4.4", "v0.4.4", "0.4.4-rc.1" -> "v0.4.4[-rc.1]".
normalize_tag() { local v="${1#v}"; printf 'v%s' "$v"; }
# chart_version: the SemVer the Helm chart carries (no leading v).
chart_version() { printf '%s' "${1#v}"; }
# is_rc: true when the version is a pre-release candidate.
is_rc() { case "$1" in *-rc.*) return 0 ;; *) return 1 ;; esac; }
# valid_version: X.Y.Z or X.Y.Z-rc.N (no leading v).
valid_version() { [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; }

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] || { echo "FAIL: $3: '$1' != '$2'"; fail=1; }; }
  _eq "$(normalize_tag 0.4.4)"      "v0.4.4"        "normalize bare"
  _eq "$(normalize_tag v0.4.4)"     "v0.4.4"        "normalize v"
  _eq "$(normalize_tag v0.4.4-rc.1)" "v0.4.4-rc.1"  "normalize rc"
  _eq "$(chart_version v0.4.4-rc.1)" "0.4.4-rc.1"   "chart strips v"
  if is_rc "0.4.4-rc.1"; then :; else echo "FAIL: is_rc rc"; fail=1; fi
  if is_rc "0.4.4"; then echo "FAIL: is_rc ga"; fail=1; fi
  for v in 0.4.4 0.4.4-rc.1 10.20.30 1.2.3-rc.15; do valid_version "$v" || { echo "FAIL: valid_version $v"; fail=1; }; done
  for v in v0.4.4 0.4 0.4.4-rc 0.4.4rc1 1.2.3-alpha; do valid_version "$v" && { echo "FAIL: valid_version accepted bad $v"; fail=1; }; done
  # FLAKE_RE decides whether a red run gets rerun or stops the cut, so both ways
  # of getting it wrong cost something real: too broad reruns past a genuine
  # regression, and a pattern that can never match does nothing while looking
  # like coverage. Both shipped in the first cut of this change; only a table
  # caught them. A positive marked [observed] is verbatim from a real failed
  # run, [ours] from our own scripts, and the rest are fixed strings from the
  # tool that emits them (Go net/http, http2, POSIX errno, curl, urllib, k3d).
  local -a POS=()
  _flake() { POS+=("$1"); printf '%s' "$1" | grep -qE "$FLAKE_RE" || { echo "FAIL: FLAKE_RE misses transient: $1"; fail=1; }; }
  _real()  { printf '%s' "$1" | grep -qE "$FLAKE_RE" && { echo "FAIL: FLAKE_RE masks real failure: $1"; fail=1; }; }

  # [observed] Docker Hub answering 500 to a manifest HEAD, 2026-09-07: five e2e
  # jobs across four PRs died inside one window with nothing of ours involved.
  _flake 'ERROR: failed to build: failed to solve: python:3.11-slim-bookworm: failed to resolve source metadata for docker.io/library/python:3.11-slim-bookworm: unexpected status from HEAD request to https://registry-1.docker.io/v2/library/python/manifests/3.11-slim-bookworm: 500 Internal Server Error'
  _flake 'toomanyrequests: You have reached your pull rate limit.'
  _flake 'net/http: TLS handshake timeout'
  _flake 'dial tcp 140.82.121.4:443: i/o timeout'
  _flake 'write /home/runner/work/_temp/build: no space left on device'
  _flake 'curl: (56) Recv failure: Connection reset by peer'
  _flake 'rpc error: code = DeadlineExceeded desc = context deadline exceeded'
  _flake 'Get "https://proxy.golang.org/github.com/@v/list": context deadline exceeded (Client.Timeout exceeded while awaiting headers)'
  _flake 'http2: server sent GOAWAY: stream ID 15; INTERNAL_ERROR'
  # [ours] scripts/lite-redeploy.sh — the lite cold-start timing flake.
  _flake '::error::/readyz never responded after 60s — boot log tail:'
  # urllib phrasing: the index or a build backend answering 5xx mid-resolve.
  _flake 'ERROR: HTTP Error 504: Gateway Time-out'
  # k3d v5.7.4: cmd/cluster/clusterCreate.go and pkg/client/cluster.go.
  _flake 'Cluster creation FAILED, all changes have been rolled back!'
  _flake 'failed Cluster Creation: Failed Cluster Preparation'

  # A 5xx out of OUR OWN control plane is the commonest shape of a real
  # regression in this harness, which has 60 curl sites pointed at it. curl
  # reports it as an exit-22 message, so any "returned error: 5xx" alternative
  # is a bare non-zero exit in disguise.
  _real 'curl: (22) The requested URL returned error: 500'
  _real 'curl: (22) The requested URL returned error: 503'
  # Our own pyproject.toml breaking under `pip install -e ./parser` prints
  # metadata-generation-failed. It is the symptom of both a network fetch and a
  # first-party defect; the network cause always arrives on its own line and is
  # matched above, so matching the symptom only adds the false positive.
  _real 'error: metadata-generation-failed'
  # [observed] main, 2026-09-07 — a real test defect that must reach a human.
  _real '--- FAIL: TestRegisterVersionDuplicateReturns409 (0.08s)'
  _real 'versions_conflict_integration_test.go:63: 409 body leaks raw pg internals ("23505")'
  _real 'Error: Process completed with exit code 1.'

  # Every alternative must earn its place by matching one of the lines above. A
  # pattern that matches nothing is not harmless: it is a transient class we
  # believe is covered and is not. This is what catches a bad escape — FLAKE_RE
  # is single-quoted, so a doubled backslash reaches grep as a literal
  # backslash and `curl: \\(22\\)` can never match the `curl: (22)` it was
  # written for. GRANDFATHERED lists the pre-existing alternatives that predate
  # this table and for which no captured sample survives (#956); nothing may be
  # added to it.
  local GRANDFATHERED='Rate exceeded|go mod download|returned error: 404|reserve cache|no merge base|exit code 128'
  local alt hit line
  while IFS= read -r alt; do
    [ -n "$alt" ] || continue
    printf '%s' "$alt" | grep -qxE "$GRANDFATHERED" && continue
    hit=0
    for line in "${POS[@]}"; do
      printf '%s' "$line" | grep -qE -- "$alt" && { hit=1; break; }
    done
    [ "$hit" = 1 ] || { echo "FAIL: FLAKE_RE alternative matches no known transient: $alt"; fail=1; }
  done < <(printf '%s' "$FLAKE_RE" | tr '|' '\n')

  # run_verdict: the classification that decides whether the cut tags. Each
  # case is a real GitHub conclusion; the cancelled ones are why this exists.
  # Fixtures are shaped like `gh run list --json databaseId,status,conclusion`,
  # NOT like the REST API: gh's Run.Conclusion is a Go string, so a run with no
  # conclusion yet carries "" and never null. Modelling the API instead is how
  # the empty-conclusion case shipped returning GREEN — every fixture used
  # null, and null never reaches the conclusion logic because those runs are
  # status-driven.
  _eq "$(run_verdict '[]')" "NONE" "no runs for the sha yet"
  _eq "$(run_verdict '[{"status":"queued","conclusion":""}]')" "PENDING" "one run still queued"
  _eq "$(run_verdict '[{"status":"in_progress","conclusion":""}]')" "PENDING" "one run in progress"
  _eq "$(run_verdict '[{"status":"completed","conclusion":""}]')" "PENDING" "completed with no conclusion yet is not GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":""}]')" "PENDING" "one unconcluded run among successes"
  _eq "$(run_verdict '[{"status":"completed","conclusion":""},{"status":"completed","conclusion":"cancelled"}]')" "PENDING" "an empty conclusion never leaks into the BLOCKED list"
  # A conclusion that is not a string is not a pass. These lock the exit-status
  # guards on the two jq calls below the blank check: without them, jq errors on
  # `join` and on `any`, both results read as "nothing bad", and the payload
  # falls through to GREEN. Unreachable from gh, which marshals a Go string —
  # but it is the same shape as the "" bug, and the guards had no test.
  _eq "$(run_verdict '[{"status":"completed","conclusion":["success"]}]')" "PENDING" "an array conclusion is not a pass"
  _eq "$(run_verdict '[{"status":"completed","conclusion":{}}]')" "PENDING" "an object conclusion is not a pass"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"stale"}]')" "BLOCKED stale" "a stale run is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"action_required"}]')" "BLOCKED action_required" "action_required is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"success"}]')" "GREEN" "all success"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"skipped"}]')" "GREEN" "success plus a skipped gate"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"neutral"}]')" "GREEN" "neutral counts as a pass"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"failure"}]')" "FAILED" "a plain failure reaches the flake path"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"cancelled"}]')" "BLOCKED cancelled" "a cancelled run is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"timed_out"}]')" "BLOCKED timed_out" "a timed-out run is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"startup_failure"}]')" "BLOCKED startup_failure" "a startup failure is never GREEN"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"cancelled"}]')" "BLOCKED cancelled" "one cancelled among successes"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"failure"},{"status":"completed","conclusion":"cancelled"}]')" "BLOCKED cancelled" "BLOCKED outranks FAILED, so FLAKE_RE never sees it"
  _eq "$(run_verdict '[{"status":"completed","conclusion":"cancelled"},{"status":"in_progress","conclusion":""}]')" "PENDING" "PENDING outranks BLOCKED"
  # And the REST shape must not regress if anything ever feeds it in.
  _eq "$(run_verdict '[{"status":"completed","conclusion":null}]')" "PENDING" "a null conclusion is treated like an empty one"

  # The release-prep PR must carry skip-changelog. Without it the changelog
  # guard fails the prepare PR itself — on an rc it deliberately leaves
  # [Unreleased] alone — and wait_sha_green correctly refuses to call that a
  # flake, so the cut dies one step past the gates. Asserted against a stub
  # because the real call opens a pull request.
  local stubdir argv
  stubdir="$(mktemp -d)"
  printf '#!/bin/sh\nprintf "%%s\\n" "$@" > "$STUB_ARGV"\n' > "$stubdir/gh"
  chmod +x "$stubdir/gh"
  argv="$(
    export PATH="$stubdir:$PATH" STUB_ARGV="$stubdir/argv" REPO=o/r
    create_prepare_pr release/v9.9.9-rc.1 "release: prepare v9.9.9-rc.1" body >/dev/null 2>&1
    cat "$stubdir/argv" 2>/dev/null
  )"
  if printf '%s\n' "$argv" | grep -qx -- '--label' && printf '%s\n' "$argv" | grep -qx 'skip-changelog'; then
    echo "  ok   prepare PR carries --label skip-changelog"
  else
    echo "  FAIL prepare PR is missing --label skip-changelog; the changelog guard will fail it"; fail=1
  fi
  _eq "$(printf '%s\n' "$argv" | grep -cx 'release/v9.9.9-rc.1')" "1" "prepare PR heads the release branch"
  rm -rf "$stubdir"

  if [ "$fail" = 0 ]; then echo "self-test: PASS"; else echo "self-test: FAIL"; return 1; fi
}

# ---- CI waiter -------------------------------------------------------------

# run_verdict <runs-json>: classify a sha's runs. Pure (jq only, no network)
# so --self-test can table it. Echoes exactly one of:
#
#   NONE                    no runs for this sha yet — keep waiting
#   PENDING                 at least one run has not completed
#   GREEN                   every run completed in {success, skipped, neutral}
#   FAILED                  at least one `failure`, and nothing worse
#   BLOCKED <conclusions>   a completed run whose conclusion is neither a pass
#                           nor a plain failure
#
# BLOCKED is the whole point. A cancelled run has status "completed" and a
# conclusion that is not "failure", so it is neither pending nor failed — and
# the old form here returned GREEN for it. On the merge commit that is the last
# gate before `git tag`, so the cut would tag a sha whose CI never finished and
# the "main red — NOT tagging" guard would never fire. timed_out,
# startup_failure, stale and action_required have the same shape.
#
# BLOCKED outranks FAILED, and the reason is the rerun loop rather than
# FLAKE_RE: the old `failed=` list only ever collected conclusion=="failure",
# so a cancelled run was never a rerun candidate to begin with. What it did
# instead was survive. One transient failure plus one cancelled run meant
# FLAKE_RE matched the failure, the rerun ground it to success, and the next
# iteration found an empty `failed` list with the cancelled run still sitting
# there — so the loop laundered the sha and echoed GREEN. Ranking BLOCKED above
# FAILED is what stops that. It also would not converge if we tried: `gh run
# rerun --failed` has nothing to act on when a run's jobs concluded cancelled.
#
# PENDING outranks both — a sha still in flight is not judged at all.
run_verdict() { # <runs-json>
  local j="$1" n pend blank bad anyfail
  n=$(printf '%s' "$j" | jq 'length' 2>/dev/null); [[ "$n" =~ ^[0-9]+$ ]] || n=0
  [ "$n" -eq 0 ] && { echo NONE; return; }
  pend=$(printf '%s' "$j" | jq '[.[] | select(.status!="completed")] | length' 2>/dev/null)
  [[ "$pend" =~ ^[0-9]+$ ]] || pend=1
  # An unpopulated conclusion is a read-window artifact, not a verdict, so it
  # counts as PENDING: the loop re-polls, and the deadline still fails it
  # closed. It must be caught HERE and not by the `bad` line below, because
  # gh's Run.Conclusion is a Go string — an absent conclusion marshals to ""
  # and never to null, so jq's `//` never fires on it, and `[""] | join(",")`
  # is "" so a non-empty test on the result cannot see it either. That is the
  # shape that returned GREEN for a completed run with no conclusion.
  blank=$(printf '%s' "$j" | jq '[.[] | select(.status=="completed") | select((.conclusion // "") == "")] | length' 2>/dev/null)
  [[ "$blank" =~ ^[0-9]+$ ]] || blank=1
  if [ "$pend" -gt 0 ] || [ "$blank" -gt 0 ]; then echo PENDING; return; fi
  # jq's own failure must not read as "nothing bad": check the exit status
  # rather than overloading emptiness of the result, or a malformed payload
  # falls through to GREEN.
  bad=$(printf '%s' "$j" | jq -r '[.[] | .conclusion | select(. != null and . != "" and . != "success" and . != "skipped" and . != "neutral" and . != "failure")] | unique | join(",")' 2>/dev/null) || { echo PENDING; return; }
  [ -n "$bad" ] && { echo "BLOCKED $bad"; return; }
  anyfail=$(printf '%s' "$j" | jq -r 'any(.[]; .conclusion == "failure")' 2>/dev/null) || { echo PENDING; return; }
  [ "$anyfail" = "true" ] && { echo FAILED; return; }
  echo GREEN
}

# wait_sha_green <sha>: block until every run for <sha> is completed; rerun only
# transient flakes (bounded); echo GREEN or RED. Robust to the post-rerun window
# where gh briefly reports the prior conclusion (it waits for pending==0).
wait_sha_green() {
  local sha="$1" reruns=0 j verdict failed rid isflake start=$SECONDS
  local deadline="${CUT_WAIT_DEADLINE:-5400}" # 90 min; a stuck-queued run must not hang the cut forever
  while :; do
    if [ $((SECONDS - start)) -gt "$deadline" ]; then
      warn "timed out after ${deadline}s waiting on CI for ${sha:0:8} — inspect: gh run list --commit $sha"
      echo RED; return 2
    fi
    # --commit filters SERVER-side. The old form listed the 40 most recent runs
    # repo-wide and filtered headSha here, but one push fans out to ~7 runs and
    # the changelog guard alone appears 4x per sha (its labeled/unlabeled
    # triggers), so 40 runs spans about 5 shas. Measured on this repo: the
    # release sha's 4 runs were outside the window and the query returned 0,
    # which run_verdict reports as NONE, spinning to the deadline before dying
    # RED on a green sha.
    j=$(gh run list --commit "$sha" --limit 100 --json databaseId,status,conclusion 2>/dev/null \
          | jq '[.[]]' 2>/dev/null)
    # run_verdict coerces jq's output itself, so a partial or `null` read keeps
    # the loop waiting rather than declaring a verdict.
    verdict="$(run_verdict "$j")"
    case "$verdict" in
      NONE)     sleep 25; continue ;;
      PENDING)  sleep 45; continue ;;
      GREEN)    echo GREEN; return 0 ;;
      BLOCKED*) warn "CI for ${sha:0:8} has a run that did not finish (${verdict#BLOCKED }) — a run that was cancelled or timed out says nothing about the code, so it is not rerun-eligible; inspect: gh run list --commit $sha"
                echo RED; return 1 ;;
      FAILED)   ;; # falls through to the flake path below
      *)        warn "run_verdict returned an unhandled verdict '$verdict' — failing closed"
                echo RED; return 1 ;;
    esac
    failed=$(echo "$j" | jq -r '.[] | select(.conclusion=="failure") | .databaseId')
    isflake=1
    for rid in $failed; do
      gh run view "$rid" --log-failed 2>/dev/null | grep -qE "$FLAKE_RE" || isflake=0
    done
    if [ "$isflake" = 1 ] && [ "$reruns" -lt 8 ]; then
      reruns=$((reruns+1)); warn "transient flake -> rerun #$reruns ($failed)"
      for rid in $failed; do gh run rerun "$rid" --failed >/dev/null 2>&1; done
      sleep 90; continue
    fi
    echo RED; return 1
  done
}

# ---- prepare edits ---------------------------------------------------------

read_chart_version() { awk '/^version:/{print $2; exit}' "$CHART"; }

bump_chart() { # <chart_version>
  local cv="$1"
  # version + appVersion move in lockstep (ADR 0028).
  perl -0pi -e "s/^version: .*/version: $cv/m; s/^appVersion: .*/appVersion: \"$cv\"/m" "$CHART"
}

# date_the_changelog <chart_version>: GA only — move [Unreleased] to a dated
# section and open a fresh [Unreleased]. rc keeps [Unreleased] untouched.
date_the_changelog() {
  local cv="$1" today; today="$(date -u +%F)"
  grep -q '^## \[Unreleased\]' "$CHANGELOG" || die "CHANGELOG has no [Unreleased] section"
  perl -0pi -e "s/^## \\[Unreleased\\]/## [Unreleased]\n\n## [$cv] - $today/m" "$CHANGELOG"
}

# create_prepare_pr: opens the release-prep PR. Extracted so --self-test can
# assert the argv against a gh stub.
#
# --label skip-changelog is load-bearing, not tidiness. changelog-guard.yaml
# triggers on every pull_request to main and exempts only that label, and on an
# rc the prepare PR touches Chart.yaml but deliberately leaves [Unreleased]
# alone — so the guard compares equal sections and fails the PR. FLAKE_RE does
# not match "does not add a CHANGELOG entry" (correctly: it is not a flake), so
# wait_sha_green returns RED and the cut dies. Release-prep is exactly the case
# the label documents.
create_prepare_pr() { # <branch> <title> <body>
  gh pr create --repo "$REPO" --base main --head "$1" --title "$2" --body "$3" --label skip-changelog
}

run_gates() { # <tag>
  local tag="$1" s ok=0 out rc
  # nullglob: without it an empty glob leaves the literal pattern, bash exits
  # 127 on it, and the cut dies reporting "gate FAIL check-*.sh".
  shopt -s nullglob
  local -a gates=("$ROOT"/scripts/check-*.sh)
  shopt -u nullglob
  # A gate renamed out of check-*.sh silently leaves the cut's gate set with no
  # signal at all, so assert the floor.
  [ "${#gates[@]}" -ge "${MIN_GATES:-7}" ] || {
    echo "gate set shrank to ${#gates[@]} (expected >= ${MIN_GATES:-7}) — a check-*.sh was renamed or removed" >&2
    return 1
  }
  for s in "${gates[@]}"; do
    # Capture rather than discard: a bare "gate FAIL <name>" during a cut is
    # unrecoverable, since $logf does not exist until after the tag.
    if [[ "$s" == *chart-version-matches-tag* ]]; then out="$(bash "$s" "$tag" 2>&1)"; else out="$(bash "$s" 2>&1)"; fi
    rc=$?
    if [ "$rc" -ne 0 ]; then
      printf 'gate FAIL %s\n%s\n' "$(basename "$s")" "$out" >&2; ok=1
    elif [[ "$out" == *"gate skipped"* ]]; then
      # A gate that has no question to ask at cut time must not be reported as
      # a PASS: 6/6 PASS for 5 gates that ran is false confidence.
      log "gate SKIP $(basename "$s") — not applicable at cut time"
    else
      log "gate PASS $(basename "$s")"
    fi
  done
  return $ok
}

confirm_tag() { # <tag>
  [ "${ASSUME_YES:-0}" = 1 ] && { log "confirmation: --yes"; return 0; }
  local ans
  printf '\033[1;33mTag and publish %s? This is irreversible. Type the tag to confirm: \033[0m' "$1"
  read -r ans
  [ "$ans" = "$1" ] || die "confirmation did not match (got '$ans') — aborting before tag"
}

# ---- main flow -------------------------------------------------------------

main() {
  # Initialize the confirmation flag HERE so an exported ASSUME_YES already in the
  # operator's shell or CI env can never silently bypass confirm_tag (M1). Only
  # --yes, parsed below, may set it.
  ASSUME_YES=0
  local arg version="" dry=0
  for arg in "$@"; do
    case "$arg" in
      --self-test) self_test; exit $? ;;
      --dry-run)   dry=1 ;;
      --yes)       ASSUME_YES=1 ;;
      -*)          die "unknown flag: $arg" ;;
      *)           version="$arg" ;;
    esac
  done
  [ -n "$version" ] || die "usage: cut-release.sh <version> [--dry-run] [--yes]"

  version="${version#v}"
  valid_version "$version" || die "invalid version '$version' (want X.Y.Z or X.Y.Z-rc.N)"
  local tag cv logf; tag="$(normalize_tag "$version")"; cv="$(chart_version "$version")"
  logf="$ROOT/.release-$tag.log"

  for t in gh jq git helm-docs; do command -v "$t" >/dev/null || die "missing tool: $t"; done

  log "cutting $tag (chart $cv, $(is_rc "$version" && echo prerelease || echo GA))"
  git rev-parse -q --verify "refs/tags/$tag" >/dev/null 2>&1 && die "tag $tag already exists"

  if [ "$dry" = 1 ]; then
    log "DRY RUN — plan only, no branch/commit/PR/tag:"
    printf '  branch:        release/%s\n  tag:           %s\n  chart version: %s -> %s\n  kind:          %s\n  changelog:     %s\n' \
      "$tag" "$tag" "$(read_chart_version)" "$cv" \
      "$(is_rc "$version" && echo 'rc — keeps [Unreleased]' || echo 'GA — dates [Unreleased]')" \
      "$(is_rc "$version" && echo 'unchanged' || echo "[Unreleased] -> [$cv] - $(date -u +%F)")"
    exit 0
  fi

  if ! git diff --quiet || ! git diff --cached --quiet; then die "working tree not clean"; fi
  [ "$(git rev-parse --abbrev-ref HEAD)" = main ] || warn "not on main (on $(git rev-parse --abbrev-ref HEAD))"
  git fetch origin main -q

  # Re-cut guard: the target version must differ from what main already carries,
  # so `--yes` with a fat-fingered or already-released version can't silently
  # re-cut the current line.
  local cur; cur="$(git show origin/main:helm/leoflow/Chart.yaml | awk '/^version:/{print $2; exit}')"
  [ "$cv" != "$cur" ] || die "chart on main is already $cur — nothing to cut (re-cut of the same version?)"

  local branch="release/$tag"
  # Safe re-run (M3): a prior failed cut can leave release/<tag> behind (local or
  # remote) + an open PR. Refuse with an explicit cleanup rather than mutating the
  # wrong tree or opening a duplicate PR.
  if git show-ref --verify --quiet "refs/heads/$branch" || git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
    die "branch $branch already exists (a prior cut?) — clean it and re-run: git branch -D $branch 2>/dev/null; git push origin :$branch 2>/dev/null; and close/reopen its PR if any"
  fi
  log "prepare on $branch"
  git checkout -b "$branch" -q origin/main || die "could not create $branch off origin/main"
  bump_chart "$cv"
  is_rc "$version" || date_the_changelog "$cv"
  helm-docs --chart-search-root="$ROOT/helm" >/dev/null 2>&1 || die "helm-docs failed"
  run_gates "$tag" || die "mechanical gates failed — fix before cutting"

  git add helm/leoflow/Chart.yaml helm/leoflow/README.md CHANGELOG.md
  git commit -q -m "release: prepare $tag" || die "nothing to commit (already prepared?)"
  git push -u origin "$branch" -q || die "pushing $branch failed"

  local title body
  if is_rc "$version"; then title="release: prepare $tag"; body="Chart version/appVersion -> $cv (ADR 0028 lockstep). rc keeps [Unreleased]."; \
  else title="release: promote $tag GA"; body="CHANGELOG [Unreleased] -> [$cv] - $(date -u +%F); Chart version/appVersion -> $cv (ADR 0028 lockstep)."; fi
  create_prepare_pr "$branch" "$title" "$body" >/dev/null
  local pr; pr="$(gh pr view "$branch" --json number -q .number)"
  log "prepare PR #$pr — waiting for CI"
  [ "$(wait_sha_green "$(git rev-parse "$branch")")" = GREEN ] || die "PR #$pr CI red (non-flake) — inspect and retry"

  gh pr merge "$pr" --repo "$REPO" --squash --delete-branch --subject "$title" --body "$body" >/dev/null || die "merge failed"
  log "PR #$pr merged"
  sleep 8; git fetch origin main -q
  local sha; sha="$(git rev-parse origin/main)"
  [ "$(git show "$sha:helm/leoflow/Chart.yaml" | awk '/^version:/{print $2;exit}')" = "$cv" ] || die "guard: Chart at $sha is not $cv"
  log "main CI on merge commit ${sha:0:8}"
  [ "$(wait_sha_green "$sha")" = GREEN ] || die "main red on the merge commit — NOT tagging"

  confirm_tag "$tag"
  git tag -a "$tag" "$sha" -m "leoflow $tag" || die "creating tag $tag failed"
  git push origin "$tag" || die "pushing tag $tag failed — the tag is local only; 'git push origin $tag' when ready"
  log "tagged $tag @ ${sha:0:8}"
  { echo "tag=$tag sha=$sha date=$(date -u +%FT%TZ) kind=$(is_rc "$version" && echo rc || echo ga)"; } >>"$logf"

  log "release workflows"
  sleep 15
  local reruns=0 j verdict failed rid isflake rstart=$SECONDS
  local rdeadline="${CUT_WAIT_DEADLINE:-5400}"
  while :; do
    if [ $((SECONDS - rstart)) -gt "$rdeadline" ]; then
      warn "timed out after ${rdeadline}s waiting on the $tag release workflows — inspect: gh run list --branch $tag"
      break
    fi
    # --branch filters server-side, same window problem as wait_sha_green.
    j=$(gh run list --branch "$tag" --limit 100 --json databaseId,status,conclusion 2>/dev/null \
          | jq '[.[]]' 2>/dev/null)
    # Same classification as wait_sha_green — see run_verdict.
    verdict="$(run_verdict "$j")"
    case "$verdict" in
      NONE)     sleep 20; continue ;;
      PENDING)  sleep 45; continue ;;
      GREEN)    log "$tag PUBLISHED"; break ;;
      BLOCKED*) warn "a $tag release run did not finish (${verdict#BLOCKED }) — the tag is already pushed, so inspect before announcing: gh run list --branch $tag"
                break ;;
      FAILED)   ;; # falls through to the flake path below
      *)        warn "run_verdict returned an unhandled verdict '$verdict' — stopping the watch"
                break ;;
    esac
    failed=$(echo "$j" | jq -r '.[] | select(.conclusion=="failure") | .databaseId')
    isflake=1; for rid in $failed; do gh run view "$rid" --log-failed 2>/dev/null | grep -qE "$FLAKE_RE" || isflake=0; done
    if [ "$isflake" = 1 ] && [ "$reruns" -lt 8 ]; then
      reruns=$((reruns+1)); warn "release flake -> rerun #$reruns"
      # If the gate retracted the release to a draft, un-draft so a download-based
      # smoke can re-fetch on rerun (see #862).
      gh release edit "$tag" --repo "$REPO" --draft=false >/dev/null 2>&1 || true
      for rid in $failed; do gh run rerun "$rid" --failed >/dev/null 2>&1; done
      sleep 60; continue
    fi
    warn "release workflows red (non-flake) — inspect the run + gate"; break
  done
  gh release view "$tag" --repo "$REPO" --json tagName,isDraft,isPrerelease,url \
    -q '"release \(.tagName) draft=\(.isDraft) prerelease=\(.isPrerelease) \(.url)"' 2>/dev/null | tee -a "$logf"
  log "done — log at $logf"
}

main "$@"
