# gcp_bigquery_query — a single BigQuery operator

The minimal "hello, BigQuery operator" for Leoflow's generic operator path (ADR 0040):
one `BigQueryInsertJobOperator` running a query against a BigQuery **public dataset**
(`bigquery-public-data.samples.shakespeare`), so it needs no data setup and stays in
the free tier.

## Set up

1. Edit `PROJECT` in `dag.py` to a project you can run BigQuery jobs in (the
   billing/execution project — the data itself is public).
2. Credentials come from the `google_cloud_platform` Connection / ADC — see
   [`examples/gcp_gcs_load`](../../gcp_gcs_load/) for the auth modes. `connectors: [gcp]`
   installs the provider.

## Run

```bash
# Lite (local): host ADC via `gcloud auth application-default login`
leoflow lite --executor=subprocess examples/gcp/bigquery_query
```

For multi-step BigQuery (dataset + chained queries + cleanup), see
[`examples/gcp/bigquery_chain`](../bigquery_chain/).
