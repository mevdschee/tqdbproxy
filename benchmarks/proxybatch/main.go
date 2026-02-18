package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

type BenchmarkResult struct {
	TargetOpsPerSec int
	BatchMs         int
	ActualOpsPerSec float64
	AvgLatencyMs    float64
	TotalOps        int64
}

func runBenchmark(targetOpsPerSec int, duration time.Duration, dbType string, dsn string) BenchmarkResult {
	log.Printf("Testing %s at %dk ops/sec...", dbType, targetOpsPerSec/1000)

	db, err := sql.Open(dbType, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Configure connection pool based on expected load
	if targetOpsPerSec <= 10_000 {
		db.SetMaxOpenConns(50)
		db.SetMaxIdleConns(10)
	} else if targetOpsPerSec <= 100_000 {
		db.SetMaxOpenConns(200)
		db.SetMaxIdleConns(50)
	} else {
		db.SetMaxOpenConns(500)
		db.SetMaxIdleConns(100)
	}
	db.SetConnMaxLifetime(30 * time.Second)

	// Test connection
	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to connect to %s: %v", dbType, err)
	}

	// For MySQL, explicitly select database
	if dbType == "mysql" {
		_, err = db.Exec("USE tqdbproxy")
		if err != nil {
			log.Fatalf("Failed to select database: %v", err)
		}
	}

	// Setup table
	if dbType == "postgres" {
		db.Exec("DROP TABLE IF EXISTS test")
		_, err = db.Exec("CREATE TABLE test (id SERIAL PRIMARY KEY, value INTEGER, created_at BIGINT)")
	} else {
		db.Exec("DROP TABLE IF EXISTS test")
		_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTO_INCREMENT, value INTEGER, created_at BIGINT)")
	}
	if err != nil {
		log.Fatal(err)
	}

	// Configure write batch manager with hint-based batching
	// Different batch windows based on target rate
	var batchMs int
	var label string

	if targetOpsPerSec <= 1_000 {
		// Baseline: no batching
		batchMs = 0
		label = "baseline"
	} else if targetOpsPerSec <= 10_000 {
		// Low rate: use 1ms batching window
		batchMs = 1
		label = "1ms batch"
	} else if targetOpsPerSec <= 100_000 {
		// Medium rate: use 10ms batching window
		batchMs = 10
		label = "10ms batch"
	} else {
		// High rate: use 100ms batching window
		batchMs = 100
		label = "100ms batch"
	}

	log.Printf("  Using %s (batch:%d)", label, batchMs)

	// Track operations and latencies
	var totalOps atomic.Int64
	var totalLatencyNs atomic.Int64
	var wg sync.WaitGroup

	// Scale workers based on target rate to avoid over-saturation at low rates
	var numWorkers int
	if targetOpsPerSec <= 10_000 {
		numWorkers = 100 // Fewer workers for low rates
	} else if targetOpsPerSec <= 100_000 {
		numWorkers = 1000
	} else {
		numWorkers = 5000 // Max workers for high rates
	}

	startTime := time.Now()
	endTime := startTime.Add(duration)

	// Choose query based on database backend and include batch hint
	var insertQuery string
	if dbType == "postgres" {
		if batchMs > 0 {
			insertQuery = fmt.Sprintf("/* batch:%d */ INSERT INTO test (value, created_at) VALUES ($1, $2)", batchMs)
		} else {
			insertQuery = "INSERT INTO test (value, created_at) VALUES ($1, $2)"
		}
	} else {
		if batchMs > 0 {
			insertQuery = fmt.Sprintf("/* batch:%d */ INSERT INTO test (value, created_at) VALUES (?, ?)", batchMs)
		} else {
			insertQuery = "INSERT INTO test (value, created_at) VALUES (?, ?)"
		}
	}

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Add small jitter to prevent thundering herd at start
			time.Sleep(time.Duration(workerID) * time.Microsecond)

			var errorCount int64

			for time.Now().Before(endTime) {
				reqStart := time.Now()

				// Format query directly to avoid prepared statements
				var formattedQuery string
				if dbType == "postgres" {
					formattedQuery = fmt.Sprintf(strings.Replace(insertQuery, "$1", "%d", 1), workerID)
					formattedQuery = strings.Replace(formattedQuery, "$2", fmt.Sprintf("%d", time.Now().UnixNano()), 1)
				} else {
					formattedQuery = strings.Replace(insertQuery, "?", fmt.Sprintf("%d", workerID), 1)
					formattedQuery = strings.Replace(formattedQuery, "?", fmt.Sprintf("%d", time.Now().UnixNano()), 1)
				}

				// Execute write directly to proxy (proxy handles batching via hint)
				_, err := db.Exec(formattedQuery)

				if err == nil {
					totalOps.Add(1)
					totalLatencyNs.Add(time.Since(reqStart).Nanoseconds())
				} else {
					errorCount++
					if errorCount <= 3 {
						// Log first few errors per worker to help diagnose issues
						log.Printf("Worker %d error: %v", workerID, err)
					}
				}
			}
		}(i)
	}

	wg.Wait()

	elapsed := time.Since(startTime)
	total := totalOps.Load()
	avgOpsPerSec := float64(total) / elapsed.Seconds()
	avgLatencyMs := float64(totalLatencyNs.Load()) / float64(total) / 1e6

	return BenchmarkResult{
		TargetOpsPerSec: targetOpsPerSec,
		BatchMs:         batchMs,
		ActualOpsPerSec: avgOpsPerSec,
		AvgLatencyMs:    avgLatencyMs,
		TotalOps:        total,
	}
}

