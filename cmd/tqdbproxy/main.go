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

	// Initialize cache (10000 entries max)
	queryCache, err := cache.New(10000)
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

	log.Println("TQDBProxy started. Press Ctrl+C to stop.")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
}
