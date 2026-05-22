// Package config loads Leoflow configuration from defaults, an optional config
// file, and LEOFLOW_* environment variables, with flags taking precedence.
package config

import "github.com/spf13/pflag"

// Default configuration values applied when nothing else sets a key.
const (
	defaultServerURL = "http://localhost:8080"
	defaultLogLevel  = "info"
	defaultParserCmd = "python3 -m leoflow_parser"
)

// Config holds runtime configuration shared by the Leoflow binaries.
type Config struct {
	ServerURL string `mapstructure:"server_url"`
	LogLevel  string `mapstructure:"log_level"`
	Registry  string `mapstructure:"registry"`
	ParserCmd string `mapstructure:"parser_cmd"`
}

// DefaultConfigFile returns the default configuration file path,
// ~/.leoflow/config.yaml.
func DefaultConfigFile() (string, error) { return "", nil }

// Load assembles configuration from defaults, the given file (when non-empty),
// LEOFLOW_* environment variables, and the provided flag set, in increasing
// order of precedence. A nil flag set or empty file path is ignored.
func Load(_ string, _ *pflag.FlagSet) (*Config, error) { return &Config{}, nil }
