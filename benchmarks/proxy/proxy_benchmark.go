package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

func main() {
	user := flag.String("user", "tqdbproxy", "Database user")
	pass := flag.String("pass", "tqdbproxy", "Database password")
	db := flag.String("db", "tqdbproxy", "Database name")
	connections := flag.Int("c", 10, "Number of concurrent connections")
	duration := flag.Int("t", 3, "Test duration in seconds")
	csvFile := flag.String("csv", "", "Write CSV output to file")
	flag.Parse()

	// MySQL DSNs
	mysqlDirectDSN := fmt.Sprintf("%s:%s@tcp(127.0.0.1:3306)/%s", *user, *pass, *db)
	mysqlProxyDSN := fmt.Sprintf("%s:%s@tcp(127.0.0.1:3307)/%s", *user, *pass, *db)

	// PostgreSQL DSNs
	pgDirectDSN := fmt.Sprintf("host=127.0.0.1 port=5432 user=%s password=%s dbname=%s sslmode=disable", *user, *pass, *db)
	pgProxyDSN := fmt.Sprintf("host=127.0.0.1 port=5433 user=%s password=%s dbname=%s sslmode=disable", *user, *pass, *db)

	// Test cases for MySQL
	mysqlCases := []struct {
		name    string
		sleepMs float64
		query   string
		cached  string
	}{
		{"SLEEP(0)", 0, "SELECT SLEEP(0)", "/* ttl:60 */ SELECT SLEEP(0)"},
		{"SLEEP(0.1ms)", 0.1, "SELECT SLEEP(0.0001)", "/* ttl:60 */ SELECT SLEEP(0.0001)"},
		{"SLEEP(1ms)", 1, "SELECT SLEEP(0.001)", "/* ttl:60 */ SELECT SLEEP(0.001)"},
		{"SLEEP(10ms)", 10, "SELECT SLEEP(0.01)", "/* ttl:60 */ SELECT SLEEP(0.01)"},
	}

	// Test cases for PostgreSQL
	pgCases := []struct {
		name    string
		sleepMs float64
		query   string
		cached  string
	}{
		{"pg_sleep(0)", 0, "SELECT pg_sleep(0)", "/* ttl:60 */ SELECT pg_sleep(0)"},
		{"pg_sleep(0.1ms)", 0.1, "SELECT pg_sleep(0.0001)", "/* ttl:60 */ SELECT pg_sleep(0.0001)"},
		{"pg_sleep(1ms)", 1, "SELECT pg_sleep(0.001)", "/* ttl:60 */ SELECT pg_sleep(0.001)"},
		{"pg_sleep(10ms)", 10, "SELECT pg_sleep(0.01)", "/* ttl:60 */ SELECT pg_sleep(0.01)"},
	}

	// Open CSV file if specified
	var csvOut *os.File
	if *csvFile != "" {
		var err error
		csvOut, err = os.Create(*csvFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create CSV file: %v\n", err)
			os.Exit(1)
		}
		defer csvOut.Close()
		fmt.Fprintf(csvOut, "# Connections: %d\n", *connections)
		fmt.Fprintln(csvOut, "Database,QueryType,SleepMs,DirectRPS,ProxyRPS,CacheRPS")
	}

	// MySQL benchmark
	fmt.Printf("\n=== MySQL Benchmark (%d connections, %ds per test) ===\n", *connections, *duration)
	fmt.Println("============================================================")
	fmt.Printf("%-15s %12s %12s %12s\n", "Query Type", "Direct RPS", "Proxy RPS", "Cache RPS")
	fmt.Println("------------------------------------------------------------")

	for _, tc := range mysqlCases {
		directRPS := benchmarkConcurrent("mysql", mysqlDirectDSN, tc.query, *connections, *duration)
		proxyRPS := benchmarkConcurrent("mysql", mysqlProxyDSN, tc.query, *connections, *duration)

		primeCache("mysql", mysqlProxyDSN, tc.cached)
		cacheRPS := benchmarkConcurrent("mysql", mysqlProxyDSN, tc.cached, *connections, *duration)

		fmt.Printf("%-15s %12.0f %12.0f %12.0f\n", tc.name, directRPS, proxyRPS, cacheRPS)

		if csvOut != nil {
			fmt.Fprintf(csvOut, "MySQL,%s,%.1f,%.0f,%.0f,%.0f\n", tc.name, tc.sleepMs, directRPS, proxyRPS, cacheRPS)
		}
	}

	// PostgreSQL benchmark
	fmt.Printf("\n=== PostgreSQL Benchmark (%d connections, %ds per test) ===\n", *connections, *duration)
	fmt.Println("============================================================")
	fmt.Printf("%-15s %12s %12s %12s\n", "Query Type", "Direct RPS", "Proxy RPS", "Cache RPS")
	fmt.Println("------------------------------------------------------------")

	for _, tc := range pgCases {
		directRPS := benchmarkConcurrent("postgres", pgDirectDSN, tc.query, *connections, *duration)
		proxyRPS := benchmarkConcurrent("postgres", pgProxyDSN, tc.query, *connections, *duration)

		primeCache("postgres", pgProxyDSN, tc.cached)
		cacheRPS := benchmarkConcurrent("postgres", pgProxyDSN, tc.cached, *connections, *duration)

		fmt.Printf("%-15s %12.0f %12.0f %12.0f\n", tc.name, directRPS, proxyRPS, cacheRPS)

		if csvOut != nil {
			fmt.Fprintf(csvOut, "PostgreSQL,%s,%.1f,%.0f,%.0f,%.0f\n", tc.name, tc.sleepMs, directRPS, proxyRPS, cacheRPS)
		}
	}
}

func primeCache(driver, dsn, query string) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return
	}
	defer db.Close()
	rows, err := db.Query(query)
	if err == nil {
		rows.Close()
	}
}

func benchmarkConcurrent(driver, dsn, query string, numConns, durationSec int) float64 {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
		return 0
	}
	defer db.Close()

	db.SetMaxOpenConns(numConns)
	db.SetMaxIdleConns(numConns)
	db.SetConnMaxLifetime(time.Minute)

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to ping: %v\n", err)
		return 0
	}

	// Warmup
	var wgWarmup sync.WaitGroup
	for i := 0; i < numConns; i++ {
		wgWarmup.Add(1)
		go func() {
			defer wgWarmup.Done()
			rows, _ := db.Query(query)
			if rows != nil {
				rows.Close()
			}
		}()
	}
	wgWarmup.Wait()

	var totalOps int64
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					rows, err := db.Query(query)
					if err == nil {
						rows.Close()
						atomic.AddInt64(&totalOps, 1)
					}
				}
			}
		}()
	}

	time.Sleep(time.Duration(durationSec) * time.Second)
	close(stop)
	wg.Wait()

	return float64(totalOps) / float64(durationSec)
}
