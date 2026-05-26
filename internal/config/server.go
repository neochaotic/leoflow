package config

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// ServerConfig is the full configuration for the leoflow-server control plane.
// It mirrors the nested YAML described in the Phase 2 prompt.
type ServerConfig struct {
	Server        ServerSection        `mapstructure:"server"`
	Database      DatabaseSection      `mapstructure:"database"`
	Redis         RedisSection         `mapstructure:"redis"`
	Auth          AuthSection          `mapstructure:"auth"`
	Scheduler     SchedulerSection     `mapstructure:"scheduler"`
	Executor      ExecutorSection      `mapstructure:"executor"`
	Logs          LogsSection          `mapstructure:"logs"`
	Observability ObservabilitySection `mapstructure:"observability"`
	UI            UISection            `mapstructure:"ui"`
	// SecretKey (LEOFLOW_SECRET_KEY) is the 32-byte key encrypting connection
	// secrets at rest (ADR 0019). Raw 32 chars, 64-char hex, or base64. Empty
	// disables connection writes.
	SecretKey string `mapstructure:"secret_key"`
}

// LogsSection configures task log shipping.
type LogsSection struct {
	// Dir is the root directory for the disk log sink.
	Dir string `mapstructure:"dir"`
}

// ExecutorSection configures how tasks are executed.
type ExecutorSection struct {
	HTTP HTTPExecutorSection `mapstructure:"http"`
	// Type selects the pod-path executor: "kubernetes" (default, pod-per-task) or
	// "subprocess" (dev only, runs the agent on the host without isolation, used
	// by `leoflow dev`).
	Type string `mapstructure:"type"`
	// AgentPath is the leoflow-agent binary the subprocess executor runs (dev only).
	AgentPath string `mapstructure:"agent_path"`
	// SubprocessWorkDir is the working directory the subprocess executor runs the
	// agent in, so it can import the project's dag.py (dev only). Empty keeps the
	// server's working directory.
	SubprocessWorkDir string `mapstructure:"subprocess_workdir"`
	// AgentControlPlaneAddr is the gRPC address task pods dial back to. Empty
	// falls back to server.grpc_addr; in a local k3d/kind cluster set it to a
	// host-reachable address such as host.k3d.internal:9091.
	AgentControlPlaneAddr string `mapstructure:"agent_control_plane_addr"`
	// AgentTLSCAConfigMap names a ConfigMap (key ca.crt) mounted into task pods so
	// the agent verifies the control plane's gRPC TLS cert (issue #58). Empty =
	// agents use the insecure channel (dev).
	AgentTLSCAConfigMap string `mapstructure:"agent_tls_ca_configmap"`
	// Defaults holds per-cluster task defaults applied at dispatch to fill gaps the
	// DAG artifact left empty (ADR 0023, layer L0). They never override a value
	// baked into dag.json, keeping the artifact portable across clusters.
	Defaults PlatformDefaultsSection `mapstructure:"defaults"`
}

// PlatformDefaultsSection configures the lowest-precedence (L0) task defaults,
// applied at dispatch to fill gaps the DAG left empty (ADR 0023).
type PlatformDefaultsSection struct {
	// StagingSize/StagingStorageClass default the per-run staging volume when the
	// DAG enabled staging without pinning them (e.g. the cluster's RWX class).
	StagingSize         string `mapstructure:"staging_size"`
	StagingStorageClass string `mapstructure:"staging_storage_class"`
	// StagingAccessMode is the PVC access mode for the staging volume. Defaults to
	// ReadWriteMany (multi-node prod); single-node dev (k3d local-path, no RWX)
	// sets ReadWriteOnce, which is sufficient for a run's sequential same-node pods.
	StagingAccessMode string `mapstructure:"staging_access_mode"`
	// ResourcesCPU/ResourcesMemory default a task's request when neither the task
	// override nor the DAG set any (Kubernetes quantities, e.g. "250m"/"256Mi").
	ResourcesCPU    string `mapstructure:"resources_cpu"`
	ResourcesMemory string `mapstructure:"resources_memory"`
}

