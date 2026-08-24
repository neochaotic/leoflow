"""
Phase F4 redirect map: OLD live MkDocs public URLs -> NEW Hugo pages, as Hugo
`aliases:` front matter.

The live Material site sets `use_directory_urls: false` (mkdocs.yml), so every old
public URL is a FLAT `.html` path served under the /leoflow/ Pages subpath, e.g.
`docs/warm-pools.md` -> `/leoflow/warm-pools.html`. The new Hugo IA moves most pages
to a section (`/leoflow/operate/warm-pools/`). Hugo aliases are root-relative and are
prefixed with the baseURL /leoflow/ subpath post-render, so an alias of
`/warm-pools.html` publishes a redirecting stub at `public/warm-pools.html` which
GitHub Pages serves at `/leoflow/warm-pools.html` — exactly the old URL.

Sources of the old->new map:
  * scripts/migration/link-map.csv  (159 rows: copy pages, ADRs, connections, merges,
    splits, landings, stubs) — the bulk.
  * the generated CLI + Go reference trees, which ALSO moved URL (docs/cli/* ->
    /reference/cli/*, docs/go/{internal,pkg}/* -> /reference/go/*) and appear in the
    old mkdocs.yml nav. These are not in link-map.csv (they are regenerated), so they
    are derived here directly from the old file tree.

Identity redirects (old public URL already served by the same physical new file) are
skipped: the home (`index.md` -> `/`), the connections landing
(`connections/index.md` -> `/connections/`), and the static Scalar page
(`api-reference.html`). Anchor-only moves (the concepts.md#glossary split) cannot be
expressed as an alias and are reported, not emitted.

Idempotent: an existing auto-managed aliases block (between the AUTO markers) is
replaced; a hand-authored `aliases:` with no marker is left untouched and reported.

Run:  cd website && python3 scripts/migration/build_redirects.py
"""
import csv
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
# repo root = the dir holding both docs/ and website/
ROOT = HERE
while ROOT != "/" and not (
    os.path.isdir(os.path.join(ROOT, "docs"))
    and os.path.isdir(os.path.join(ROOT, "website"))
):
    ROOT = os.path.dirname(ROOT)
DOCS = os.path.join(ROOT, "docs")
CONTENT = os.path.join(ROOT, "website", "content")
LINK_MAP = os.path.join(HERE, "link-map.csv")

MARK_BEGIN = "# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---"
MARK_END = "# --- end AUTO redirect aliases ---"

# Old source paths whose old public URL is already served by the identical new file.
IDENTITY = {
    "index.md",              # -> home /   (old /index.html is the home file)
    "connections/index.md",  # -> /connections/  (old /connections/index.html)
    "api-reference.html",    # -> /api-reference.html  (static Scalar page, unchanged)
}


def old_public_url(src_rel):
    """docs-relative source path -> old MkDocs public URL (flat, root-relative)."""
    if src_rel.endswith(".html"):
        return "/" + src_rel
    assert src_rel.endswith(".md"), src_rel
    return "/" + src_rel[:-3] + ".html"


def content_file_for(new_url):
    """new Hugo URL (…/) -> content file on disk, or None if it is not a page."""
    rel = new_url.strip("/")
    if rel == "" :
        return os.path.join(CONTENT, "_index.md")
    if rel.endswith(".html"):
        return None  # static asset (Scalar), not a content page
    leaf = os.path.join(CONTENT, rel + ".md")
    if os.path.isfile(leaf):
        return leaf
    section = os.path.join(CONTENT, rel, "_index.md")
    if os.path.isfile(section):
        return section
    return None


