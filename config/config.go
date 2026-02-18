package config

import (
	"log"
	"os"
	"strings"

	"gopkg.in/ini.v1"
)

// Config holds the proxy configuration
type Config struct {
	MariaDB  ProxyConfig
	Postgres ProxyConfig
}

// ProxyConfig holds configuration for a protocol proxy with multiple backends
type ProxyConfig struct {
	Listen     string                   // TCP listen address (e.g., ":3307")
	Socket     string                   // Optional Unix socket path
	Default    string                   // Name of the default backend
	Backends   map[string]BackendConfig // Backend pool configurations
	DBMap      map[string]string        // Mapping of database names to backend names
	WriteBatch WriteBatchConfig         // Write batching configuration
}

// WriteBatchConfig holds configuration for write batching
type WriteBatchConfig struct {
	MaxBatchSize int // Maximum batch size
}

// BackendConfig holds configuration for a single backend pool (primary + replicas)
type BackendConfig struct {
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
		MariaDB:  loadProxyConfig(cfg, "mariadb", ":3307"),
		Postgres: loadProxyConfig(cfg, "postgres", ":5433"),
	}

	// Environment variable overrides for MariaDB
	if v := os.Getenv("TQDBPROXY_MARIADB_LISTEN"); v != "" {
		config.MariaDB.Listen = v
	}
	// Environment variable overrides for Postgres
	if v := os.Getenv("TQDBPROXY_POSTGRES_LISTEN"); v != "" {
		config.Postgres.Listen = v
	}

	return config, nil
}

func loadProxyConfig(cfg *ini.File, protocol, defaultListen string) ProxyConfig {
	sec := cfg.Section(protocol)

	pcfg := ProxyConfig{
		Listen:   sec.Key("listen").MustString(defaultListen),
		Socket:   sec.Key("socket").String(),
		Default:  sec.Key("default").MustString("main"),
		Backends: make(map[string]BackendConfig),
		DBMap:    make(map[string]string),
		WriteBatch: WriteBatchConfig{
			MaxBatchSize: sec.Key("writebatch_max_batch_size").MustInt(1000),
		},
	}

	// Find all backends for this protocol [protocol.name]
	sections := cfg.Sections()
	prefix := protocol + "."
	for _, s := range sections {
		name := s.Name()
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			backendName := name[len(prefix):]

			// Load primary and replicas
			primary := s.Key("primary").String()

			// Support comma-separated replicas
			var replicas []string
			if s.HasKey("replicas") {
				raw := s.Key("replicas").String()
				if raw != "" {
					parts := strings.Split(raw, ",")
					for _, p := range parts {
						replicas = append(replicas, strings.TrimSpace(p))
					}
				}
			}

			if primary != "" {
				pcfg.Backends[backendName] = BackendConfig{
					Primary:  primary,
					Replicas: replicas,
				}

				// Map databases to this backend
				if s.HasKey("databases") {
					dbs := s.Key("databases").String()
					parts := strings.Split(dbs, ",")
					for _, db := range parts {
						pcfg.DBMap[strings.TrimSpace(db)] = backendName
					}
				}
			}
		}
	}

	// Check if at least one backend is defined
	if len(pcfg.Backends) == 0 {
		log.Printf("Warning: no backends defined for %s, proxy will have no shards", protocol)
	}

	return pcfg
}
