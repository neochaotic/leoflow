import requests
import json
import sys
import os

def get_airflow_token():
    r = requests.post("http://localhost:8081/auth/token", json={"username": "admin", "password": "4C4GRSKwyt99SEDH"})
    return r.json().get("access_token")

def get_leoflow_token():
    r = requests.post("http://localhost:8080/auth/token", json={"username": "admin@leoflow.local", "password": "admin"})
    return r.json().get("access_token")

airflow_token = get_airflow_token()
leoflow_token = get_leoflow_token()

headers_a = {"Authorization": f"Bearer {airflow_token}"}
headers_l = {"Authorization": f"Bearer {leoflow_token}"}

outdir = "audit_data"
os.makedirs(outdir, exist_ok=True)

# Comprehensive endpoint list
endpoints = {
    # Auth & Config
    "ui_auth_me": "/ui/auth/me",
    "ui_auth_menus": "/ui/auth/menus",
    "ui_config": "/ui/config",

    # Dashboard
    "ui_dashboard_dag_stats": "/ui/dashboard/dag_stats",
    "ui_dashboard_historical": "/ui/dashboard/historical_metrics_data?start_date=2026-05-01T00:00:00Z&end_date=2026-05-23T23:59:59Z",

    # DAGs list
    "ui_dags": "/ui/dags",

    # Public API v2
    "api_v2_version": "/api/v2/version",
    "api_v2_health": "/api/v2/monitor/health",
    "api_v2_dags": "/api/v2/dags",
    "api_v2_dag_tags": "/api/v2/dagTags",
    "api_v2_dag_warnings": "/api/v2/dagWarnings",
    "api_v2_import_errors": "/api/v2/importErrors",
    "api_v2_event_logs": "/api/v2/eventLogs",
    "api_v2_variables": "/api/v2/variables",
    "api_v2_connections": "/api/v2/connections",
    "api_v2_pools": "/api/v2/pools",
    "api_v2_providers": "/api/v2/providers",
    "api_v2_plugins": "/api/v2/plugins",
    "api_v2_config": "/api/v2/config",
    "api_v2_assets": "/api/v2/assets",
    "api_v2_assets_events": "/api/v2/assets/events",

    # Degraded UI endpoints
    "ui_dependencies": "/ui/dependencies",
    "ui_backfills": "/ui/backfills",
    "ui_teams": "/ui/teams",
    "ui_connections_hook_meta": "/ui/connections/hook_meta",
}

results = {}

for name, ep in endpoints.items():
    print(f"--- {name}: {ep} ---")
    try:
        ra = requests.get(f"http://localhost:8081{ep}", headers=headers_a, timeout=10)
    except Exception as e:
        ra = None
        print(f"  Airflow ERROR: {e}")
    try:
        rl = requests.get(f"http://localhost:8080{ep}", headers=headers_l, timeout=10)
    except Exception as e:
        rl = None
        print(f"  Leoflow ERROR: {e}")

    a_status = ra.status_code if ra else "ERROR"
    l_status = rl.status_code if rl else "ERROR"
    print(f"  Airflow: {a_status}, Leoflow: {l_status}")

    results[name] = {
        "endpoint": ep,
        "airflow_status": a_status,
        "leoflow_status": l_status,
    }

    if ra:
        with open(f"{outdir}/airflow_{name}.json", "w") as f:
            f.write(ra.text)
    if rl:
        with open(f"{outdir}/leoflow_{name}.json", "w") as f:
            f.write(rl.text)

# DAG-specific endpoints (Airflow uses tutorial, Leoflow uses demo_http_chain)
dag_specific = {
    "ui_dag_latest_run": ("/ui/dags/{id}/latest_run", "tutorial", "demo_http_chain"),
    "ui_grid_structure": ("/ui/grid/structure/{id}", "tutorial", "demo_http_chain"),
    "ui_grid_runs": ("/ui/grid/runs/{id}", "tutorial", "demo_http_chain"),
    "ui_grid_ti_summaries": ("/ui/grid/ti_summaries/{id}", "tutorial", "demo_http_chain"),
    "ui_structure_data": ("/ui/structure/structure_data?dag_id={id}", "tutorial", "demo_http_chain"),
    "ui_calendar": ("/ui/calendar/{id}", "tutorial", "demo_http_chain"),
    "api_v2_dag_details": ("/api/v2/dags/{id}/details", "tutorial", "demo_http_chain"),
    "api_v2_dag_runs": ("/api/v2/dags/{id}/dagRuns", "tutorial", "demo_http_chain"),
    "api_v2_dag_source": ("/api/v2/dagSources/{id}", "tutorial", "demo_http_chain"),
}

for name, (ep, a_id, l_id) in dag_specific.items():
    a_ep = ep.format(id=a_id)
    l_ep = ep.format(id=l_id)
    print(f"--- {name}: {a_ep} / {l_ep} ---")
    try:
        ra = requests.get(f"http://localhost:8081{a_ep}", headers=headers_a, timeout=10)
    except Exception as e:
        ra = None
    try:
        rl = requests.get(f"http://localhost:8080{l_ep}", headers=headers_l, timeout=10)
    except Exception as e:
        rl = None

    a_status = ra.status_code if ra else "ERROR"
    l_status = rl.status_code if rl else "ERROR"
    print(f"  Airflow: {a_status}, Leoflow: {l_status}")
    results[name] = {
        "endpoint": ep,
        "airflow_status": a_status,
        "leoflow_status": l_status,
    }
    if ra:
        with open(f"{outdir}/airflow_{name}.json", "w") as f:
            f.write(ra.text)
    if rl:
        with open(f"{outdir}/leoflow_{name}.json", "w") as f:
            f.write(rl.text)

# Summary
with open(f"{outdir}/summary.json", "w") as f:
    json.dump(results, f, indent=2)

print("\n=== SUMMARY ===")
for name, r in results.items():
    match = "✅" if r["airflow_status"] == r["leoflow_status"] else "❌"
    print(f"{match} {name}: Airflow={r['airflow_status']} Leoflow={r['leoflow_status']}  ({r['endpoint']})")
