package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mevdschee/tqdbproxy/writebatch"
)

type DataPoint struct {
	Timestamp       time.Time
	TargetOpsPerSec int
	ActualOpsPerSec uint64
	CurrentDelay    float64
	QueuedOps       int
}

type BenchmarkResult struct {
	TargetOpsPerSec int
	DataPoints      []DataPoint
	Duration        time.Duration
	TotalOps        int64
	AvgDelay        float64
	FinalDelay      float64
}

func runBenchmark(targetOpsPerSec int, duration time.Duration, usePostgres bool) BenchmarkResult {
	log.Printf("=== Starting benchmark: %d ops/sec for %v ===", targetOpsPerSec, duration)

	var db *sql.DB
	var err error

	if usePostgres {
		// Connect to PostgreSQL
		db, err = sql.Open("postgres", "host=127.0.0.1 port=5432 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable")
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()

		// Drop and recreate table for clean test
		db.Exec("DROP TABLE IF EXISTS test")
		_, err = db.Exec("CREATE TABLE test (id SERIAL PRIMARY KEY, value INTEGER, created_at BIGINT)")
		if err != nil {
			log.Fatal(err)
		}
	} else {
		// Create SQLite in-memory database
		db, err = sql.Open("sqlite3", "file:test.db?cache=shared&mode=memory")
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()

		_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value INTEGER, created_at INTEGER)")
		if err != nil {
			log.Fatal(err)
		}

		// Optimize SQLite for performance
		db.Exec("PRAGMA journal_mode=WAL")
		db.Exec("PRAGMA synchronous=NORMAL")
		db.Exec("PRAGMA cache_size=10000")
		db.Exec("PRAGMA temp_store=MEMORY")
	}

	// Configure write batch manager with adaptive settings
	// Use a fixed threshold of 5000 ops/sec so we can see adaptive behavior:
	// - Below 2500 ops/s: delay decreases (more responsive)
	// - Above 5000 ops/s: delay increases (more batching)
	cfg := writebatch.Config{
		InitialDelayMs:  10,
		MaxDelayMs:      100,
		MinDelayMs:      1,
		MaxBatchSize:    1000,
		WriteThreshold:  5000, // Fixed threshold to demonstrate adaptive behavior
		AdaptiveStep:    1.5,
		MetricsInterval: 1, // 1 second between adjustments
	}

	manager := writebatch.New(db, cfg)
	defer manager.Close()

	// Start adaptive adjustment
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go manager.StartAdaptiveAdjustment(ctx)

	// Data collection
	var dataPoints []DataPoint
	var mu sync.Mutex
	var totalOps atomic.Int64

	// Collect metrics every 100ms
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				actualOps := manager.GetOpsPerSecond()
				currentDelay := manager.GetCurrentDelay()
				mu.Lock()
				dataPoints = append(dataPoints, DataPoint{
					Timestamp:       t,
					TargetOpsPerSec: targetOpsPerSec,
					ActualOpsPerSec: actualOps,
					CurrentDelay:    currentDelay,
					QueuedOps:       int(totalOps.Load()),
				})
				mu.Unlock()
			}
		}
	}()

	// Generate load
	var wg sync.WaitGroup
	startTime := time.Now()
	endTime := startTime.Add(duration)

	// Use massive concurrency to saturate the system
	numWorkers := 50000

	opsPerWorker := targetOpsPerSec / numWorkers
	if opsPerWorker < 1 {
		opsPerWorker = 1
	}

	// Pre-allocate reusable context to reduce allocation overhead
	bgCtx := context.Background()

	// Choose query based on database backend
	var insertQuery string
	if usePostgres {
		insertQuery = "INSERT INTO test (value, created_at) VALUES ($1, $2)"
	} else {
		insertQuery = "INSERT INTO test (value, created_at) VALUES (?, ?)"
	}

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Pre-allocate and reuse to reduce GC pressure
			query := insertQuery
			batchKey := "INSERT"
			params := []interface{}{workerID, int64(0)}

			// For high loads, use tight loop instead of ticker
			if targetOpsPerSec >= 50000 {
				// Maximum throughput mode - continuous loop with pre-allocated params
				for time.Now().Before(endTime) {
					params[1] = time.Now().Unix()
					result := manager.Enqueue(bgCtx, batchKey, query, params)

					if result.Error == nil {
						totalOps.Add(1)
					}
				}
			} else {
				// Controlled rate mode with ticker
				interval := time.Second / time.Duration(opsPerWorker)
				if interval < time.Microsecond {
					interval = time.Microsecond
				}

				ticker := time.NewTicker(interval)
				defer ticker.Stop()

				for {
					select {
					case <-ticker.C:
						if time.Now().After(endTime) {
							return
						}

						params[1] = time.Now().Unix()
						result := manager.Enqueue(bgCtx, batchKey, query, params)

						if result.Error == nil {
							totalOps.Add(1)
						}
					}
				}
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond) // Let final metrics collect

	mu.Lock()
	defer mu.Unlock()

	// Calculate average delay
	var sumDelay float64
	for _, dp := range dataPoints {
		sumDelay += dp.CurrentDelay
	}
	avgDelay := sumDelay / float64(len(dataPoints))
	finalDelay := dataPoints[len(dataPoints)-1].CurrentDelay

	result := BenchmarkResult{
		TargetOpsPerSec: targetOpsPerSec,
		DataPoints:      dataPoints,
		Duration:        time.Since(startTime),
		TotalOps:        totalOps.Load(),
		AvgDelay:        avgDelay,
		FinalDelay:      finalDelay,
	}

	log.Printf("Completed: %d ops in %v (%.0f ops/sec actual), Avg Delay: %.1fms, Final Delay: %.1fms",
		result.TotalOps, result.Duration,
		float64(result.TotalOps)/result.Duration.Seconds(),
		result.AvgDelay, result.FinalDelay)

	return result
}

