import requests
import json
import sys

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

endpoints = [
    "/ui/auth/me",
    "/ui/auth/menus",
    "/ui/config",
    "/ui/dags",
    "/api/v2/monitor/health"
]

for ep in endpoints:
    print(f"--- Endpoint: {ep} ---")
    ra = requests.get(f"http://localhost:8081{ep}", headers=headers_a)
    rl = requests.get(f"http://localhost:8080{ep}", headers=headers_l)
    print("Airflow:", ra.status_code)
    print("Leoflow:", rl.status_code)
    with open(f"airflow_{ep.replace('/', '_')}.json", "w") as f:
        f.write(ra.text)
    with open(f"leoflow_{ep.replace('/', '_')}.json", "w") as f:
        f.write(rl.text)

dag_endpoints_airflow = [
    ("/ui/dags/{id}/latest_run", "tutorial"),
    ("/ui/grid/structure/{id}", "tutorial"),
    ("/ui/grid/runs/{id}", "tutorial"),
    ("/ui/grid/ti_summaries/{id}", "tutorial"),
    ("/ui/structure/structure_data?dag_id={id}", "tutorial")
]

dag_endpoints_leoflow = [
    ("/ui/dags/{id}/latest_run", "demo_http_chain"),
    ("/ui/grid/structure/{id}", "demo_http_chain"),
    ("/ui/grid/runs/{id}", "demo_http_chain"),
    ("/ui/grid/ti_summaries/{id}", "demo_http_chain"),
    ("/ui/structure/structure_data?dag_id={id}", "demo_http_chain")
]

for ea, el in zip(dag_endpoints_airflow, dag_endpoints_leoflow):
    pa = ea[0].format(id=ea[1])
    pl = el[0].format(id=el[1])
    print(f"--- Endpoint: {pa} / {pl} ---")
    ra = requests.get(f"http://localhost:8081{pa}", headers=headers_a)
    rl = requests.get(f"http://localhost:8080{pl}", headers=headers_l)
    print("Airflow:", ra.status_code)
    print("Leoflow:", rl.status_code)
    name = pa.split("?")[0].replace("/", "_")
    with open(f"airflow_{name}.json", "w") as f:
        f.write(ra.text)
    with open(f"leoflow_{name}.json", "w") as f:
        f.write(rl.text)
