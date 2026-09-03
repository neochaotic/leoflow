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
# classes seen in practice: registry rate-limits, Go module-proxy stream resets,
# the shallow-fetch merge-base gate, the lite cold-start /readyz timing flake.
FLAKE_RE='toomanyrequests|Rate exceeded|TLS handshake|i/o timeout|no space left|Connection reset|context deadline|Client\.Timeout|INTERNAL_ERROR|proxy\.golang\.org|stream ID [0-9]|readyz never responded|go mod download|failed to solve|returned error: 404|reserve cache|no merge base|exit code 128'

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
  if [ "$fail" = 0 ]; then echo "self-test: PASS"; else echo "self-test: FAIL"; return 1; fi
}

# ---- CI waiter -------------------------------------------------------------

# wait_sha_green <sha>: block until every run for <sha> is completed; rerun only
# transient flakes (bounded); echo GREEN or RED. Robust to the post-rerun window
# where gh briefly reports the prior conclusion (it waits for pending==0).
wait_sha_green() {
  local sha="$1" reruns=0 j pend failed rid isflake start=$SECONDS
  local deadline="${CUT_WAIT_DEADLINE:-5400}" # 90 min; a stuck-queued run must not hang the cut forever
  while :; do
    if [ $((SECONDS - start)) -gt "$deadline" ]; then
      warn "timed out after ${deadline}s waiting on CI for ${sha:0:8} — inspect: gh run list --commit $sha"
      echo RED; return 2
    fi
    j=$(gh run list --limit 40 --json databaseId,status,conclusion,headSha \
          -q "[.[] | select(.headSha==\"$sha\")]" 2>/dev/null)
    # jq can emit partial output or `null` before erroring, so coerce to a real
    # integer before any [ -eq/-gt ] (else "integer expression expected"). Safe
    # defaults keep the loop waiting rather than falsely declaring a verdict.
    cnt=$(printf '%s' "$j" | jq 'length' 2>/dev/null); [[ "$cnt" =~ ^[0-9]+$ ]] || cnt=0
    [ "$cnt" -eq 0 ] && { sleep 25; continue; }
    pend=$(printf '%s' "$j" | jq '[.[] | select(.status!="completed")] | length' 2>/dev/null); [[ "$pend" =~ ^[0-9]+$ ]] || pend=1
    [ "$pend" -gt 0 ] && { sleep 45; continue; }
    failed=$(echo "$j" | jq -r '.[] | select(.conclusion=="failure") | .databaseId')
    [ -z "$failed" ] && { echo GREEN; return 0; }
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

run_gates() { # <tag>
  local tag="$1" s ok=0
  for s in "$ROOT"/scripts/check-*.sh; do
    if [[ "$s" == *chart-version-matches-tag* ]]; then bash "$s" "$tag" >/dev/null 2>&1; else bash "$s" >/dev/null 2>&1; fi
    local rc=$?
    if [ "$rc" -eq 0 ]; then log "gate PASS $(basename "$s")"; else echo "gate FAIL $(basename "$s")" >&2; ok=1; fi
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
  gh pr create --repo "$REPO" --base main --head "$branch" --title "$title" --body "$body" >/dev/null
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
  local reruns=0 j pend failed rid isflake rstart=$SECONDS
  local rdeadline="${CUT_WAIT_DEADLINE:-5400}"
  while :; do
    if [ $((SECONDS - rstart)) -gt "$rdeadline" ]; then
      warn "timed out after ${rdeadline}s waiting on the $tag release workflows — inspect: gh run list --branch $tag"
      break
    fi
    j=$(gh run list --limit 25 --json databaseId,status,conclusion,headBranch -q "[.[] | select(.headBranch==\"$tag\")]" 2>/dev/null)
    # Coerce jq output to an integer before [ -eq/-gt ] (see wait_sha_green).
    cnt=$(printf '%s' "$j" | jq 'length' 2>/dev/null); [[ "$cnt" =~ ^[0-9]+$ ]] || cnt=0
    [ "$cnt" -eq 0 ] && { sleep 20; continue; }
    pend=$(printf '%s' "$j" | jq '[.[] | select(.status!="completed")] | length' 2>/dev/null); [[ "$pend" =~ ^[0-9]+$ ]] || pend=1
    [ "$pend" -gt 0 ] && { sleep 45; continue; }
    failed=$(echo "$j" | jq -r '.[] | select(.conclusion=="failure") | .databaseId')
    if [ -z "$failed" ]; then log "$tag PUBLISHED"; break; fi
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