// UISection configures the embedded Airflow UI.
type UISection struct {
	// InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
	// falls back to "Leoflow"; `leoflow lite` sets it to mark the environment.
	InstanceName string `mapstructure:"instance_name"`
	// Edition marks the running edition; "lite" shows the LITE badge in the UI
	// shell (independent of the auth mode). Empty/"production" shows no badge.
	Edition string `mapstructure:"edition"`
	// Workspace is the DAG project directory the Lite web editor edits (ADR 0025).
	// Empty disables the editor (Production, or Lite without one).
	Workspace string `mapstructure:"workspace"`
	// MonacoDir is where the pinned Monaco bundle was fetched by `leoflow setup`;
	// the editor page is served Monaco from it. Empty shows a setup hint.
	MonacoDir string `mapstructure:"monaco_dir"`
}

// HTTPExecutorSection configures the inline http_api execution path (ADR 0002).
type HTTPExecutorSection struct {
	// InlineMaxDurationSeconds caps how long an inline http_api task may run; a
	// task declaring a longer execution_timeout_seconds must use execution_mode: pod.
	InlineMaxDurationSeconds int `mapstructure:"inline_max_duration_seconds"`
	// InlineConcurrencyLimit bounds the number of in-flight inline goroutines.
	InlineConcurrencyLimit int `mapstructure:"inline_concurrency_limit"`
	// UserAgent is the User-Agent header sent on inline http_api requests.
	UserAgent string `mapstructure:"user_agent"`
}

// ServerSection configures the HTTP, metrics, and agent gRPC listeners.
type ServerSection struct {
	HTTPAddr    string      `mapstructure:"http_addr"`
	MetricsAddr string      `mapstructure:"metrics_addr"`
	GRPCAddr    string      `mapstructure:"grpc_addr"`
	CORS        CORSSection `mapstructure:"cors"`
	// GRPCTLSCert/GRPCTLSKey enable TLS on the agent gRPC listener (issue #58).
	// When both are set the channel is encrypted; empty means plaintext (dev).
	GRPCTLSCert string `mapstructure:"grpc_tls_cert"`
	GRPCTLSKey  string `mapstructure:"grpc_tls_key"`
}

