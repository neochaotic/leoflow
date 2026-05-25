"""api_chain — chain two public API calls: list users, then fetch one's details."""
from __future__ import annotations

from airflow.sdk import DAG, task

BASE = "https://jsonplaceholder.typicode.com"


@task
def pick_user() -> int:
    import requests

    users = requests.get(f"{BASE}/users", timeout=30).json()
    chosen = users[0]["id"]
    print(f"pick_user: {len(users)} users, chose id={chosen}")
    return chosen


@task
def fetch_detail(user_id: int) -> dict:
    import requests

    user = requests.get(f"{BASE}/users/{user_id}", timeout=30).json()
    posts = requests.get(f"{BASE}/posts", params={"userId": user_id}, timeout=30).json()
    out = {"name": user["name"], "company": user["company"]["name"], "posts": len(posts)}
    print(f"fetch_detail: {out}")
    return out


with DAG("api_chain", schedule=None, catchup=False, tags=["example"]):
    fetch_detail(pick_user())
