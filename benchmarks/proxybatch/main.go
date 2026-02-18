package main

import (
"database/sql"
"fmt"
"log"
"os"
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

	// Test connection
	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to connect to %s: %v", dbType, err)
	}

	// Setup table
	if dbType == "postgres" {
		if _, err := db.Exec("DROP TABLE IF EXISTS test"); err != nil {
			log.Printf("Warning: DROP TABLE failed: %v", err)
		}
		_, err = db.Exec("CREATE TABLE test (id SERIAL PRIMARY KEY, value INTEGER, created_at BIGINT)")
	} else {
		if _, err := db.Exec("DROP TABLE IF EXISTS test"); err != nil {
			log.Printf("Warning: DROP TABLE failed: %v", err)
		}
		_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTO_INCREMENT, value INTEGER, created_at BIGINT)")
	}
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	// Configure batch hint based on target rate
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
numWorkers = 1000
} else if targetOpsPerSec <= 100_000 {
numWorkers = 10000
} else {
numWorkers = 50000
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

// Launch workers
for i := 0; i < numWorkers; i++ {
wg.Add(1)
go func(workerID int) {
defer wg.Done()

for {
now := time.Now()
if now.After(endTime) {
return
}

opStart := time.Now()

// Execute write directly to proxy (proxy handles batching)
_, err := db.Exec(insertQuery, workerID, opStart.UnixNano())
if err != nil {
				// Log first few errors to help diagnose issues
				if totalOps.Load() < 5 {
					log.Printf("Worker %d error: %v", workerID, err)
				}
totalLatencyNs.Add(latency.Nanoseconds())

			// Rate limiting: sleep based on target rate
			sleepTime := time.Duration(float64(time.Second) / float64(targetOpsPerSec) * float64(numWorkers))
			if sleepTime > 0 {
				time.Sleep(sleepTime)
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

// Test rates with different batching hints
targets := []int{1_000, 10_000, 100_000, 1_000_000}
duration := 3 * time.Second

// PostgreSQL tests via proxy
log.Println("=== PostgreSQL Tests (via Proxy) ===")
pgDSN := "host=127.0.0.1 port=5433 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable"
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
}
