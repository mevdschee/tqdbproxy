package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/config"
	"github.com/mevdschee/tqdbproxy/mariadb"
	"github.com/mevdschee/tqdbproxy/metrics"
	"github.com/mevdschee/tqdbproxy/postgres"
	"github.com/mevdschee/tqdbproxy/replica"
)

func main() {
	configPath := flag.String("config", "config.ini", "Path to configuration file")
	metricsAddr := flag.String("metrics", ":9090", "Metrics endpoint address")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize metrics
	metrics.Init()

	// Start metrics HTTP server with pprof
	go func() {
		http.Handle("/metrics", metrics.Handler())
		log.Printf("Metrics endpoint at http://localhost%s/metrics", *metricsAddr)
		log.Printf("Pprof endpoints at http://localhost%s/debug/pprof/", *metricsAddr)
		if err := http.ListenAndServe(*metricsAddr, nil); err != nil {
			log.Printf("Metrics server error: %v", err)
		}
	}()

	// Initialize cache with thundering herd protection
	queryCache, err := cache.New(cache.DefaultCacheConfig())
	if err != nil {
		log.Fatalf("Failed to create cache: %v", err)
	}

	// Create MariaDB pools
	mariadbPools := initPools(cfg.MariaDB.Backends)
	log.Printf("[MariaDB] Initialized %d backend pools", len(mariadbPools))

	// Start health checks for all MariaDB pools
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for name, pool := range mariadbPools {
		go pool.StartHealthChecks(ctx, 10*time.Second)
		log.Printf("[MariaDB] Pool %s primary: %s", name, pool.GetPrimary())
	}

	// Start MariaDB proxy with config and pools
	mariadbProxy := mariadb.New(cfg.MariaDB, mariadbPools, queryCache)
	if err := mariadbProxy.Start(); err != nil {
		log.Fatalf("Failed to start MariaDB proxy: %v", err)
	}

	// Create PostgreSQL pools
	pgPools := initPools(cfg.Postgres.Backends)
	log.Printf("[PostgreSQL] Initialized %d backend pools", len(pgPools))

	// Start health checks for all PostgreSQL pools
	for name, pool := range pgPools {
		go pool.StartHealthChecks(ctx, 10*time.Second)
		log.Printf("[PostgreSQL] Pool %s primary: %s", name, pool.GetPrimary())
	}

	// Start PostgreSQL proxy with config and pools
	pgProxy := postgres.New(cfg.Postgres, pgPools, queryCache)
	if err := pgProxy.Start(); err != nil {
		log.Fatalf("Failed to start PostgreSQL proxy: %v", err)
	}

	log.Println("TQDBProxy started. Press Ctrl+C to stop. Send SIGHUP to reload config.")

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		sig := <-sigChan
		switch sig {
		case syscall.SIGHUP:
			log.Println("Received SIGHUP, reloading configuration...")
			newCfg, err := config.Load(*configPath)
			if err != nil {
				log.Printf("Failed to reload config: %v", err)
				continue
			}

			// Update MariaDB pools
			mariadbPools = updatePools(mariadbPools, newCfg.MariaDB.Backends, ctx)
			mariadbProxy.UpdateConfig(newCfg.MariaDB, mariadbPools)
			log.Printf("[MariaDB] Reloaded - %d backends", len(newCfg.MariaDB.Backends))

			// Update PostgreSQL pools
			pgPools = updatePools(pgPools, newCfg.Postgres.Backends, ctx)
			pgProxy.UpdateConfig(newCfg.Postgres, pgPools)
			log.Printf("[PostgreSQL] Reloaded - %d backends", len(newCfg.Postgres.Backends))

			log.Println("Configuration reloaded successfully")

		case syscall.SIGINT, syscall.SIGTERM:
			log.Println("Shutting down...")
			return
		}
	}
}

func initPools(backends map[string]config.BackendConfig) map[string]*replica.Pool {
	pools := make(map[string]*replica.Pool)
	for name, backend := range backends {
		pools[name] = replica.NewPool(backend.Primary, backend.Replicas)
	}
	return pools
}

func updatePools(current map[string]*replica.Pool, backends map[string]config.BackendConfig, ctx context.Context) map[string]*replica.Pool {
	newPools := make(map[string]*replica.Pool)

	// Update existing pools or create new ones
	for name, backend := range backends {
		if pool, exists := current[name]; exists {
			pool.UpdateReplicas(backend.Primary, backend.Replicas)
			newPools[name] = pool
		} else {
			pool := replica.NewPool(backend.Primary, backend.Replicas)
			go pool.StartHealthChecks(ctx, 10*time.Second)
			newPools[name] = pool
		}
	}

	// Note: We don't explicitly stop health checks for removed pools here
	// but context cancellation on shutdown handles it. Periodic SIGHUPs
	// might leak some goroutines if backends change frequently, but it's
	// minor for now.

	return newPools
}
