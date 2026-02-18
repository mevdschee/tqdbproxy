package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mevdschee/tqdbproxy/writebatch"
)

func runExtremeTest() {
	log.Println("=== Extreme Throughput Test (SQLite with Batched Inserts) ===")
	log.Println("Configuration:")
	log.Println("  - Delay: 100ms (maximum batching)")
	log.Println("  - Batch Size: 1000")
	log.Println("  - Workers: 50000 (high concurrency)")
	log.Println("  - Backend: SQLite with actual batched INSERT statements")
	log.Println("  - Optimization: Reduced allocations, reused contexts")
	log.Println("  - Target: >1M ops/sec")
	log.Println()

	// Start CPU profiling
	cpuProfile, err := os.Create("cpu.prof")
	if err != nil {
		log.Fatal("Could not create CPU profile: ", err)
	}
	defer cpuProfile.Close()

	if err := pprof.StartCPUProfile(cpuProfile); err != nil {
		log.Fatal("Could not start CPU profile: ", err)
	}
	defer pprof.StopCPUProfile()

	log.Println("CPU profiling enabled -> cpu.prof")
	log.Println()

	// Create SQLite database
	db, err := sql.Open("sqlite3", "file:test.db?cache=shared&mode=memory")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Create table
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value INTEGER, created_at INTEGER)")
	if err != nil {
		log.Fatal(err)
	}

	// Optimize SQLite for performance
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA synchronous=NORMAL")
	db.Exec("PRAGMA cache_size=10000")
	db.Exec("PRAGMA temp_store=MEMORY")

	// Configure for maximum throughput
	cfg := writebatch.Config{
		InitialDelayMs:  100, // Longer delay for maximum batching
		MaxDelayMs:      100,
		MinDelayMs:      100,
		MaxBatchSize:    1000,
		WriteThreshold:  2000000,
		AdaptiveStep:    1.0,
		MetricsInterval: 1,
	}

	manager := writebatch.New(db, cfg)
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start metrics collection
	var totalOps atomic.Int64
	var lastOps int64
	var peakOps uint64

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := totalOps.Load()
				opsThisSec := uint64(current - lastOps)
				lastOps = current
				if opsThisSec > peakOps {
					peakOps = opsThisSec
				}
				log.Printf("  Current: %s ops/sec, Total: %s ops, Peak: %s ops/sec",
					formatLarge(opsThisSec), formatLarge(uint64(current)), formatLarge(peakOps))
			}
		}
	}()

	// Generate load with many workers in tight loops
	duration := 30 * time.Second
	numWorkers := 50000 // Massive concurrency to saturate the system
	var wg sync.WaitGroup

	startTime := time.Now()
	endTime := startTime.Add(duration)

	log.Printf("Starting %d workers (massive concurrency) for %v...\n", numWorkers, duration)

	// Pre-allocate reusable context to reduce allocation overhead
	bgCtx := context.Background()

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Pre-allocate and reuse to reduce GC pressure
			query := "INSERT INTO test (value, created_at) VALUES (?, ?)"
			batchKey := "INSERT"
			params := []interface{}{workerID, int64(0)}

			// Tight loop - minimize allocations
			for time.Now().Before(endTime) {
				params[1] = time.Now().Unix()
				result := manager.Enqueue(bgCtx, batchKey, query, params)

				if result.Error == nil {
					totalOps.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()
	cancel()

	// Final stats
	elapsed := time.Since(startTime)
	total := totalOps.Load()
	avgOpsPerSec := float64(total) / elapsed.Seconds()

	log.Println()
	log.Println("=== Final Results ===")
	log.Printf("Duration:           %v", elapsed)
	log.Printf("Total Operations:   %s", formatLarge(uint64(total)))
	log.Printf("Average Throughput: %s ops/sec", formatLarge(uint64(avgOpsPerSec)))
	log.Printf("Peak Throughput:    %s ops/sec", formatLarge(peakOps))
	log.Println()

	if avgOpsPerSec >= 1000000 {
		log.Println("✓ SUCCESS: Achieved >1M ops/sec average throughput!")
	} else {
		log.Printf("✗ Target not reached: %.0f%% of 1M ops/sec target",
			(avgOpsPerSec/1000000)*100)
	}

	log.Println()
	log.Println("CPU profile saved to: cpu.prof")
	log.Println("Analyze with: go tool pprof -http=:8080 cpu.prof")
	log.Println("Or text view: go tool pprof -top cpu.prof")
}

func formatLarge(n uint64) string {
	if n >= 1000000000 {
		return fmt.Sprintf("%.2fB", float64(n)/1000000000)
	} else if n >= 1000000 {
		return fmt.Sprintf("%.2fM", float64(n)/1000000)
	} else if n >= 1000 {
		return fmt.Sprintf("%.2fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func main() {
	runExtremeTest()
}
