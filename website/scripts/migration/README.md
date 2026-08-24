# Docs migration tooling (MkDocs-Material → Hugo + Docsy)

Reusable converters that migrated `docs/*` into `website/content/` for the UX-first
Hugo + Docsy IA (Phase F1). They read the live `docs/` tree (source of truth) and
regenerate the bulk of `website/content/` deterministically — re-run them any time
`docs/` changes upstream and you want to refresh the migrated pages.

## Files

| File | Purpose |
|---|---|
| `iamap.py` | Single source of truth for the IA: old-path → new-path/URL map, per-page front-matter (title/linkTitle/weight/description), section weights. |
| `convert.py` | Pure text transforms: front-matter injection, `!!! type` admonitions → `{{% alert %}}`, `=== "x"` tabs → `{{< tabpane >}}`, image/link rewriting, MkDocs `{ .md-button }` → Bootstrap buttons. Mermaid fences left untouched. |
| `build_site.py` | Orchestrator — migrates the 32 simple pages, `why-leoflow`, the 58 ADRs, and the 54 connection recipes. Emits `link-map.csv` + `unmapped-links.csv`. |
| `build_connections_index.py` | Builds `connections/_index.md`: the converted provider-install explainer + matrix, plus a category-grouped quick-reference table. |
| `test_convert.py` | Unit tests for every transform in `convert.py`. |
| `link-map.csv` | `old docs path → new Hugo URL` for every migrated page + merge/split/landing. **Feeds the F4 redirect map.** |
| `unmapped-links.csv` | Internal doc links the converter could not map (currently 0). |

## Run

```bash
cd website/scripts/migration
python3 test_convert.py          # 13/13 must pass
python3 build_site.py            # regenerates the bulk pages + reports
python3 build_connections_index.py
```

The scripts locate the repo root automatically (they look for a directory holding
both `docs/` and `website/`), so they run from anywhere.

## What they do NOT touch

Hand-authored pages are owned by hand, not regenerated: the home (`_index.md`), all
section `_index.md` landings, the two merges (`concepts/editions.md`,
`contribute/local-dev-loop.md`), the split (`concepts/core-concepts.md` +
`reference/glossary.md`), and the Reference/CLI/Go/Python/Helm/GAP stubs.
