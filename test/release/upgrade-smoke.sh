#!/usr/bin/env sh
# Upgrade-in-place smoke for the release gate (#150 / ADR 0033).
#
# Installs the PREVIOUS non-draft prerelease, runs `leoflow version` to prove
# the old binary is healthy, then installs the CURRENT tag and asserts the new
# version is in place + the binary still runs. Catches the class of bug where
# install.sh handles a fresh box but breaks on top of an existing ~/.leoflow/
# (stale config, stale pysrc, leftover PATH entries, etc.) — the failure mode
# the user would otherwise hit on `leoflow upgrade`.
#
# Required env:
#   LEOFLOW_REPO     — "owner/name"     (typically ${GITHUB_REPOSITORY})
#   LEOFLOW_VERSION  — the just-pushed tag we are gating
#   GH_TOKEN         — used by `gh release list` (auto in GitHub Actions)
#
# Run in a clean container (the release workflow does this; locally use
# `docker run --rm -it -e LEOFLOW_REPO=... ubuntu:24.04 bash`).
set -eu

: "${LEOFLOW_REPO:?LEOFLOW_REPO must be set (e.g. neochaotic/leoflow)}"
: "${LEOFLOW_VERSION:?LEOFLOW_VERSION must be set to the just-pushed tag}"

fail() {
  printf '  \033[31mFAIL\033[0m %s\n' "$1" >&2
  exit 1
}
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }

echo "==> resolving previous non-draft prerelease (excluding ${LEOFLOW_VERSION})"
# Page through enough releases to skip the just-published one and any drafts.
# `gh release list` orders newest-first, so the first non-draft, non-self entry
# is the previous prealpha.
PREVIOUS="$(gh release list \
  --repo "${LEOFLOW_REPO}" \
  --limit 20 \
  --json tagName,isDraft \
  --jq 'map(select(.isDraft == false and .tagName != "'"${LEOFLOW_VERSION}"'")) | .[0].tagName')"

if [ -z "${PREVIOUS}" ] || [ "${PREVIOUS}" = "null" ]; then
  echo "==> no previous non-draft prerelease available — skipping upgrade smoke"
  echo "    (first release on a fresh project; the next tag will exercise this gate)"
  pass "skipped (nothing to upgrade from)"
  exit 0
fi
echo "==> upgrading from ${PREVIOUS} → ${LEOFLOW_VERSION}"

echo "==> installing previous version (${PREVIOUS})"
LEOFLOW_VERSION="${PREVIOUS}" \
  curl -fsSL "https://raw.githubusercontent.com/${LEOFLOW_REPO}/${LEOFLOW_VERSION}/install.sh" | sh

# Make the binary discoverable on this shell — install.sh writes a hint to
# rc files but we cannot rely on those in a sh-only smoke runner.
export PATH="${HOME}/.local/bin:${HOME}/.leoflow/bin:${PATH}"

INSTALLED="$(leoflow version --output json 2>/dev/null | grep -oE 'v[0-9][^"]*' | head -1 || leoflow version 2>/dev/null | head -1)"
if [ -z "${INSTALLED}" ]; then
  fail "leoflow version reported nothing after installing ${PREVIOUS}"
fi
case "${INSTALLED}" in
  *${PREVIOUS}*) pass "previous version installed (${INSTALLED})" ;;
  *) fail "expected ${PREVIOUS} after first install, got '${INSTALLED}'" ;;
esac

echo "==> installing current version (${LEOFLOW_VERSION}) ON TOP of ${PREVIOUS}"
LEOFLOW_VERSION="${LEOFLOW_VERSION}" \
  curl -fsSL "https://raw.githubusercontent.com/${LEOFLOW_REPO}/${LEOFLOW_VERSION}/install.sh" | sh

UPGRADED="$(leoflow version --output json 2>/dev/null | grep -oE 'v[0-9][^"]*' | head -1 || leoflow version 2>/dev/null | head -1)"
case "${UPGRADED}" in
  *${LEOFLOW_VERSION}*) pass "upgrade landed (${UPGRADED})" ;;
  *) fail "after upgrading, expected ${LEOFLOW_VERSION}, got '${UPGRADED}'" ;;
esac

echo "==> verifying the managed CPython still launches after the upgrade"
if [ -x "${HOME}/.leoflow/python/bin/python3.11" ]; then
  "${HOME}/.leoflow/python/bin/python3.11" --version >/dev/null
  pass "managed CPython runs after upgrade"
else
  echo "    (managed CPython not extracted on this distro — skip)"
fi

echo
echo "  \033[32m✅ upgrade-in-place ${PREVIOUS} → ${LEOFLOW_VERSION} verified.\033[0m"
