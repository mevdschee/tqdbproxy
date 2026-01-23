package config

import (
	"os"
	"strconv"

	"gopkg.in/ini.v1"
)

// Config holds the proxy configuration
type Config struct {
	MariaDB  ProxyConfig
	Postgres ProxyConfig
}

// ProxyConfig holds configuration for a single protocol proxy
type ProxyConfig struct {
	Listen   string
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
		MariaDB:  loadProxyConfig(cfg, "mariadb", ":3307", "127.0.0.1:3306"),
		Postgres: loadProxyConfig(cfg, "postgres", ":5433", "127.0.0.1:5432"),
	}

	// Environment variable overrides for MariaDB
	if v := os.Getenv("TQDBPROXY_MARIADB_LISTEN"); v != "" {
		config.MariaDB.Listen = v
	}
	if v := os.Getenv("TQDBPROXY_MARIADB_PRIMARY"); v != "" {
		config.MariaDB.Primary = v
	}

	// Environment variable overrides for Postgres
	if v := os.Getenv("TQDBPROXY_POSTGRES_LISTEN"); v != "" {
		config.Postgres.Listen = v
	}
	if v := os.Getenv("TQDBPROXY_POSTGRES_PRIMARY"); v != "" {
		config.Postgres.Primary = v
	}

	return config, nil
}

func loadProxyConfig(cfg *ini.File, section, defaultListen, defaultPrimary string) ProxyConfig {
	sec := cfg.Section(section)

	listen := sec.Key("listen").MustString(defaultListen)
	primary := sec.Key("primary").MustString(defaultPrimary)

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
		Primary:  primary,
		Replicas: replicas,
	}
}
