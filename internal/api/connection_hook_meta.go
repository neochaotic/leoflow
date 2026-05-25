package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// The connection-type catalog the SPA's "Add/Edit Connection" form reads from
// GET /ui/connections/hook_meta. Each entry tells the form which standard fields
// to render (host/login/password/port/schema/description) and the type's display
// name — borrowed from Airflow's providers so the common connections work and the
// user just edits them. Without this the edit form renders empty (the form is
// entirely driven by this metadata).
//
// standard_fields keys mirror what the SPA consumes: description, host, login,
// password, port, and url_schema (the "Schema" field). Each value is a behavior
// object; { "hidden": true } drops the field for that type.

type hookMetaEntry struct {
	ConnectionType  string         `json:"connection_type"`
	HookName        string         `json:"hook_name"`
	HookClassName   string         `json:"hook_class_name"`
	DefaultConnName string         `json:"default_conn_name"`
	ExtraFields     map[string]any `json:"extra_fields"`
	StandardFields  map[string]any `json:"standard_fields"`
}

// stdFields builds the standard-field behavior map, hiding the named fields.
func stdFields(hidden ...string) map[string]any {
	h := make(map[string]bool, len(hidden))
	for _, f := range hidden {
		h[f] = true
	}
	out := make(map[string]any, 6)
	for _, f := range []string{"description", "host", "login", "password", "port", "url_schema"} {
		out[f] = map[string]any{"hidden": h[f]}
	}
	return out
}

// connectionTypeCatalog is the curated set of common connection types. New types
// are additive; each is a template the user edits. (A full provider catalog can
// follow; these cover the common cases.)
func connectionTypeCatalog() []hookMetaEntry {
	all := stdFields()
	return []hookMetaEntry{
		{"postgres", "Postgres", "airflow.providers.postgres.hooks.postgres.PostgresHook", "postgres_default", map[string]any{}, all},
		{"mysql", "MySQL", "airflow.providers.mysql.hooks.mysql.MySqlHook", "mysql_default", map[string]any{}, all},
		{"sqlite", "SQLite", "airflow.providers.sqlite.hooks.sqlite.SqliteHook", "sqlite_default", map[string]any{}, stdFields("login", "password", "port", "url_schema")},
		{"mssql", "Microsoft SQL Server", "airflow.providers.microsoft.mssql.hooks.mssql.MsSqlHook", "mssql_default", map[string]any{}, all},
		{"oracle", "Oracle", "airflow.providers.oracle.hooks.oracle.OracleHook", "oracle_default", map[string]any{}, all},
		{"redis", "Redis", "airflow.providers.redis.hooks.redis.RedisHook", "redis_default", map[string]any{}, stdFields("url_schema")},
		{"mongo", "MongoDB", "airflow.providers.mongo.hooks.mongo.MongoHook", "mongo_default", map[string]any{}, all},
		{"http", "HTTP", "airflow.providers.http.hooks.http.HttpHook", "http_default", map[string]any{}, all},
		{"aws", "Amazon Web Services", "airflow.providers.amazon.aws.hooks.base_aws.AwsBaseHook", "aws_default", map[string]any{}, stdFields("host", "port", "url_schema")},
		{"google_cloud_platform", "Google Cloud", "airflow.providers.google.cloud.hooks.cloud_base.GoogleBaseHook", "google_cloud_default", map[string]any{}, stdFields("host", "login", "password", "port", "url_schema")},
		{"snowflake", "Snowflake", "airflow.providers.snowflake.hooks.snowflake.SnowflakeHook", "snowflake_default", map[string]any{}, all},
		{"ssh", "SSH", "airflow.providers.ssh.hooks.ssh.SSHHook", "ssh_default", map[string]any{}, stdFields("url_schema")},
		{"ftp", "FTP", "airflow.providers.ftp.hooks.ftp.FTPHook", "ftp_default", map[string]any{}, stdFields("url_schema")},
		{"sftp", "SFTP", "airflow.providers.sftp.hooks.sftp.SFTPHook", "sftp_default", map[string]any{}, stdFields("url_schema")},
		{"kafka", "Apache Kafka", "airflow.providers.apache.kafka.hooks.base.KafkaBaseHook", "kafka_default", map[string]any{}, stdFields("login", "password", "port", "url_schema")},
	}
}

// connectionHookMetaHandler serves the connection-type catalog the form needs.
func connectionHookMetaHandler() gin.HandlerFunc {
	catalog := connectionTypeCatalog()
	return func(c *gin.Context) { c.JSON(http.StatusOK, catalog) }
}
