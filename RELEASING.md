# Releasing Leoflow

Cutting a release is one command: `scripts/cut-release.sh <version>`. The script
owns the mechanical flow so it is not re-derived (and re-broken) by hand each time
(#879). You own the *decisions*: whether to cut, the version, and rc-vs-GA.

## The changelog fills itself, per PR

Every PR records its own user-facing change under `## [Unreleased]` in
`CHANGELOG.md`, enforced by the **`changelog-guard`** CI job
(`scripts/check-changelog-entry.sh`). So by the time you cut, `[Unreleased]` is
already the complete release note — no scramble to reconstruct it, and no
back-fill "docs(changelog)" PRs (the recurring pattern this gate ends). A PR with
no user-facing change (release-prep, chore, dependabot, docs-only) carries the
**`skip-changelog`** label to bypass the gate.

## The flow the script runs

1. **Preflight** — required tools, clean tree, on `main`, version validated,
   rc-vs-GA detected from the `-rc.N` suffix.
2. **Prepare** — a `release/<tag>` branch: bump `helm/leoflow/Chart.yaml`
   `version`+`appVersion` in lockstep (ADR 0028), regenerate the chart README with
   `helm-docs`, and for a **GA** move `CHANGELOG [Unreleased]` to `[X.Y.Z] - <date>`
   with a fresh empty `[Unreleased]` (an **rc** keeps `[Unreleased]`), . On a **GA** the
   published docs root is repointed too, but **after the tag exists** — see
   below. Run every
   `scripts/check-*.sh` gate against the tag. One of them,
   `check-changelog-entry.sh`, is a pull-request gate with no question to ask
   when there is no pull request, so it reports `gate SKIP`; the cut log
   distinguishes SKIP from PASS to keep the count honest.
3. **PR → wait green → merge** — opens the prepare PR and waits for CI, re-running
   **only known-transient flakes** (registry rate-limits, Go module-proxy resets,
   the shallow-fetch merge-base gate, the cold-start `/readyz` timeout) and never
   hard-failing on them; then squash-merges.
4. **Guard → tag** — verifies the Chart at the merge commit matches the version,
   then — behind an explicit **confirmation gate** — tags and pushes.
5. **Watch** — follows the tag's release workflows to **PUBLISHED**, un-drafting +
   re-running if the gate retracts on a flake (#862). Writes `.release-<tag>.log`.


## If the cut dies after the prepare PR merged

The cut is a transaction with an irreversible middle. Once the prepare PR is
merged, `main` carries the bump and the `release/<tag>` branch is gone — so a
failure at the merge-commit gate (which is how a cut fails: the tag gate
refuses a run that did not finish) cannot be recovered by re-invoking, because
the re-cut guard sees `main` already carrying the version and cannot tell an
interrupted cut from an accidental re-cut of a released one.

    scripts/cut-release.sh <version> --resume

picks up at the merge-commit gate. It refuses unless `main`'s `Chart.yaml`
carries exactly the version being cut, which is what proves the prepare half
completed, and it tags the commit that **introduced** that version rather than
`main`'s tip — a chart version is a plateau, so anything merged since the
prepare would otherwise be swept into the tag. When they differ it prints what
it is excluding. There is deliberately no override for the version check: fix
the reason the gate refused, then resume.

## Publishing the docs root (GA)

`website/scripts/ci/versions.json`'s `latest` entry is the ref the published
site root is built from. A GA repoints it and archives the one it replaces —
and the cut opens that as its own PR **after pushing the tag**, because the
deploy checks the tag out and it does not exist until then. If that PR does not
land, the release is still published; the docs root just still serves the
previous GA until it does.

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
