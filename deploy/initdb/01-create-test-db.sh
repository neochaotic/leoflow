#!/bin/sh
# Provision the isolated integration-test database alongside the main one.
#
# Postgres' official image only honors a single POSTGRES_DB; this init hook
# (run once, on an empty data volume) adds `leoflow_test` so local integration
# tests never write to the `leoflow` database that backs the dev control plane.
# For an existing volume the hook does not re-run — use `make test-db` instead.
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<-SQL
	SELECT 'CREATE DATABASE leoflow_test'
	WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'leoflow_test')\gexec
SQL
