package config

import (
	"os"
	"strconv"

	"gopkg.in/ini.v1"
)

// Config holds the proxy configuration
type Config struct {
	MySQL    ProxyConfig
	Postgres ProxyConfig
}

// ProxyConfig holds configuration for a single protocol proxy
type ProxyConfig struct {
	Listen   string
	Backend  string   // Deprecated: use Primary instead
	Primary  string   // Primary database address
	Replicas []string // Read replica addresses
}

// Load reads configuration from an INI file with environment variable overrides
func Load(path string) (*Config, error) {
	cfg, err := ini.Load(path)
	if err != nil {
		return nil, err
	}

	config := &Config{
		MySQL:    loadProxyConfig(cfg, "mysql", ":3307", "127.0.0.1:3306"),
		Postgres: loadProxyConfig(cfg, "postgres", ":5433", "127.0.0.1:5432"),
	}

	// Environment variable overrides for MySQL
	if v := os.Getenv("TQDBPROXY_MYSQL_LISTEN"); v != "" {
		config.MySQL.Listen = v
	}
	if v := os.Getenv("TQDBPROXY_MYSQL_BACKEND"); v != "" {
		config.MySQL.Backend = v
		config.MySQL.Primary = v // Also set primary for backward compatibility
	}
	if v := os.Getenv("TQDBPROXY_MYSQL_PRIMARY"); v != "" {
		config.MySQL.Primary = v
	}

	// Environment variable overrides for Postgres
	if v := os.Getenv("TQDBPROXY_POSTGRES_LISTEN"); v != "" {
		config.Postgres.Listen = v
	}
	if v := os.Getenv("TQDBPROXY_POSTGRES_BACKEND"); v != "" {
		config.Postgres.Backend = v
		config.Postgres.Primary = v // Also set primary for backward compatibility
	}
	if v := os.Getenv("TQDBPROXY_POSTGRES_PRIMARY"); v != "" {
		config.Postgres.Primary = v
	}

	return config, nil
}

func loadProxyConfig(cfg *ini.File, section, defaultListen, defaultBackend string) ProxyConfig {
	sec := cfg.Section(section)

	listen := sec.Key("listen").MustString(defaultListen)
	backend := sec.Key("backend").MustString(defaultBackend)
	primary := sec.Key("primary").MustString("")

	// Backward compatibility: if primary not set, use backend
	if primary == "" {
		primary = backend
	}

	// Parse replicas (replica1, replica2, etc.)
	var replicas []string
	for i := 1; i <= 10; i++ { // Support up to 10 replicas
		keyName := "replica" + strconv.Itoa(i)
		replica := sec.Key(keyName).String()
		if replica != "" {
			replicas = append(replicas, replica)
		}
	}

	return ProxyConfig{
		Listen:   listen,
		Backend:  backend,
		Primary:  primary,
		Replicas: replicas,
	}
}
