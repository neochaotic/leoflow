#!/usr/bin/env bash
# Verify every directory .github/dependabot.yaml points at actually exists.
#
# Dependabot does not complain about a directory with no manifest — it finds
# nothing and reports nothing. A config entry for a path that was renamed or
# never existed therefore looks configured and silently updates nothing, which
# is how this repository went its whole life without a single base-image update:
# three docker entries pointed at /runtime/python-3.1{0,1,2}, none of which are
# in the tree.
#
# Nothing else can catch this. Dependabot's own validation checks syntax, not
# whether the path resolves, and a missing update is invisible by definition.
set -euo pipefail

cd "$(dirname "$0")/.."
config=".github/dependabot.yaml"

if [[ ! -f "$config" ]]; then
	echo "FAIL: $config not found" >&2
	exit 1
fi

python3 - "$config" <<'PY'
import os
import sys

try:
	import yaml
except ImportError:
	sys.exit("SKIP: PyYAML unavailable; cannot verify dependabot directories")

config = sys.argv[1]
with open(config, encoding="utf-8") as fh:
	cfg = yaml.safe_load(fh)

missing = []
checked = 0
for update in cfg.get("updates", []):
	eco = update.get("package-ecosystem", "?")
	dirs = update.get("directories") or [update.get("directory")]
	for d in dirs:
		if d is None or "*" in d:
			# A glob is resolved by Dependabot at run time; nothing to verify.
			continue
		checked += 1
		# "/" is the repo root: lstrip leaves an empty string, which is not a dir.
		if not os.path.isdir(d.lstrip("/") or "."):
			missing.append(f"  {eco}: {d}")

if missing:
	print(f"FAIL: {config} points at directories that do not exist:")
	print("\n".join(missing))
	print("\nDependabot silently finds no manifest there, so those updates never run.")
	sys.exit(1)

print(f"OK: all {checked} dependabot directories resolve")
PY
