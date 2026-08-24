#!/usr/bin/env python3
"""Build content/connections/_index.md: the converted provider-install explainer +
matrix from docs/connections/index.md, with a category-grouped quick-reference table
(Databases / Cloud / Messaging / BI & SaaS / Files) inserted on top of the matrix."""
import os
import re
import iamap
import convert as C

_HERE = os.path.dirname(os.path.abspath(__file__))


def _find_root(start):
    d = start
    while d != os.path.dirname(d):
        if os.path.isdir(os.path.join(d, "docs")) and os.path.isdir(os.path.join(d, "website")):
            return d
        d = os.path.dirname(d)
    raise SystemExit("could not locate repo root (needs docs/ + website/)")


_ROOT = _find_root(_HERE)
DOCS = os.path.join(_ROOT, "docs")
CONTENT = os.path.join(_ROOT, "website", "content")
url_index = iamap.build_url_index()
un = []

CATEGORY = {
    # Databases & query engines
    "postgres": "Databases", "mysql": "Databases", "mssql": "Databases",
    "sqlite": "Databases", "oracle": "Databases", "mongo": "Databases",
    "redis": "Databases", "cassandra": "Databases", "neo4j": "Databases",
    "vertica": "Databases", "influxdb": "Databases", "druid": "Databases",
    "pinot": "Databases", "trino": "Databases", "presto": "Databases",
    "jdbc": "Databases", "elasticsearch": "Databases", "hiveserver2": "Databases",
    "hive_cli": "Databases",
    # Cloud platforms, warehouses & compute
    "aws": "Cloud", "azure": "Cloud", "google_cloud_platform": "Cloud",
    "snowflake": "Cloud", "databricks": "Cloud", "redshift": "Cloud",
    "athena": "Cloud", "emr": "Cloud", "gcpbigquery": "Cloud",
    "gcpcloudsql": "Cloud", "gcpssh": "Cloud", "spark": "Cloud",
    "kafka": "Messaging", "livy": "Cloud", "docker": "Cloud", "http": "Cloud",
    # Messaging, alerting & mail
    "slack": "Messaging", "telegram": "Messaging", "discord": "Messaging",
    "pagerduty": "Messaging", "opsgenie": "Messaging", "smtp": "Messaging",
    "imap": "Messaging",
    # BI & SaaS
    "tableau": "BI & SaaS", "powerbi": "BI & SaaS", "gcp_looker": "BI & SaaS",
    "salesforce": "BI & SaaS", "datadog": "BI & SaaS", "github": "BI & SaaS",
    "google_ads": "BI & SaaS", "zendesk": "BI & SaaS", "dbt_cloud": "BI & SaaS",
    "msgraph": "BI & SaaS", "google_cloud_platform_looker": "BI & SaaS",
    # Files & transfer
    "file-transfer": "Files", "samba": "Files",
}
CAT_ORDER = ["Databases", "Cloud", "Messaging", "BI & SaaS", "Files"]


def parse_matrix(body):
    """Pull (slug, conn_type, provider) from the existing 'Connector matrix' table."""
    rows = []
    for m in re.finditer(r"^\|\s*\[([a-z0-9_\-]+)\]\([^)]+\)\s*\|\s*(.+?)\s*\|\s*(`[^|]+`)\s*\|", body, re.M):
        slug, conn_type, provider = m.group(1), m.group(2).strip(), m.group(3).strip()
        rows.append((slug, conn_type, provider))
    return rows


def category_table(rows):
    out = ["## Connectors by category", "",
           "Every documented connector grouped by domain — its `conn_type`, the provider "
           "package the `connectors:` sugar expands to, and its category. Click a name for "
           "the full recipe (URI shape, example DAG, how to test).", ""]
    bycat = {c: [] for c in CAT_ORDER}
    for slug, ct, prov in rows:
        cat = CATEGORY.get(slug, "Cloud")
        bycat[cat].append((slug, ct, prov))
    for cat in CAT_ORDER:
        entries = sorted(bycat[cat])
        if not entries:
            continue
        out.append(f"### {cat}")
        out.append("")
        out.append("| Connector | `conn_type` | Provider package | Category |")
        out.append("|---|---|---|---|")
        for slug, ct, prov in entries:
            out.append(f"| [{slug}](/connections/{slug}/) | {ct} | {prov} | {cat} |")
        out.append("")
    return "\n".join(out)


def main():
    text = open(os.path.join(DOCS, "connections/index.md")).read()
    _, body = C.strip_frontmatter(text)
    _, body = C.strip_h1(body)
    rows = parse_matrix(body)
    body = C.convert_body(body, "connections/index.md", url_index, un, {}, iamap.ANCHOR_REDIRECTS)

    cat_md = category_table(rows)
    # Insert the category table right before the existing "## Connector matrix"
    marker = "## Connector matrix"
    idx = body.find(marker)
    if idx != -1:
        body = body[:idx] + cat_md + "\n" + body[idx:]
    else:
        body = body + "\n\n" + cat_md

    front = C.inject_frontmatter(
        "Connections", "Connections", 30,
        "The connector matrix: install a provider, then wire a Connection. "
        "54 recipes grouped by category, each with its conn_id and conn_type.",
        {"cascade": "{ type: docs }", "menu": "{ main: { weight: 30 } }"},
    )
    open(os.path.join(CONTENT, "connections/_index.md"), "w").write(front + "\n" + body.lstrip("\n"))
    print(f"wrote connections/_index.md — parsed {len(rows)} matrix rows; unmapped {len(un)}")


if __name__ == "__main__":
    main()
