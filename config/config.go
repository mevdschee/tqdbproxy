package config

import (
	"os"

	"gopkg.in/ini.v1"
)

// Config holds the proxy configuration
type Config struct {
	MySQL    ProxyConfig
	Postgres ProxyConfig
}

// ProxyConfig holds configuration for a single protocol proxy
type ProxyConfig struct {
	Listen  string
	Backend string
}

// Load reads configuration from an INI file with environment variable overrides
func Load(path string) (*Config, error) {
	cfg, err := ini.Load(path)
	if err != nil {
		return nil, err
	}

	config := &Config{
		MySQL: ProxyConfig{
			Listen:  cfg.Section("mysql").Key("listen").MustString(":3307"),
			Backend: cfg.Section("mysql").Key("backend").MustString("127.0.0.1:3306"),
		},
		Postgres: ProxyConfig{
			Listen:  cfg.Section("postgres").Key("listen").MustString(":5433"),
			Backend: cfg.Section("postgres").Key("backend").MustString("127.0.0.1:5432"),
		},
	}

	// Environment variable overrides
	if v := os.Getenv("TQDBPROXY_MYSQL_LISTEN"); v != "" {
		config.MySQL.Listen = v
	}
	if v := os.Getenv("TQDBPROXY_MYSQL_BACKEND"); v != "" {
		config.MySQL.Backend = v
	}
	if v := os.Getenv("TQDBPROXY_POSTGRES_LISTEN"); v != "" {
		config.Postgres.Listen = v
	}
	if v := os.Getenv("TQDBPROXY_POSTGRES_BACKEND"); v != "" {
		config.Postgres.Backend = v
	}

	return config, nil
}