func main() {
	usePostgres := flag.Bool("postgres", false, "Use PostgreSQL instead of SQLite")
	flag.Parse()

	backend := "SQLite"
	if *usePostgres {
		backend = "PostgreSQL"
	}

	// Start CPU profiling
	cpuProfile, err := os.Create("adaptive_cpu.prof")
	if err != nil {
		log.Fatal("Could not create CPU profile: ", err)
	}
	defer cpuProfile.Close()

	if err := pprof.StartCPUProfile(cpuProfile); err != nil {
		log.Fatal("Could not start CPU profile: ", err)
	}
	defer pprof.StopCPUProfile()

	log.Println("Adaptive Write Batching Benchmark")
	log.Println("==================================")
	log.Printf("Backend: %s\n", backend)
	log.Println("CPU profiling enabled -> adaptive_cpu.prof")
	log.Println()

	targets := []int{1_000, 10_000, 100_000, 1_000_000}
	duration := 10 * time.Second
	var results []BenchmarkResult

	for _, target := range targets {
		result := runBenchmark(target, duration, *usePostgres)
		results = append(results, result)

		// Cooldown between tests
		time.Sleep(2 * time.Second)
	}

	// Generate CSV output
	generateCSV(results)

	// Generate gnuplot script
	generateGnuplot()

	// Generate summary
	generateSummary(results)

	log.Println("\n=== Benchmark Complete ===")
	log.Println("Generated files:")
	log.Println("  - adaptive_data.csv (raw data)")
	log.Println("  - adaptive_plot.gnu (gnuplot script)")
	log.Println("  - adaptive_summary.txt (results summary)")
	log.Println("\nTo generate graph, run:")
	log.Println("  gnuplot adaptive_plot.gnu")
	log.Println("  (creates adaptive_benchmark.png)")
}

func generateCSV(results []BenchmarkResult) {
	f, err := os.Create("adaptive_data.csv")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	fmt.Fprintf(f, "timestamp,target_ops_sec,actual_ops_sec,delay_ms,queued_ops,elapsed_sec\n")

	for _, result := range results {
		startTime := result.DataPoints[0].Timestamp
		for _, dp := range result.DataPoints {
			elapsed := dp.Timestamp.Sub(startTime).Seconds()
			fmt.Fprintf(f, "%s,%d,%d,%.2f,%d,%.2f\n",
				dp.Timestamp.Format("15:04:05.000"),
				dp.TargetOpsPerSec,
				dp.ActualOpsPerSec,
				dp.CurrentDelay,
				dp.QueuedOps,
				elapsed)
		}
	}

	log.Println("Generated: adaptive_data.csv")
}

func generateGnuplot() {
	script := `#!/usr/bin/gnuplot

set terminal png size 1600,900 enhanced font 'Arial,12'
set output 'adaptive_benchmark.png'

set title 'Adaptive Write Batching: Delay vs Throughput Under Different Loads' font 'Arial,16'
set xlabel 'Time (seconds)' font 'Arial,12'
set ylabel 'Delay (ms)' font 'Arial,12' textcolor rgb 'blue'
set y2label 'Throughput (ops/sec)' font 'Arial,12' textcolor rgb 'red'

set ytics nomirror tc rgb 'blue'
set y2tics nomirror tc rgb 'red'

set grid
set key outside right top

set style line 1 lc rgb '#0060ad' lt 1 lw 2 pt 7 ps 0.5
set style line 2 lc rgb '#dd181f' lt 1 lw 2 pt 5 ps 0.5
set style line 3 lc rgb '#00ad60' lt 1 lw 2 pt 9 ps 0.5
set style line 4 lc rgb '#ad6000' lt 1 lw 2 pt 11 ps 0.5

set datafile separator ","

plot 'adaptive_data.csv' using 6:($2==1000?$4:1/0) with linespoints ls 1 title '1k ops/s - Delay' axes x1y1, \
     'adaptive_data.csv' using 6:($2==1000?$3:1/0) with lines ls 1 dt 2 title '1k ops/s - Throughput' axes x1y2, \
     'adaptive_data.csv' using 6:($2==10000?$4:1/0) with linespoints ls 2 title '10k ops/s - Delay' axes x1y1, \
     'adaptive_data.csv' using 6:($2==10000?$3:1/0) with lines ls 2 dt 2 title '10k ops/s - Throughput' axes x1y2, \
     'adaptive_data.csv' using 6:($2==100000?$4:1/0) with linespoints ls 3 title '100k ops/s - Delay' axes x1y1, \
     'adaptive_data.csv' using 6:($2==100000?$3:1/0) with lines ls 3 dt 2 title '100k ops/s - Throughput' axes x1y2, \
     'adaptive_data.csv' using 6:($2==1000000?$4:1/0) with linespoints ls 4 title '1M ops/s - Delay' axes x1y1, \
     'adaptive_data.csv' using 6:($2==1000000?$3:1/0) with lines ls 4 dt 2 title '1M ops/s - Throughput' axes x1y2
`

	err := os.WriteFile("adaptive_plot.gnu", []byte(script), 0644)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Generated: adaptive_plot.gnu")
}

