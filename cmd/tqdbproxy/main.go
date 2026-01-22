package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/config"
	"github.com/mevdschee/tqdbproxy/mysql"
	"github.com/mevdschee/tqdbproxy/proxy"
)

func main() {
	configPath := flag.String("config", "config.ini", "Path to configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize cache (10000 entries max)
	queryCache, err := cache.New(10000)
	if err != nil {
		log.Fatalf("Failed to create cache: %v", err)
	}

	// Start MySQL proxy with caching
	mysqlProxy := mysql.New(cfg.MySQL.Listen, cfg.MySQL.Backend, queryCache)
	if err := mysqlProxy.Start(); err != nil {
		log.Fatalf("Failed to start MySQL proxy: %v", err)
	}

	// Start PostgreSQL proxy (transparent for now)
	pgProxy := proxy.New("PostgreSQL", cfg.Postgres.Listen, cfg.Postgres.Backend)
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
