// Package connectors is the single source of truth for the curated Airflow
// connector types Leoflow knows about: the connection type, its display name,
// the Airflow hook class, the pip package that provides it (named as PyPI ships
// it), and which standard form fields to hide. Three consumers read this catalog
// so they never drift: the admin connection form (internal/api), the
// `connectors:` sugar expansion at compile, and compile-time dependency
// validation (ADR 0038).
package connectors

// Connector is one curated connector type.
type Connector struct {
	// Type is the Airflow conn_type, e.g. "postgres".
	Type string
	// DisplayName is the human label shown in the admin form.
	DisplayName string
	// HookClass is the dotted Airflow hook class, e.g.
	// "airflow.providers.postgres.hooks.postgres.PostgresHook".
	HookClass string
	// PipPackage is the PyPI package that provides the hook, e.g.
	// "apache-airflow-providers-postgres". Note the package boundary is not a
	// simple join of the dotted path (amazon.aws -> amazon; microsoft.mssql ->
	// microsoft-mssql), which is exactly why this is curated, not derived.
	PipPackage string
	// DefaultConnName is Airflow's conventional default connection id.
	DefaultConnName string
	// HiddenFields are the standard form fields hidden for this type.
	HiddenFields []string
	// Aliases are extra short names the `connectors:` sugar accepts for this
	// type, on top of Type. They exist purely to smooth Airflow's own
	// asymmetric conn_type vocabulary (e.g. "gcp" for the verbose
	// "google_cloud_platform", mirroring how "aws" is already terse). The
	// canonical Type always works; aliases never reach the admin form or the
	// AIRFLOW_CONN delivery, which speak Airflow's conn_type verbatim.
	Aliases []string
}

// catalog is the curated registry. Additive: new connector types append here.
var catalog = []Connector{
	{"postgres", "Postgres", "airflow.providers.postgres.hooks.postgres.PostgresHook", "apache-airflow-providers-postgres", "postgres_default", nil, nil},
	{"mysql", "MySQL", "airflow.providers.mysql.hooks.mysql.MySqlHook", "apache-airflow-providers-mysql", "mysql_default", nil, nil},
	{"sqlite", "SQLite", "airflow.providers.sqlite.hooks.sqlite.SqliteHook", "apache-airflow-providers-sqlite", "sqlite_default", []string{"login", "password", "port", "url_schema"}, nil},
	{"mssql", "Microsoft SQL Server", "airflow.providers.microsoft.mssql.hooks.mssql.MsSqlHook", "apache-airflow-providers-microsoft-mssql", "mssql_default", nil, nil},
	{"oracle", "Oracle", "airflow.providers.oracle.hooks.oracle.OracleHook", "apache-airflow-providers-oracle", "oracle_default", nil, nil},
	{"redis", "Redis", "airflow.providers.redis.hooks.redis.RedisHook", "apache-airflow-providers-redis", "redis_default", []string{"url_schema"}, nil},
	{"mongo", "MongoDB", "airflow.providers.mongo.hooks.mongo.MongoHook", "apache-airflow-providers-mongo", "mongo_default", nil, nil},
	{"http", "HTTP", "airflow.providers.http.hooks.http.HttpHook", "apache-airflow-providers-http", "http_default", nil, nil},
	{"aws", "Amazon Web Services", "airflow.providers.amazon.aws.hooks.base_aws.AwsBaseHook", "apache-airflow-providers-amazon", "aws_default", []string{"host", "port", "url_schema"}, nil},
	{"google_cloud_platform", "Google Cloud", "airflow.providers.google.cloud.hooks.cloud_base.GoogleBaseHook", "apache-airflow-providers-google", "google_cloud_default", []string{"host", "login", "password", "port", "url_schema"}, []string{"gcp", "google"}},
	{"snowflake", "Snowflake", "airflow.providers.snowflake.hooks.snowflake.SnowflakeHook", "apache-airflow-providers-snowflake", "snowflake_default", nil, nil},
	{"ssh", "SSH", "airflow.providers.ssh.hooks.ssh.SSHHook", "apache-airflow-providers-ssh", "ssh_default", []string{"url_schema"}, nil},
	{"ftp", "FTP", "airflow.providers.ftp.hooks.ftp.FTPHook", "apache-airflow-providers-ftp", "ftp_default", []string{"url_schema"}, nil},
	{"sftp", "SFTP", "airflow.providers.sftp.hooks.sftp.SFTPHook", "apache-airflow-providers-sftp", "sftp_default", []string{"url_schema"}, nil},
	{"kafka", "Apache Kafka", "airflow.providers.apache.kafka.hooks.base.KafkaBaseHook", "apache-airflow-providers-apache-kafka", "kafka_default", []string{"login", "password", "port", "url_schema"}, nil},
}

// Catalog returns the curated connector registry.
func Catalog() []Connector { return catalog }

// PackageFor returns the pip package for a connector name — its canonical Type
// or any of its sugar Aliases (e.g. "gcp" for "google_cloud_platform"). ok=false
// when the name is in neither, so the caller can fall back to dependencies:.
func PackageFor(name string) (pkg string, ok bool) {
	for _, c := range catalog {
		if c.Type == name {
			return c.PipPackage, true
		}
		for _, a := range c.Aliases {
			if a == name {
				return c.PipPackage, true
			}
		}
	}
	return "", false
}

// Resolve expands `connectors:` short names into their pip packages, preserving
// order. Unknown names are returned separately so the caller can fail compile
// with an actionable message ("unknown connector 'x'; known: …; or add the
// package to dependencies:") rather than letting a typo slip to a runtime
// ModuleNotFoundError. ADR 0038.
func Resolve(types []string) (packages, unknown []string) {
	for _, t := range types {
		if pkg, ok := PackageFor(t); ok {
			packages = append(packages, pkg)
		} else {
			unknown = append(unknown, t)
		}
	}
	return packages, unknown
}

// Types returns the known connector type names, for building error messages
// ("known: postgres, mysql, …").
func Types() []string {
	out := make([]string, len(catalog))
	for i, c := range catalog {
		out[i] = c.Type
	}
	return out
}