func generateSummary(results []BenchmarkResult) {
	f, err := os.Create("adaptive_summary.txt")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	fmt.Fprintf(f, "Adaptive Write Batching Benchmark Results\n")
	fmt.Fprintf(f, "==========================================\n\n")
	fmt.Fprintf(f, "Date: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	for _, result := range results {
		actualOpsPerSec := float64(result.TotalOps) / result.Duration.Seconds()
		efficiency := (actualOpsPerSec / float64(result.TargetOpsPerSec)) * 100

		fmt.Fprintf(f, "Target Load: %s ops/sec\n", formatNumber(result.TargetOpsPerSec))
		fmt.Fprintf(f, "  Duration:         %v\n", result.Duration)
		fmt.Fprintf(f, "  Total Operations: %s\n", formatNumber(int(result.TotalOps)))
		fmt.Fprintf(f, "  Actual Throughput: %s ops/sec (%.1f%% of target)\n",
			formatNumber(int(actualOpsPerSec)), efficiency)
		fmt.Fprintf(f, "  Average Delay:    %.2f ms\n", result.AvgDelay)
		fmt.Fprintf(f, "  Final Delay:      %.2f ms\n", result.FinalDelay)

		// Analyze delay behavior
		if len(result.DataPoints) > 5 {
			initialDelay := result.DataPoints[2].CurrentDelay
			midDelay := result.DataPoints[len(result.DataPoints)/2].CurrentDelay
			delayChange := result.FinalDelay - initialDelay

			fmt.Fprintf(f, "  Delay Analysis:\n")
			fmt.Fprintf(f, "    Initial:  %.2f ms\n", initialDelay)
			fmt.Fprintf(f, "    Mid-run:  %.2f ms\n", midDelay)
			fmt.Fprintf(f, "    Final:    %.2f ms\n", result.FinalDelay)
			fmt.Fprintf(f, "    Change:   %+.2f ms", delayChange)
			if delayChange > 0 {
				fmt.Fprintf(f, " (increased - high load)\n")
			} else if delayChange < 0 {
				fmt.Fprintf(f, " (decreased - low load)\n")
			} else {
				fmt.Fprintf(f, " (stable)\n")
			}
		}
		fmt.Fprintf(f, "\n")
	}

	// Compare delay ranges
	minAvgDelay := results[0].AvgDelay
	maxAvgDelay := results[0].AvgDelay
	for _, r := range results {
		if r.AvgDelay < minAvgDelay {
			minAvgDelay = r.AvgDelay
		}
		if r.AvgDelay > maxAvgDelay {
			maxAvgDelay = r.AvgDelay
		}
	}

	fmt.Fprintf(f, "Key Insights:\n")
	fmt.Fprintf(f, "=============\n\n")
	fmt.Fprintf(f, "1. Delay Range: %.2f ms to %.2f ms\n", minAvgDelay, maxAvgDelay)
	fmt.Fprintf(f, "   - Lower delays at lower throughput (more responsive)\n")
	fmt.Fprintf(f, "   - Higher delays at higher throughput (more batching)\n\n")

	fmt.Fprintf(f, "2. Adaptive Behavior:\n")
	fmt.Fprintf(f, "   - System automatically adjusts delay based on load\n")
	fmt.Fprintf(f, "   - Balances latency vs batching efficiency\n")
	fmt.Fprintf(f, "   - Responds to changes in real-time\n\n")

	fmt.Fprintf(f, "3. Recommendations:\n")
	for _, r := range results {
		if r.TargetOpsPerSec <= 10000 {
			fmt.Fprintf(f, "   - Low load (%s ops/s): Use min delay ~%.0fms for responsiveness\n",
				formatNumber(r.TargetOpsPerSec), r.AvgDelay)
		} else {
			fmt.Fprintf(f, "   - High load (%s ops/s): Use max delay ~%.0fms for throughput\n",
				formatNumber(r.TargetOpsPerSec), r.AvgDelay)
		}
	}

	log.Println("Generated: adaptive_summary.txt")
}

func formatNumber(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	} else if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
