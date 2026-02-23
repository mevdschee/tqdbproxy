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

	_ "github.com/lib/pq"
	"github.com/mevdschee/tqdbproxy/writebatch"
)

func runExtremeTest() {
	log.Println("=== Extreme Throughput Test (PostgreSQL with Batched Inserts) ===")
	log.Println("Configuration:")
	log.Println("  - Delay: 100ms (maximum batching)")
	log.Println("  - Batch Size: 5000")
	log.Println("  - Workers: 25000 (high concurrency)")
	log.Println("  - Backend: PostgreSQL with actual batched INSERT statements")
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

	// Create PostgreSQL database connection
	connStr := "host=127.0.0.1 port=5432 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable"
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Drop table if exists and create fresh
	db.Exec("DROP TABLE IF EXISTS test")
	_, err = db.Exec("CREATE TABLE test (id SERIAL PRIMARY KEY, value INTEGER, created_at BIGINT)")
	if err != nil {
		log.Fatal(err)
	}

	// Configure for maximum throughput
	cfg := writebatch.Config{
		MaxBatchSize: 2500,
		//UseCopy:      true, // Enable PostgreSQL COPY for comparison
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
	numWorkers := 25000 // Massive concurrency to saturate the system
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
			query := "INSERT INTO test (value, created_at) VALUES ($1, $2)"
			batchKey := "INSERT"
			params := []interface{}{workerID, int64(0)}

			// Tight loop - minimize allocations
			for time.Now().Before(endTime) {
				params[1] = time.Now().Unix()
				result := manager.Enqueue(bgCtx, batchKey, query, params, 100, nil)

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
