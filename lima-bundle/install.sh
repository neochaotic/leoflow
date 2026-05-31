#!/usr/bin/env bash
# Lima alpha bundle installer.
#
# One-shot installer for the Leoflow Lite hands-on validation on a Lima VM
# (or any clean Linux box). Downloads the latest prealpha binary, runs
# `leoflow setup` (which generates the admin password — printed in CYAN on
# the terminal, save it), drops the curated DAG bundle into the workspace,
# and prints the next-step commands.
#
# Usage on the Lima VM:
#   curl -fsSL https://raw.githubusercontent.com/neochaotic/leoflow/main/lima-bundle/install.sh | bash
# or, if you cloned the repo:
#   bash lima-bundle/install.sh

set -euo pipefail

# ─── Colors (with TTY detection) ─────────────────────────────────────────────
if [[ -t 1 ]]; then
  CYAN=$'\e[36m'; YELLOW=$'\e[33m'; GREEN=$'\e[32m'; BOLD=$'\e[1m'; RESET=$'\e[0m'
else
  CYAN=''; YELLOW=''; GREEN=''; BOLD=''; RESET=''
fi

echo "${BOLD}Leoflow Lima alpha bundle installer${RESET}"
echo

# ─── 1. Detect OS / arch ────────────────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
if [[ "$OS" != "linux" && "$OS" != "darwin" ]]; then
  echo "unsupported OS: $OS (Lima alpha targets linux; macOS works for spot-checks)" >&2
  exit 1
fi

# ─── 2. Resolve the source dir for the DAGs ─────────────────────────────────
# When this script is run via `bash lima-bundle/install.sh` from a checkout,
# the DAGs sit next to it. When piped from curl, we re-fetch them from the
# same repo+commit the script came from (BUNDLE_REPO + BUNDLE_REF defaults
# below; override via env if you're testing a branch).
BUNDLE_REPO="${BUNDLE_REPO:-neochaotic/leoflow}"
BUNDLE_REF="${BUNDLE_REF:-main}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo "")"
if [[ -n "$SCRIPT_DIR" && -d "$SCRIPT_DIR/dags" ]]; then
  DAG_SOURCE="$SCRIPT_DIR/dags"
  echo "DAG source: ${DAG_SOURCE} (local checkout)"
else
  TMPDIR="$(mktemp -d)"
  trap 'rm -rf "$TMPDIR"' EXIT
  echo "Fetching DAG bundle from github.com/${BUNDLE_REPO}@${BUNDLE_REF}..."
  curl -fsSL "https://codeload.github.com/${BUNDLE_REPO}/tar.gz/${BUNDLE_REF}" \
    | tar -xz -C "$TMPDIR" --strip-components=1 \
    "$(basename "$BUNDLE_REPO")-${BUNDLE_REF}/lima-bundle/dags" \
    "$(basename "$BUNDLE_REPO")-${BUNDLE_REF}/examples" 2>/dev/null \
    || { echo "could not fetch DAG bundle"; exit 1; }
  DAG_SOURCE="$TMPDIR/lima-bundle/dags"
  EXAMPLES_SOURCE="$TMPDIR/examples"
fi

# ─── 3. Install the leoflow binary ───────────────────────────────────────────
# Pick the latest prealpha release. The release workflow publishes
# leoflow-${OS}-${ARCH} assets.
echo
echo "${BOLD}1. Installing leoflow binary${RESET}"
RELEASE_API="https://api.github.com/repos/${BUNDLE_REPO}/releases/latest"
DOWNLOAD_URL="$(curl -fsSL "$RELEASE_API" | grep -E "browser_download_url.*leoflow-${OS}-${ARCH}\"" | head -1 | sed -E 's/.*"(https[^"]+)".*/\1/')"
if [[ -z "${DOWNLOAD_URL}" ]]; then
  echo "could not resolve latest leoflow-${OS}-${ARCH} release asset" >&2
  echo "fallback: check https://github.com/${BUNDLE_REPO}/releases manually" >&2
  exit 1
fi
echo "  url: ${DOWNLOAD_URL}"
INSTALL_DIR="${HOME}/.local/bin"
mkdir -p "$INSTALL_DIR"
curl -fsSL "$DOWNLOAD_URL" -o "$INSTALL_DIR/leoflow"
chmod +x "$INSTALL_DIR/leoflow"
export PATH="$INSTALL_DIR:$PATH"
echo "  installed: $(leoflow version 2>&1 | head -1)"

# ─── 4. Run setup (generates admin password — captured in setup's output) ───
echo
echo "${BOLD}2. Running leoflow setup (generates the admin password)${RESET}"
SETUP_LOG="$(mktemp)"
trap 'rm -f "$SETUP_LOG"' EXIT
if ! leoflow setup 2>&1 | tee "$SETUP_LOG"; then
  echo "${YELLOW}setup did not complete cleanly; check the output above${RESET}" >&2
  exit 1
fi

# Try to extract the printed credentials block from the log. setup prints
# `user:` / `password:` / `open:` lines on first install only (a re-run
# preserves the existing config and prints "already configured").
PASSWORD_LINE="$(grep -E '^\s+password:' "$SETUP_LOG" | head -1 || true)"

# ─── 5. Copy the DAG bundle into the workspace ──────────────────────────────
WORKSPACE="${HOME}/leoflow"
mkdir -p "$WORKSPACE"
echo
echo "${BOLD}3. Copying the DAG bundle into ${WORKSPACE}${RESET}"
cp -R "$DAG_SOURCE/." "$WORKSPACE/"
echo "  Lima-bundle DAGs ($(ls "$DAG_SOURCE" | wc -l | tr -d ' ')) copied"

# Curate a useful subset of /examples too. Skip the connector DAGs that
# need external services pre-configured (the user can copy those by hand
# after creating Connections in the UI).
if [[ -n "${EXAMPLES_SOURCE:-}" ]] || [[ -d "$SCRIPT_DIR/../examples" ]]; then
  EX_SRC="${EXAMPLES_SOURCE:-$SCRIPT_DIR/../examples}"
  for dag in lifecycle montecarlo_pi fan_out_aggregate taskflow_sales; do
    if [[ -d "$EX_SRC/$dag" ]]; then
      cp -R "$EX_SRC/$dag" "$WORKSPACE/"
      echo "  example DAG copied: $dag"
    fi
  done
fi

# ─── 6. Print the next-step block ───────────────────────────────────────────
echo
echo "${BOLD}${GREEN}═══ All set ═══${RESET}"
echo
if [[ -n "$PASSWORD_LINE" ]]; then
  echo "${BOLD}Save these credentials (the password is shown ONCE):${RESET}"
  echo
  echo "  user:     admin@leoflow.local"
  echo "  ${YELLOW}${PASSWORD_LINE}${RESET}"
  echo "  open:     http://localhost:8088"
  echo
  echo "  Lost the password? Run: ${CYAN}sudo leoflow lite reset-password${RESET}"
else
  echo "${YELLOW}(setup re-ran on an existing install; the password above was preserved.)${RESET}"
  echo "  Lost it? Run: ${CYAN}sudo leoflow lite reset-password${RESET}"
fi
echo
echo "${BOLD}DAGs bundled in ${WORKSPACE}:${RESET}"
ls -1 "$WORKSPACE" | sed 's/^/  - /'
echo
echo "${BOLD}Start Leoflow Lite:${RESET}"
echo "  ${CYAN}leoflow lite${RESET}"
echo
echo "  (then open http://localhost:8088 — or from your Mac host:"
echo "   http://<lima-vm-ip>:8088 with Lima port-forwarding configured)"
