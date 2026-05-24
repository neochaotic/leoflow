package config

import (
	"errors"
	"fmt"
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
	// AgentControlPlaneAddr is the gRPC address task pods dial back to. Empty
	// falls back to server.grpc_addr; in a local k3d/kind cluster set it to a
	// host-reachable address such as host.k3d.internal:9091.
	AgentControlPlaneAddr string `mapstructure:"agent_control_plane_addr"`
	// AgentTLSCAConfigMap names a ConfigMap (key ca.crt) mounted into task pods so
	// the agent verifies the control plane's gRPC TLS cert (issue #58). Empty =
	// agents use the insecure channel (dev).
	AgentTLSCAConfigMap string `mapstructure:"agent_tls_ca_configmap"`
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
	"scheduler.loop_interval_ms":                1000,
	"scheduler.enabled":                         true,
	"executor.http.inline_max_duration_seconds": 300,
	"executor.http.inline_concurrency_limit":    256,
	"executor.http.user_agent":                  "leoflow/0.1",
	"executor.agent_control_plane_addr":         "",
	"executor.agent_tls_ca_configmap":           "",
	"logs.dir":                                  "/var/log/leoflow",
	"observability.otel.enabled":                true,
	"observability.otel.endpoint":               "localhost:4317",
	"observability.log_level":                   "info",
	"observability.log_format":                  "json",
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
	return nil
}
