# Releasing Leoflow

Cutting a release is one command: `scripts/cut-release.sh <version>`. The script
owns the mechanical flow so it is not re-derived (and re-broken) by hand each time
(#879). You own the *decisions*: whether to cut, the version, and rc-vs-GA.

## The flow the script runs

1. **Preflight** — required tools, clean tree, on `main`, version validated,
   rc-vs-GA detected from the `-rc.N` suffix.
2. **Prepare** — a `release/<tag>` branch: bump `helm/leoflow/Chart.yaml`
   `version`+`appVersion` in lockstep (ADR 0028), regenerate the chart README with
   `helm-docs`, and for a **GA** move `CHANGELOG [Unreleased]` to `[X.Y.Z] - <date>`
   with a fresh empty `[Unreleased]` (an **rc** keeps `[Unreleased]`). Run every
   `scripts/check-*.sh` gate against the tag.
3. **PR → wait green → merge** — opens the prepare PR and waits for CI, re-running
   **only known-transient flakes** (registry rate-limits, Go module-proxy resets,
   the shallow-fetch merge-base gate, the cold-start `/readyz` timeout) and never
   hard-failing on them; then squash-merges.
4. **Guard → tag** — verifies the Chart at the merge commit matches the version,
   then — behind an explicit **confirmation gate** — tags and pushes.
5. **Watch** — follows the tag's release workflows to **PUBLISHED**, un-drafting +
   re-running if the gate retracts on a flake (#862). Writes `.release-<tag>.log`.

## Release authorization

The tag + push (the irreversible publish) never happens without an explicit
confirmation: the interactive prompt (type the tag back), or `--yes` passed
deliberately for automation. Everything before the tag — prepare, PR, merge — is
recoverable; nothing is published until you confirm.

## Commands

```bash
# Cut a release candidate (interactive confirm before the tag)
scripts/cut-release.sh v0.4.4-rc.1

# Promote it to GA once the RC is validated in staging
scripts/cut-release.sh v0.4.4

# Preview the plan — no branch, commit, PR, or tag
scripts/cut-release.sh v0.4.4 --dry-run

# Non-interactive (skips the confirm prompt — use only in trusted automation)
scripts/cut-release.sh v0.4.4 --yes

# Pure-logic self-test (no network) — also runnable in CI
scripts/cut-release.sh --self-test
```

## rc → GA promotion

Cut `vX.Y.Z-rc.1` first, let the field validate it (especially the upgrade path
and any security-sensitive change), then promote to `vX.Y.Z`. Today the GA tag
rebuilds and re-runs the full gate matrix; lightening that to a build-once /
promote-artifact model is tracked in #878.

## If something goes wrong

- **PR/main red on a non-flake** — the script stops before tagging; inspect the
  run, fix, re-run the command (it refuses if the tag already exists).
- **Release retracted to a draft** — the script un-drafts and re-runs on a flake;
  if it persists, `gh release edit <tag> --draft=false` then re-run the failed
  jobs once the transient clears.
- **`no merge base`** in the heavy-E2E gate — rebase the branch onto current
  `main` (tracked: #876).

Related: #878 (light promotion gate), #862 (draft fragility), #876 (merge-base gate).
