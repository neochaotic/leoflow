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
- **Go** (Docsy and its dependencies are fetched as Hugo Modules).
- **Node.js** — the PostCSS/autoprefixer toolchain the theme uses. Run
  `npm install` inside `website/` once.

## Build & preview

```bash
cd website
npm install                 # once: PostCSS toolchain
hugo mod get                # fetch the Docsy module (first run)
hugo server                 # live-reload preview at http://localhost:1313/
```

To produce the exact artifact CI publishes:

```bash
cd website
hugo --gc --minify          # output in website/public/
```

`--gc` also flags broken internal reference links, so a clean build means the
cross-references resolve.

## How it is wired

- **`website/hugo.toml`** — site config: Docsy module import, the dev/latest version
  selector, and the top-navbar menu.
- **`website/layouts/`** — the two local overrides: `index.html` (the landing page)
  and `baseof.html` (a one-line Mermaid cache fix; see the comment in the file).
- **`website/content/`** — the docs tree, one directory per IA section.

{{% alert title="Parallel track" color="info" %}}
This Hugo + Docsy site is migrated **in parallel** with the live MkDocs-Material
site under `docs/` + `mkdocs.yml`. The MkDocs site keeps shipping until the Hugo
site reaches full parity and we cut over. Edit `website/content/` for the new site;
do not hand-edit the MkDocs tree for migration work.
{{% /alert %}}
