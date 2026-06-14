"""gcp_bigquery_query — a single real BigQuery operator through Leoflow's generic
operator path (ADR 0040), credentials from the google_cloud_platform connector.

Runs one query with BigQueryInsertJobOperator against a BigQuery public dataset
(no setup, free-tier). Set PROJECT to a project you can run BigQuery jobs in
(billing/execution project); credentials come from the google_cloud_platform
Connection / ADC and ``connectors: [gcp]`` installs the provider.
"""
from __future__ import annotations

from airflow.providers.google.cloud.operators.bigquery import BigQueryInsertJobOperator
from airflow.sdk import DAG

# --- set this for your environment (the project BigQuery jobs run/bill in) ---
PROJECT = "your-gcp-project"
LOCATION = "US"

QUERY = (
    "SELECT word, word_count "
    "FROM `bigquery-public-data.samples.shakespeare` "
    "WHERE corpus = 'hamlet' "
    "ORDER BY word_count DESC LIMIT 10"
)

with DAG("gcp_bigquery_query", schedule=None, catchup=False, tags=["example"]):
    BigQueryInsertJobOperator(
        task_id="query",
        configuration={"query": {"query": QUERY, "useLegacySql": False}},
        project_id=PROJECT,
        location=LOCATION,
    )
