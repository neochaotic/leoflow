---
connectors: [github]
---

# GitHub connection

Connect a task to the GitHub API to read repos, manage issues/PRs, or
trigger workflows over a managed Leoflow Connection. `GithubHook`
authenticates with a personal access token (PAT).

## URI shape

```
github://:<personal_access_token>@
```

There is **no host** — the PAT (token-only shape, like Slack) lives in
Password and is percent-escaped. GitHub Enterprise hosts can be supplied
via `Extra`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `github_default`. Exported as `AIRFLOW_CONN_GITHUB_DEFAULT`. |
| Conn Type | yes | `github`. |
| Password | yes | The personal access token, e.g. `ghp_...`. Encrypted at rest (ADR 0019). |
| Host | no | Leave blank for github.com. |
| Extra | optional | JSON: `{"base_url":"https://ghe.example.com/api/v3"}` for GitHub Enterprise. |

## Example DAG

```python
from airflow.sdk import dag, task


@dag(schedule=None, catchup=False, tags=["github"])
def github_repo():
    @task
    def stars():
        from airflow.providers.github.hooks.github import GithubHook

        hook = GithubHook(github_conn_id="github_default")
        client = hook.get_conn()
        repo = client.get_repo("apache/airflow")
        print("stars:", repo.stargazers_count)

    stars()


github_repo()
```

```yaml
# leoflow.yaml
dag_id: github_repo
runtime: python3.12
requirements:
  - apache-airflow-providers-github
connections:
  - conn_id: github_default
    conn_type: github
```

## Security notes

- **Fine-grained PATs**: prefer fine-grained tokens scoped to specific
  repos and permissions over classic PATs.
- **Short expiry + rotation**: set a token expiry and rotate via the
  Connection; revoke immediately on leak.
- Never log `AIRFLOW_CONN_GITHUB_DEFAULT`; it carries the PAT.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestGithubConnectionURIShapeIntegration` — chain-of-custody delivery test.
- [Slack connection](slack.md) — the same token-in-password shape.
