"""http_jsonplaceholder — call a public JSON API (no key) and summarize it."""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def fetch() -> list[dict]:
    import requests

    r = requests.get("https://jsonplaceholder.typicode.com/posts", timeout=30)
    r.raise_for_status()
    posts = r.json()
    print(f"fetch: {len(posts)} posts from jsonplaceholder")
    return posts


@task
def summarize(posts: list[dict]) -> dict:
    by_user: dict[int, int] = {}
    for p in posts:
        by_user[p["userId"]] = by_user.get(p["userId"], 0) + 1
    top = max(by_user, key=by_user.get)
    out = {"posts": len(posts), "users": len(by_user), "top_user": top, "top_count": by_user[top]}
    print(f"summarize: {out}")
    return out


with DAG("http_jsonplaceholder", schedule=None, catchup=False, tags=["example"]):
    summarize(fetch())