// CORSSection configures cross-origin access.
type CORSSection struct {
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// DatabaseSection configures the Postgres connection pool.
type DatabaseSection struct {
	URL          string `mapstructure:"url"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
}

// RedisSection configures the Redis connection.
type RedisSection struct {
	URL string `mapstructure:"url"`
}

// AuthSection configures authentication.
type AuthSection struct {
	Provider string     `mapstructure:"provider"`
	JWT      JWTSection `mapstructure:"jwt"`
	// DevNoAuth disables authentication entirely, treating every request as an
	// admin. It exists ONLY for `leoflow dev` (local, unsandboxed). It is false by
	// default and the server logs a prominent warning when it is on. NEVER set
	// this in production (LEOFLOW_AUTH_DEV_NO_AUTH).
	DevNoAuth bool `mapstructure:"dev_no_auth"`
	// LoginRateLimitPerMinute caps failed /auth/token attempts per client IP per
	// minute (anti-brute-force). Only failures count, so a successful login never
	// consumes budget. Lite raises this well above the production default because
	// it is a local single-user tool where lockouts are pure friction.
	LoginRateLimitPerMinute int `mapstructure:"login_rate_limit_per_minute"`
}

// JWTSection configures JWT issuance and validation.
type JWTSection struct {
	Secret          string `mapstructure:"secret"`
	TokenTTLSeconds int    `mapstructure:"token_ttl_seconds"`
}

// SchedulerSection configures the scheduler loop.
type SchedulerSection struct {
	LoopIntervalMS int  `mapstructure:"loop_interval_ms"`
	Enabled        bool `mapstructure:"enabled"`
}

// ObservabilitySection configures logging, metrics, and tracing.
type ObservabilitySection struct {
	OTel      OTelSection `mapstructure:"otel"`
	LogLevel  string      `mapstructure:"log_level"`
	LogFormat string      `mapstructure:"log_format"`
}

// OTelSection configures OpenTelemetry export.
type OTelSection struct {
	Enabled  bool   `mapstructure:"enabled"`
	Endpoint string `mapstructure:"endpoint"`
}

// serverDefaults lists every leaf key with its default so that AutomaticEnv and
// Unmarshal resolve nested keys correctly.
var serverDefaults = map[string]any{
	"server.http_addr":                          "0.0.0.0:8080",
	"server.metrics_addr":                       "0.0.0.0:9090",
	"server.grpc_addr":                          "0.0.0.0:9091",
	"server.grpc_tls_cert":                      "",
	"server.grpc_tls_key":                       "",
	"server.cors.allowed_origins":               []string{"http://localhost:8080"},
	"database.url":                              "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable",
	"database.max_open_conns":                   25,
	"database.max_idle_conns":                   5,
	"redis.url":                                 "redis://localhost:6379/0",
	"auth.provider":                             "jwt",
	"auth.jwt.secret":                           "",
	"auth.jwt.token_ttl_seconds":                3600,
	"auth.login_rate_limit_per_minute":          5,
	"scheduler.loop_interval_ms":                1000,
	"scheduler.enabled":                         true,
	"executor.http.inline_max_duration_seconds": 300,
	"executor.http.inline_concurrency_limit":    256,
	"executor.http.user_agent":                  "leoflow/0.1",
	"executor.type":                             "kubernetes",
	"executor.agent_path":                       "leoflow-agent",
	"executor.subprocess_workdir":               "",
	"executor.agent_control_plane_addr":         "",
	"executor.agent_tls_ca_configmap":           "",
	"executor.defaults.staging_access_mode":     "ReadWriteMany",
	"logs.dir":                                  "/var/log/leoflow",
	"observability.otel.enabled":                true,
	"observability.otel.endpoint":               "localhost:4317",
	"observability.log_level":                   "info",
	"observability.log_format":                  "json",
	"ui.instance_name":                          "Leoflow",
	"ui.edition":                                "",
	"ui.workspace":                              "",
	"ui.monaco_dir":                             "",
	"auth.dev_no_auth":                          false,
	"secret_key":                                "",
}

// LoadServer assembles the server configuration from defaults, the given file,
// LEOFLOW_* environment variables, and flags, in increasing precedence.
func LoadServer(configFile string, flags *pflag.FlagSet) (*ServerConfig, error) {
	v := viper.New()
	for key, val := range serverDefaults {
		v.SetDefault(key, val)
	}
	v.SetEnvPrefix("LEOFLOW")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", configFile, err)
		}
	}
	if flags != nil {
		if err := v.BindPFlags(flags); err != nil {
			return nil, fmt.Errorf("binding flags: %w", err)
		}
	}

	var c ServerConfig
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshaling server config: %w", err)
	}
	return &c, nil
}

// Validate reports configuration errors that must abort startup.
func (c *ServerConfig) Validate() error {
	if c.Auth.Provider == "jwt" && c.Auth.JWT.Secret == "" {
		return errors.New("auth.jwt.secret is required (set LEOFLOW_AUTH_JWT_SECRET)")
	}
	// auth.dev_no_auth disables authentication entirely; permit it only when the
	// HTTP API binds to loopback, so a misconfigured (or accidental) dev bypass can
	// never expose an unauthenticated API off-host. Fail closed otherwise.
	if c.Auth.DevNoAuth && !isLoopbackListenAddr(c.Server.HTTPAddr) {
		return fmt.Errorf("auth.dev_no_auth disables authentication and is only permitted on a loopback http_addr (got %q); never enable it in production", c.Server.HTTPAddr)
	}
	return nil
}

// isLoopbackListenAddr reports whether a listen address binds only to loopback,
// so a no-auth dev server is unreachable off-host.
func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
