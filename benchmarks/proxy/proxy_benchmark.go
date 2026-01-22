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
)

func main() {
	user := flag.String("user", "", "MySQL user")
	pass := flag.String("pass", "", "MySQL password")
	db := flag.String("db", "", "MySQL database")
	connections := flag.Int("c", 10, "Number of concurrent connections")
	duration := flag.Int("t", 3, "Test duration in seconds")
	csvFile := flag.String("csv", "", "Write CSV output to file")
	flag.Parse()

	// Build DSN - handle empty password and database
	auth := *user
	if *pass != "" {
		auth = *user + ":" + *pass
	}
	directDSN := fmt.Sprintf("%s@tcp(127.0.0.1:3306)/%s", auth, *db)
	proxyDSN := fmt.Sprintf("%s@tcp(127.0.0.1:3307)/%s", auth, *db)

	// Test cases: different query complexities
	testCases := []struct {
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
		fmt.Fprintln(csvOut, "QueryType,SleepMs,DirectRPS,ProxyRPS,CacheRPS")
	}

	// Always print to stdout
	fmt.Printf("MySQL Proxy Benchmark (%d connections, %ds per test)\n", *connections, *duration)
	fmt.Println("============================================================")
	fmt.Printf("%-15s %12s %12s %12s\n", "Query Type", "Direct RPS", "Proxy RPS", "Cache RPS")
	fmt.Println("------------------------------------------------------------")

	for _, tc := range testCases {
		directRPS := benchmarkConcurrent(directDSN, tc.query, *connections, *duration)
		proxyRPS := benchmarkConcurrent(proxyDSN, tc.query, *connections, *duration)

		// Prime cache then benchmark cache hits
		primeCache(proxyDSN, tc.cached)
		cacheRPS := benchmarkConcurrent(proxyDSN, tc.cached, *connections, *duration)

		// Always print to stdout
		fmt.Printf("%-15s %12.0f %12.0f %12.0f\n", tc.name, directRPS, proxyRPS, cacheRPS)

		// Write to CSV if specified
		if csvOut != nil {
			fmt.Fprintf(csvOut, "%s,%.1f,%.0f,%.0f,%.0f\n", tc.name, tc.sleepMs, directRPS, proxyRPS, cacheRPS)
		}
	}
}

func primeCache(dsn, query string) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return
	}
	defer db.Close()
	rows, err := db.Query(query)
	if err == nil {
		rows.Close()
	}
}

func benchmarkConcurrent(dsn, query string, numConns, durationSec int) float64 {
	db, err := sql.Open("mysql", dsn)
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
