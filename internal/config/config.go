// Package config loads Leoflow configuration from defaults, an optional config
// file, and LEOFLOW_* environment variables, with flags taking precedence.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Default configuration values applied when nothing else sets a key.
const (
	defaultServerURL = "http://localhost:8080"
	defaultLogLevel  = "info"
	defaultParserCmd = "python3 -m leoflow_parser"
)

// flagToKey maps CLI flag names (kebab-case) to configuration keys (snake_case)
// so that a changed flag overrides the corresponding env var and file value.
var flagToKey = map[string]string{
	"server-url": "server_url",
	"log-level":  "log_level",
	"registry":   "registry",
	"parser-cmd": "parser_cmd",
}

// Config holds runtime configuration shared by the Leoflow binaries.
type Config struct {
	ServerURL string `mapstructure:"server_url"`
	LogLevel  string `mapstructure:"log_level"`
	Registry  string `mapstructure:"registry"`
	ParserCmd string `mapstructure:"parser_cmd"`
}

// DefaultConfigFile returns the default configuration file path,
// ~/.leoflow/config.yaml.
func DefaultConfigFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".leoflow", "config.yaml"), nil
}

// Load assembles configuration from defaults, the given file (when non-empty),
// LEOFLOW_* environment variables, and the provided flag set, in increasing
// order of precedence. A nil flag set or empty file path is ignored.
func Load(configFile string, flags *pflag.FlagSet) (*Config, error) {
	v := viper.New()
	v.SetDefault("server_url", defaultServerURL)
	v.SetDefault("log_level", defaultLogLevel)
	v.SetDefault("parser_cmd", defaultParserCmd)
	v.SetDefault("registry", "")

	v.SetEnvPrefix("LEOFLOW")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()

	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", configFile, err)
		}
	}

	if flags != nil {
		for flagName, key := range flagToKey {
			if f := flags.Lookup(flagName); f != nil {
				if err := v.BindPFlag(key, f); err != nil {
					return nil, fmt.Errorf("binding flag %q: %w", flagName, err)
				}
			}
		}
	}

	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}
	return &c, nil
}