func generateBarChart(results []BenchmarkResult, dbType string) {
	// Generate data file
	filename := fmt.Sprintf("bars_%s.dat", dbType)
	f, err := os.Create(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	fmt.Fprintf(f, "# BatchHint Throughput(k) Latency(ms)\n")
	for _, r := range results {
		fmt.Fprintf(f, "batch:%d %.1f %.2f\n",
			r.BatchMs,
			r.ActualOpsPerSec/1000,
			r.AvgLatencyMs)
	}
	log.Printf("Generated: %s", filename)
}

func generateGnuplotScript() {
	script := `#!/usr/bin/gnuplot
set terminal pngcairo size 1600,600 enhanced font 'Arial,12'
set output 'proxybatch_performance.png'

set multiplot layout 1,2 title "Proxy Hint-Based Write Batching Performance (3s tests, max 1k batch size)"

# Left plot - PostgreSQL
set title "PostgreSQL (via Proxy)"
set xlabel "Batch Hint (ms)"
set ylabel "Throughput (k ops/sec)" textcolor rgb "blue"
set y2label "Latency (ms)" textcolor rgb "red"
set ytics nomirror
set y2tics
set style data histograms
set style histogram clustered gap 1
set style fill solid 0.5 border -1
set boxwidth 0.8
set grid y

plot 'bars_postgres.dat' using 2:xtic(1) title 'Throughput' axes x1y1 linecolor rgb "blue", \
     'bars_postgres.dat' using 3 title 'Latency' axes x1y2 linecolor rgb "red"

# Right plot - MariaDB
unset title
set title "MariaDB (via Proxy)"
set xlabel "Batch Hint (ms)"
set ylabel "Throughput (k ops/sec)" textcolor rgb "blue"
set y2label "Latency (ms)" textcolor rgb "red"

plot 'bars_mysql.dat' using 2:xtic(1) title 'Throughput' axes x1y1 linecolor rgb "blue", \
     'bars_mysql.dat' using 3 title 'Latency' axes x1y2 linecolor rgb "red"

unset multiplot
`
	err := os.WriteFile("plot_bars.gnu", []byte(script), 0644)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Generated: plot_bars.gnu")
}

func main() {
	log.Println("Proxy Write Batching Throughput & Latency Benchmark")
	log.Println("====================================================")
	log.Println("Testing batching hint performance via proxy (3s each)")
	log.Println()

	// Start CPU profiling
	cpuFile, err := os.Create("cpu.prof")
	if err != nil {
		log.Fatal("Could not create CPU profile: ", err)
	}
	defer cpuFile.Close()
	if err := pprof.StartCPUProfile(cpuFile); err != nil {
		log.Fatal("Could not start CPU profile: ", err)
	}
	defer pprof.StopCPUProfile()
	log.Println("CPU profiling enabled -> cpu.prof")

	// Test rates with different batching hints
	targets := []int{1_000, 10_000, 100_000, 1_000_000}
	duration := 3 * time.Second

	// PostgreSQL tests via proxy
	log.Println("=== PostgreSQL Tests (via Proxy) ===")
	pgDSN := "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable"
	var pgResults []BenchmarkResult
	for _, target := range targets {
		result := runBenchmark(target, duration, "postgres", pgDSN)
		pgResults = append(pgResults, result)
		speedup := result.ActualOpsPerSec / float64(pgResults[0].ActualOpsPerSec)
		log.Printf("  %dk target -> %.0f ops/sec actual, %.2f ms latency (%.1fx speedup)",
			target/1000, result.ActualOpsPerSec, result.AvgLatencyMs, speedup)
		time.Sleep(1 * time.Second) // Cooldown
	}

	// MariaDB tests via proxy
	log.Println("\n=== MariaDB Tests (via Proxy) ===")
	mysqlDSN := "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy"
	var mysqlResults []BenchmarkResult
	for _, target := range targets {
		result := runBenchmark(target, duration, "mysql", mysqlDSN)
		mysqlResults = append(mysqlResults, result)
		speedup := result.ActualOpsPerSec / float64(mysqlResults[0].ActualOpsPerSec)
		log.Printf("  %dk target -> %.0f ops/sec actual, %.2f ms latency (%.1fx speedup)",
			target/1000, result.ActualOpsPerSec, result.AvgLatencyMs, speedup)
		time.Sleep(1 * time.Second) // Cooldown
	}

	// Generate output files
	log.Println("\n=== Generating output files ===")
	generateBarChart(pgResults, "postgres")
	generateBarChart(mysqlResults, "mysql")
	generateGnuplotScript()

	log.Println("\n=== Summary ===")
	log.Printf("PostgreSQL: %.0f → %.0f ops/sec (%.1fx speedup)",
		pgResults[0].ActualOpsPerSec, pgResults[len(pgResults)-1].ActualOpsPerSec,
		pgResults[len(pgResults)-1].ActualOpsPerSec/pgResults[0].ActualOpsPerSec)
	log.Printf("MariaDB: %.0f → %.0f ops/sec (%.1fx speedup)",
		mysqlResults[0].ActualOpsPerSec, mysqlResults[len(mysqlResults)-1].ActualOpsPerSec,
		mysqlResults[len(mysqlResults)-1].ActualOpsPerSec/mysqlResults[0].ActualOpsPerSec)

	log.Println("\nTo generate graph, run:")
	log.Println("  gnuplot plot_bars.gnu")
	log.Println("  (creates proxybatch_performance.png)")

	// Write memory profile
	memFile, err := os.Create("mem.prof")
	if err != nil {
		log.Printf("Could not create memory profile: %v", err)
	} else {
		defer memFile.Close()
		if err := pprof.WriteHeapProfile(memFile); err != nil {
			log.Printf("Could not write memory profile: %v", err)
		} else {
			log.Println("\nMemory profile written -> mem.prof")
		}
	}

	log.Println("\nTo analyze profiles:")
	log.Println("  go tool pprof cpu.prof")
	log.Println("  go tool pprof mem.prof")
}