def collect():
    """Return (file -> sorted set of alias urls), report rows, unmapped list."""
    aliases = {}            # content file -> set(old urls)
    report = []             # (old_url, new_url, target_rel, note)
    unmapped = []           # (old_url, reason)

    def add(src_rel, new_url):
        if src_rel in IDENTITY:
            report.append((old_public_url(src_rel), new_url, "(identity)", "SKIP-identity"))
            return
        target = content_file_for(new_url)
        old_url = old_public_url(src_rel)
        if target is None:
            unmapped.append((old_url, f"no content page for {new_url}"))
            return
        aliases.setdefault(target, set()).add(old_url)
        report.append((old_url, new_url, os.path.relpath(target, ROOT), ""))

    # 1) link-map.csv (the bulk)
    with open(LINK_MAP, newline="", encoding="utf-8") as fh:
        for row in csv.DictReader(fh):
            src = row["old_source_path"]
            new_url = row["new_hugo_url"]
            assert src.startswith("docs/"), src
            add(src[len("docs/"):], new_url)

    # The CLI + Go reference trees are generated (not committed on this branch) by
    # the SAME generator the live MkDocs pipeline runs, so the new content tree is
    # the authoritative page set. Derive each old public URL from the new page.

    # 2) generated CLI tree: /reference/cli/<name>/ <- old /cli/<name>.html
    cli_dir = os.path.join(CONTENT, "reference", "cli")
    for name in sorted(os.listdir(cli_dir)):
        if name.endswith(".md") and name != "_index.md":
            add("cli/" + name, "/reference/cli/%s/" % name[:-3])

    # 3) generated Go tree: /reference/go/<sub>-<rest>/ <- old /go/<sub>/<rest>.html
    go_dir = os.path.join(CONTENT, "reference", "go")
    for name in sorted(os.listdir(go_dir)):
        if name.endswith(".md") and name != "_index.md":
            sub, _, rest = name[:-3].partition("-")
            add("go/%s/%s.md" % (sub, rest), "/reference/go/%s/" % name[:-3])

    return aliases, report, unmapped


def inject(target, urls):
    """Insert/replace the AUTO aliases block in a file's YAML front matter."""
    with open(target, encoding="utf-8") as fh:
        text = fh.read()
    assert text.startswith("---\n"), target

    block_lines = [MARK_BEGIN, "aliases:"] + ["  - %s" % u for u in sorted(urls)] + [MARK_END]
    block = "\n".join(block_lines) + "\n"

    # Replace an existing AUTO block if present.
    if MARK_BEGIN in text:
        text = re.sub(
            re.escape(MARK_BEGIN) + r".*?" + re.escape(MARK_END) + r"\n",
            block,
            text,
            flags=re.DOTALL,
        )
        with open(target, "w", encoding="utf-8") as fh:
            fh.write(text)
        return "replaced"

    # A hand-authored aliases: (no marker) — leave it, caller reports.
    fm_end = text.index("\n---", 4)
    fm = text[4:fm_end]
    if re.search(r"^aliases:\s*$|^aliases:\s*\[", fm, re.MULTILINE):
        return "manual-present"

    # Insert the block right after the opening '---' line.
    new_text = "---\n" + block + text[4:]
    with open(target, "w", encoding="utf-8") as fh:
        fh.write(new_text)
    return "inserted"


def main():
    aliases, report, unmapped = collect()

    total_aliases = sum(len(v) for v in aliases.values())
    if "--report" in sys.argv:
        print("old_public_url,new_hugo_url,target_file,note")
        for row in sorted(report):
            print(",".join(row))
        print("\n# UNMAPPED (old URL with no clear new target):")
        for u, why in unmapped:
            print("  %s  -- %s" % (u, why))
        print("\n# files: %d, aliases: %d, unmapped: %d"
              % (len(aliases), total_aliases, len(unmapped)))
        return

    tally = {}
    for target, urls in aliases.items():
        r = inject(target, urls)
        tally[r] = tally.get(r, 0) + 1

    print("files touched: %d" % len(aliases))
    print("aliases added: %d" % total_aliases)
    print("status: %s" % tally)
    if unmapped:
        print("UNMAPPED old URLs (no new target): %d" % len(unmapped))
        for u, why in unmapped:
            print("  %s  -- %s" % (u, why))


if __name__ == "__main__":
    main()
