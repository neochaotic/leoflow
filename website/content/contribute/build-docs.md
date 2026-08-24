---
title: Build the docs
linkTitle: Build the docs
weight: 30
description: Build and preview the Leoflow documentation site locally — Hugo + Docsy.
---

The documentation site is built with [Hugo](https://gohugo.io/) (extended) and the
[Docsy](https://www.docsy.dev/) theme, pulled in as a Hugo Module. Everything lives
under `website/`.

## Prerequisites

- **Hugo extended**, v0.110.0 or newer (`hugo version` must show `+extended`).
  CI pins `0.165.0`. Docsy needs the *extended* build — the plain build cannot
  compile the theme's SCSS.
- **Go** 1.26+ — used two ways: Hugo fetches Docsy (and its Bootstrap/Font Awesome
  deps) as **Hugo Modules**, and the CLI/Go reference generators run `go run`
  against the root module.
- **Node.js** 20+ — the PostCSS/autoprefixer toolchain the theme uses. Run
  `npm install` (or `npm ci`) inside `website/` once.
- **Python** 3.12+ — the reference generators post-process markdown and render the
  Python runtime API with `pdoc`. No system packages needed; `gen-python.sh`
  creates its own venv.

## First-time setup

```bash
cd website
npm ci                      # PostCSS toolchain (package-lock.json is committed)
hugo mod get                # fetch the Docsy module at the version pinned in go.mod
```

Do **not** run `hugo mod get -u` — that upgrades past the pinned Docsy version.
`hugo` auto-downloads modules at exactly the pinned versions on the next build.

## Generate the reference (do this before building)

Four sections of the site are **generated from source**, mirroring the live MkDocs
pipeline. The rendered trees are committed for preview convenience, but regenerate
them whenever the source changes (and CI reruns all four before every build, so
they can never drift):

```bash
# run from the repo root
./website/scripts/gen-cli.sh       # CLI reference from Cobra   -> content/reference/cli/
./website/scripts/gen-go.sh        # Go packages from gomarkdoc -> content/reference/go/
./website/scripts/gen-openapi.sh   # OpenAPI spec for Scalar    -> static/openapi.yaml
./website/scripts/gen-python.sh    # Python runtime API (pdoc)  -> static/python-api/
```

Each script is idempotent and documents itself in a header comment. What they do:

| Script | Source | Output | Notes |
|---|---|---|---|
| `gen-cli.sh` | `go run ./cmd/leoflow gen-docs` (Cobra) | `content/reference/cli/*.md` | Adds Hugo front matter; rewrites `.md` cross-links to pretty URLs. |
| `gen-go.sh` | `gomarkdoc` over a fixed package set | `content/reference/go/*.md` | Flattens one page per package; adds front matter. |
| `gen-openapi.sh` | `docs/api/openapi.yaml` | `static/openapi.yaml` | Read by `static/api-reference.html` (the embedded Scalar page). |
| `gen-python.sh` | `runtime/python` docstrings via `pdoc` | `static/python-api/` | A self-contained sidecar subsite, linked (not embedded) from `reference/python-api`. |

## Preview & build

```bash
cd website
hugo server                 # live-reload preview at http://localhost:1313/leoflow/
```

To produce the exact artifact CI publishes:

```bash
cd website
hugo --gc --minify          # output in website/public/
```

`--gc` also flags broken internal reference links, so a clean build means the
cross-references resolve.

## Refresh the migrated pages

Most content pages were mechanically migrated from the live MkDocs tree by the
converters under `website/scripts/migration/`. When `docs/` changes upstream and you
want to pull those edits into the Hugo tree, rerun them (they never touch
hand-authored pages — see that directory's `README.md`):

```bash
cd website/scripts/migration
python3 test_convert.py            # transforms are unit-tested (must pass)
python3 build_site.py              # regenerates the bulk pages + link-map.csv
python3 build_connections_index.py
```

{{% alert title="Redirect map (F4)" color="info" %}}
`build_site.py` emits `website/scripts/migration/link-map.csv` — an
**old-MkDocs-path → new-Hugo-URL** row for every migrated page. That file is the
source for the redirect map added in migration phase **F4**, so that bookmarks and
external links to the old flat URLs land on the right Hugo page after cutover. Keep
it in sync when you move or rename pages.
{{% /alert %}}

## How it is wired

- **`website/hugo.toml`** — site config: Docsy module import, the dev/latest version
  selector, the compact sidebar, and the top-navbar menu.
- **`website/layouts/`** — the two local overrides: `index.html` (the landing page)
  and `baseof.html` (a one-line Mermaid cache fix; see the comment in the file).
- **`website/content/`** — the docs tree, one directory per IA section.
- **`website/scripts/`** — the four reference generators and, under `migration/`,
  the MkDocs→Hugo converters.
- **`.github/workflows/website-build.yml`** — CI: installs the toolchains, runs the
  four generators, then `hugo --gc --minify`. It only builds an artifact; it never
  deploys (the live MkDocs site still ships until cutover).
- **`.github/workflows/website-deploy.yml.draft`** — the staged cutover deploy
  workflow. It is a `.draft` file, so Actions ignores it; activating it (renaming
  to `.yml`) is a deliberate maintainer step, documented in its header.

## Redirects (old URLs keep working)

The old MkDocs site serves flat `.html` URLs (`use_directory_urls: false`), and the
Hugo IA moves most pages into sections. To keep bookmarks and external links alive,
each moved page carries a **Hugo `aliases:`** block in its front matter — Hugo
renders a redirecting stub at the old path, no server config needed (works on GitHub
Pages under the `/leoflow/` subpath).

That block is generated, not hand-maintained:

```bash
cd website
python3 scripts/migration/build_redirects.py   # idempotent; ~211 aliases
python3 scripts/migration/check_links.py        # 0 broken internal links
```

`build_redirects.py` reads `scripts/migration/link-map.csv` plus the generated
CLI/Go trees; the marked `AUTO redirect aliases` block it writes is safe to
regenerate. When you move or rename a page, update `link-map.csv` and rerun it.

The **cutover runbook** — the exact steps to switch the published site from
MkDocs+mike to this one — is documented in the header of
`.github/workflows/website-deploy.yml.draft` (the committed source of truth), and
mirrored as a longer local note at `website/spec/CUTOVER.md` (`spec/` is a
local-only scratch dir, so that copy is not committed).

{{% alert title="Parallel track" color="info" %}}
This Hugo + Docsy site is migrated **in parallel** with the live MkDocs-Material
site under `docs/` + `mkdocs.yml`. The MkDocs site keeps shipping until the Hugo
site reaches full parity and we cut over. Edit `website/content/` for the new site;
do not hand-edit the MkDocs tree for migration work.
{{% /alert %}}
