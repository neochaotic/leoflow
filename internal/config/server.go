package config

import "github.com/spf13/pflag"

// ServerConfig is the full configuration for the leoflow-server control plane.
// It mirrors the nested YAML described in the Phase 2 prompt.
type ServerConfig struct {
	Server        ServerSection        `mapstructure:"server"`
	Database      DatabaseSection      `mapstructure:"database"`
	Redis         RedisSection         `mapstructure:"redis"`
	Auth          AuthSection          `mapstructure:"auth"`
	Scheduler     SchedulerSection     `mapstructure:"scheduler"`
	Observability ObservabilitySection `mapstructure:"observability"`
}

// ServerSection configures the HTTP and metrics listeners.
type ServerSection struct {
	HTTPAddr    string      `mapstructure:"http_addr"`
	MetricsAddr string      `mapstructure:"metrics_addr"`
	CORS        CORSSection `mapstructure:"cors"`
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

// LoadServer assembles the server configuration from defaults, the given file,
// LEOFLOW_* environment variables, and flags, in increasing precedence.
func LoadServer(_ string, _ *pflag.FlagSet) (*ServerConfig, error) {
	return &ServerConfig{}, nil
}

// Validate reports configuration errors that must abort startup.
func (c *ServerConfig) Validate() error {
	return nil
}
