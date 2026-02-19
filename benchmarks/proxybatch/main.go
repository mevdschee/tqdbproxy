package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

// dbIOStats holds the I/O counter snapshotted before and after a run.
type dbIOStats struct {
	pgFsyncs       int64 // PostgreSQL: wal_sync from pg_stat_wal
	mysqlLogWrites int64 // MariaDB:    Innodb_log_writes
}

func readPGIOStats(db *sql.DB) dbIOStats {
	var s dbIOStats
	db.QueryRow(`SELECT wal_sync FROM pg_stat_wal`).Scan(&s.pgFsyncs)
	return s
}

func readMySQLIOStats(db *sql.DB) dbIOStats {
	var s dbIOStats
	rows, err := db.Query(`SHOW GLOBAL STATUS WHERE Variable_name = 'Innodb_log_writes'`)
	if err != nil {
		return s
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		if k == "Innodb_log_writes" {
			s.mysqlLogWrites, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return s
}

type BenchmarkResult struct {
	TargetOpsPerSec int
	BatchMs         int
	ActualOpsPerSec float64
	AvgLatencyMs    float64
	TotalOps        int64
	TotalBatches    int64
	AvgBatchSize    float64
	DBFsyncs        int64 // PG: wal_sync delta; MariaDB: Innodb_log_writes delta
}

func runBenchmark(numWorkers int, batchMs int, totalRecords int64, dbType string, dsn string) BenchmarkResult {
	db, err := sql.Open(dbType, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Pool must be at least as large as numWorkers so every goroutine can hold
	// an open connection concurrently; a lower limit serialises requests and
	// kills batching.
	db.SetMaxOpenConns(numWorkers)
	db.SetMaxIdleConns(numWorkers)
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

	// Setup table - create if not exists, then clear data
	// Using DELETE instead of TRUNCATE/DROP to preserve table stats
	if dbType == "postgres" {
		_, err = db.Exec("CREATE TABLE IF NOT EXISTS test (id SERIAL PRIMARY KEY, value INTEGER, created_at BIGINT)")
		if err != nil {
			log.Fatal(err)
		}
		db.Exec("DELETE FROM test")
	} else {
		_, err = db.Exec("CREATE TABLE IF NOT EXISTS test (id INTEGER PRIMARY KEY AUTO_INCREMENT, value INTEGER, created_at BIGINT)")
		if err != nil {
			log.Fatal(err)
		}
		db.Exec("DELETE FROM test")
	}

	// Track operations and latencies
	var totalOps atomic.Int64
	var totalLatencyNs atomic.Int64
	var wg sync.WaitGroup

	startTime := time.Now()

	// Helper: read the global batch counter from the proxy status
	readBatchCount := func() int64 {
		var n int64
		if dbType == "postgres" {
			rows, err := db.Query("SELECT * FROM pg_tqdb_status()")
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var k, v string
					if err := rows.Scan(&k, &v); err == nil && k == "writebatch.batches.total" {
						n, _ = strconv.ParseInt(v, 10, 64)
					}
				}
			}
		} else {
			rows, err := db.Query("SHOW TQDB STATUS")
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var k, v string
					if err := rows.Scan(&k, &v); err == nil && k == "writebatch.batches.total" {
						n, _ = strconv.ParseInt(v, 10, 64)
					}
				}
			}
		}
		return n
	}
	batchCountBefore := readBatchCount()

	// Open a direct backend connection (bypassing proxy) for I/O stats.
	var ioDirect *sql.DB
	var ioStatsBefore dbIOStats
	if dbType == "postgres" {
		ioDirect, _ = sql.Open("postgres", "host=127.0.0.1 port=5432 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable")
		if ioDirect != nil {
			ioStatsBefore = readPGIOStats(ioDirect)
		}
	} else {
		ioDirect, _ = sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3306)/tqdbproxy")
		if ioDirect != nil {
			ioStatsBefore = readMySQLIOStats(ioDirect)
		}
	}

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

			for {
				// Check if we've reached the target number of operations
				if totalOps.Load() >= totalRecords {
					break
				}

				reqStart := time.Now()

				// Use parameterized queries for both databases
				_, err := db.Exec(insertQuery, workerID, time.Now().UnixNano())

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

	totalBatches := readBatchCount() - batchCountBefore
	var avgBatchSize float64

	var dbFsyncs int64
	if ioDirect != nil {
		var ioStatsAfter dbIOStats
		if dbType == "postgres" {
			ioStatsAfter = readPGIOStats(ioDirect)
			dbFsyncs = ioStatsAfter.pgFsyncs - ioStatsBefore.pgFsyncs
		} else {
			ioStatsAfter = readMySQLIOStats(ioDirect)
			dbFsyncs = ioStatsAfter.mysqlLogWrites - ioStatsBefore.mysqlLogWrites
		}
		ioDirect.Close()
	}

	elapsed := time.Since(startTime)
	total := totalOps.Load()
	avgOpsPerSec := float64(total) / elapsed.Seconds()
	avgLatencyMs := float64(totalLatencyNs.Load()) / float64(total) / 1e6

	if totalBatches > 0 {
		avgBatchSize = float64(total) / float64(totalBatches)
	}

	return BenchmarkResult{
		TargetOpsPerSec: numWorkers,
		BatchMs:         batchMs,
		ActualOpsPerSec: avgOpsPerSec,
		AvgLatencyMs:    avgLatencyMs,
		TotalOps:        total,
		TotalBatches:    totalBatches,
		AvgBatchSize:    avgBatchSize,
		DBFsyncs:        dbFsyncs,
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

	fmt.Fprintf(f, "# BatchHint Throughput(k) Latency(ms) Fsyncs\n")
	for _, r := range results {
		fmt.Fprintf(f, "batch:%d %.1f %.2f %d\n",
			r.BatchMs,
			r.ActualOpsPerSec/1000,
			r.AvgLatencyMs,
			r.DBFsyncs)
	}
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
}

func main() {
	// Fixed concurrency: all runs use the same number of workers so that the
	// only variable across runs is the batch window size.
	const numWorkers = 1000
	totalRecords := int64(100_000)

	log.Println("Proxy Write Batching Throughput & Latency Benchmark")
	log.Println("====================================================")
	log.Println("Testing batching hint performance via proxy")
	log.Printf("(%d workers, %d total records each)", numWorkers, totalRecords)
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

	// Batch windows to test: 0 = no batching (baseline), then increasing windows.
	batchWindows := []int{0, 1, 5, 10, 50, 100}

	// PostgreSQL tests via proxy
	log.Printf("=== PostgreSQL Tests (via Proxy, %d workers) ===", numWorkers)
	pgDSN := "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable"
	var pgResults []BenchmarkResult
	for _, batchMs := range batchWindows {
		time.Sleep(250 * time.Millisecond) // Cooldown
		result := runBenchmark(numWorkers, batchMs, totalRecords, "postgres", pgDSN)
		pgResults = append(pgResults, result)
		ioLabel := "fsyncs"
		if batchMs == 0 {
			log.Printf("  batch:%4dms -> %.0f ops/sec, %.2f ms latency, %d batches (avg %.1f), %s %d",
				batchMs, result.ActualOpsPerSec, result.AvgLatencyMs, result.TotalBatches, result.AvgBatchSize,
				ioLabel, result.DBFsyncs)
		} else {
			log.Printf("  batch:%4dms -> %.0f ops/sec, %.2f ms latency, %d batches (avg %.1f), %s %d",
				batchMs, result.ActualOpsPerSec, result.AvgLatencyMs, result.TotalBatches, result.AvgBatchSize,
				ioLabel, result.DBFsyncs)
		}
	}

	// MariaDB tests via proxy
	log.Printf("\n=== MariaDB Tests (via Proxy, %d workers) ===", numWorkers)
	mysqlDSN := "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy"
	var mysqlResults []BenchmarkResult
	for _, batchMs := range batchWindows {
		result := runBenchmark(numWorkers, batchMs, totalRecords, "mysql", mysqlDSN)
		mysqlResults = append(mysqlResults, result)
		ioLabel := "writes"
		if batchMs == 0 {
			log.Printf("  batch:%4dms -> %.0f ops/sec, %.2f ms latency, %d batches (avg %.1f), %s %d",
				batchMs, result.ActualOpsPerSec, result.AvgLatencyMs, result.TotalBatches, result.AvgBatchSize,
				ioLabel, result.DBFsyncs)
		} else {
			log.Printf("  batch:%4dms -> %.0f ops/sec, %.2f ms latency, %d batches (avg %.1f), %s %d",
				batchMs, result.ActualOpsPerSec, result.AvgLatencyMs, result.TotalBatches, result.AvgBatchSize,
				ioLabel, result.DBFsyncs)
		}
		time.Sleep(1 * time.Second) // Cooldown
	}

	// Generate output files
	generateBarChart(pgResults, "postgres")
	generateBarChart(mysqlResults, "mysql")
	generateGnuplotScript()

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
		}
	}

	log.Println("\nTo analyze profiles:")
	log.Println("  go tool pprof cpu.prof")
	log.Println("  go tool pprof mem.prof")
}
