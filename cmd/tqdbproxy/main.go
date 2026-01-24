package main

import (
	"context"
	"flag"
	"log"
	"net/http"
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

	// Start metrics HTTP server
	go func() {
		http.Handle("/metrics", metrics.Handler())
		log.Printf("Metrics endpoint at http://localhost%s/metrics", *metricsAddr)
		if err := http.ListenAndServe(*metricsAddr, nil); err != nil {
			log.Printf("Metrics server error: %v", err)
		}
	}()

	// Initialize cache with thundering herd protection
	queryCache, err := cache.New(cache.DefaultCacheConfig())
	if err != nil {
		log.Fatalf("Failed to create cache: %v", err)
	}

	// Create MariaDB replica pool
	mariadbPool := replica.NewPool(cfg.MariaDB.Primary, cfg.MariaDB.Replicas)
	log.Printf("[MariaDB] Primary: %s, Replicas: %v", cfg.MariaDB.Primary, cfg.MariaDB.Replicas)

	// Start health checks for MariaDB replicas
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mariadbPool.StartHealthChecks(ctx, 10*time.Second)

	// Start MariaDB proxy with replica pool
	mariadbProxy := mariadb.New(cfg.MariaDB.Listen, cfg.MariaDB.Socket, mariadbPool, queryCache)
	if err := mariadbProxy.Start(); err != nil {
		log.Fatalf("Failed to start MariaDB proxy: %v", err)
	}

	// Create PostgreSQL replica pool
	pgPool := replica.NewPool(cfg.Postgres.Primary, cfg.Postgres.Replicas)
	log.Printf("[PostgreSQL] Primary: %s, Replicas: %v", cfg.Postgres.Primary, cfg.Postgres.Replicas)

	// Start PostgreSQL proxy with replica pool and caching
	pgProxy := postgres.New(cfg.Postgres.Listen, cfg.Postgres.Socket, pgPool, queryCache)
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

			// Update MariaDB replica pool
			mariadbPool.UpdateReplicas(newCfg.MariaDB.Primary, newCfg.MariaDB.Replicas)
			log.Printf("[MariaDB] Reloaded - Primary: %s, Replicas: %v", newCfg.MariaDB.Primary, newCfg.MariaDB.Replicas)

			// Update PostgreSQL replica pool
			pgPool.UpdateReplicas(newCfg.Postgres.Primary, newCfg.Postgres.Replicas)
			log.Printf("[PostgreSQL] Reloaded - Primary: %s, Replicas: %v", newCfg.Postgres.Primary, newCfg.Postgres.Replicas)

			log.Println("Configuration reloaded successfully")

		case syscall.SIGINT, syscall.SIGTERM:
			log.Println("Shutting down...")
			return
		}
	}
}
