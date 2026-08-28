#!/usr/bin/env python3
"""Render a Hugo config OVERLAY for one leg of the multi-version Pages build
(issue #814).

Why an overlay instead of editing hugo.toml in place: the four legs (latest
GA / dev / each archived tag) are built from FOUR DIFFERENT git refs, and an
archived tag's own website/hugo.toml predates this versioning scheme (it may
have only the old cosmetic dev/latest pair, or none at all). We never rely on
the checked-out ref's hugo.toml to already know about the other legs. Instead
this script reads website/scripts/ci/versions.json from THIS ref (the ref
that triggered the workflow, i.e. always the current tooling) and renders a
second TOML file. The build then runs:

    hugo --config hugo.toml,<this-overlay> --baseURL <leg-url> ...

Hugo merges multiple --config files left-to-right with the LAST file winning
per key (arrays are replaced wholesale, not appended) — verified locally
against hugo v0.165.0. So the overlay fully determines baseURL, the active
version label, the archived-version banner, and the complete version
dropdown for every leg, regardless of what that leg's own hugo.toml contains.

The "dev" leg's ref in versions.json is the empty string (meaning "whatever
ref triggered this workflow run"); that resolution happens in the workflow's
`plan` job (via jq, against github.sha), not here — this script only cares
about labels/subpaths/archived flags, which are static.

Usage:
    render-version-config.py --versions-file website/scripts/ci/versions.json \\
        --leg-id v0.4.0 --out overlay.toml
"""
import argparse
import json
import sys


def build_url(root_url: str, subpath: str) -> str:
    root = root_url.rstrip("/") + "/"
    if not subpath:
        return root
    return root + subpath.strip("/") + "/"


def toml_string(value: str) -> str:
    # Values here are URLs / version labels — no embedded quotes/newlines expected,
    # but escape defensively rather than assume.
    escaped = value.replace("\\", "\\\\").replace('"', '\\"')
    return f'"{escaped}"'


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--versions-file", required=True)
    ap.add_argument("--leg-id", required=True, help="id of the leg being built (matches versions.json)")
    ap.add_argument("--out", required=True, help="path to write the overlay TOML")
    args = ap.parse_args()

    with open(args.versions_file, encoding="utf-8") as f:
        manifest = json.load(f)

    root_url = manifest["root_url"]
    entries = manifest["versions"]

    this = next((e for e in entries if e["id"] == args.leg_id), None)
    if this is None:
        print(f"error: leg id {args.leg_id!r} not found in {args.versions_file}", file=sys.stderr)
        return 1

    leg_url = build_url(root_url, this["subpath"])

    lines = [
        f"baseURL = {toml_string(leg_url)}",
        "[params]",
        f"  version = {toml_string(this['label'])}",
        '  version_menu = "Releases"',
        f"  archived_version = {'true' if this['archived'] else 'false'}",
        f"  url_latest_version = {toml_string(root_url)}",
    ]
    for e in entries:
        entry_url = build_url(root_url, e["subpath"])
        lines.append("  [[params.versions]]")
        lines.append(f"    version = {toml_string(e['label'])}")
        lines.append(f"    url = {toml_string(entry_url)}")

    with open(args.out, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")

    print(f"render-version-config: wrote {args.out} (leg={args.leg_id}, baseURL={leg_url})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
