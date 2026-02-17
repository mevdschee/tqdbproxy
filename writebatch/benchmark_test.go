package writebatch

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Benchmark comparing batched vs unbatched writes
func BenchmarkWriteBatching(b *testing.B) {
	// Create in-memory database
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	// Create test table
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		b.Fatal(err)
	}

	b.Run("Unbatched", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := db.Exec("INSERT INTO test (value) VALUES (?)", fmt.Sprintf("test%d", i))
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Batched_10", func(b *testing.B) {
		benchmarkBatchedInserts(b, db, 10)
	})

	b.Run("Batched_100", func(b *testing.B) {
		benchmarkBatchedInserts(b, db, 100)
	})

	b.Run("Batched_1000", func(b *testing.B) {
		benchmarkBatchedInserts(b, db, 1000)
	})
}

func benchmarkBatchedInserts(b *testing.B, db *sql.DB, batchSize int) {
	b.ResetTimer()

	// Process in batches
	for i := 0; i < b.N; i += batchSize {
		end := i + batchSize
		if end > b.N {
			end = b.N
		}

		// Build batch query
		query := "INSERT INTO test (value) VALUES "
		args := make([]interface{}, 0, end-i)
		for j := i; j < end; j++ {
			if j > i {
				query += ", "
			}
			query += "(?)"
			args = append(args, fmt.Sprintf("test%d", j))
		}

		_, err := db.Exec(query, args...)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark throughput at different load levels
func BenchmarkThroughput(b *testing.B) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		b.Fatal(err)
	}

	cfg := Config{
		InitialDelayMs:  5,
		MaxDelayMs:      50,
		MinDelayMs:      1,
		MaxBatchSize:    1000,
		WriteThreshold:  100,
		AdaptiveStep:    1.5,
		MetricsInterval: 1,
	}

	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()
	go manager.StartAdaptiveAdjustment(ctx)

	b.Run("Low_100ops", func(b *testing.B) {
		benchmarkThroughputLevel(b, manager, 100)
	})

	b.Run("Medium_1000ops", func(b *testing.B) {
		benchmarkThroughputLevel(b, manager, 1000)
	})

	b.Run("High_10000ops", func(b *testing.B) {
		benchmarkThroughputLevel(b, manager, 10000)
	})
}

func benchmarkThroughputLevel(b *testing.B, manager *Manager, numOps int) {
	b.ResetTimer()

	var wg sync.WaitGroup
	ctx := context.Background()

	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			query := "INSERT INTO test (value) VALUES (?)"
			batchKey := "INSERT INTO test (value) VALUES (?)"
			result := manager.Enqueue(ctx, batchKey, query, []interface{}{fmt.Sprintf("test%d", idx)})
			if result.Error != nil {
				b.Logf("Error: %v", result.Error)
			}
		}(i)
	}

	wg.Wait()
}

// Benchmark latency impact with different delays
func BenchmarkLatency(b *testing.B) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		b.Fatal(err)
	}

	delays := []int{1, 5, 10, 50, 100}

	for _, delay := range delays {
		b.Run(fmt.Sprintf("Delay_%dms", delay), func(b *testing.B) {
			cfg := Config{
				InitialDelayMs:  delay,
				MaxDelayMs:      delay,
				MinDelayMs:      delay,
				MaxBatchSize:    100,
				WriteThreshold:  10000,
				AdaptiveStep:    1.0,
				MetricsInterval: 60,
			}

			manager := New(db, cfg)
			defer manager.Close()

			ctx := context.Background()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				query := "INSERT INTO test (value) VALUES (?)"
				batchKey := "INSERT INTO test (value) VALUES (?)"
				result := manager.Enqueue(ctx, batchKey, query, []interface{}{fmt.Sprintf("test%d", i)})
				if result.Error != nil {
					b.Fatal(result.Error)
				}
			}
		})
	}
}

// Benchmark adaptive delay adjustment
func BenchmarkAdaptiveDelay(b *testing.B) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		b.Fatal(err)
	}

	cfg := Config{
		InitialDelayMs:  10,
		MaxDelayMs:      100,
		MinDelayMs:      1,
		MaxBatchSize:    1000,
		WriteThreshold:  1000,
		AdaptiveStep:    1.5,
		MetricsInterval: 1,
	}

	manager := New(db, cfg)
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go manager.StartAdaptiveAdjustment(ctx)

	// Wait for initial adjustment
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()

	// Simulate varying load
	for i := 0; i < b.N; i++ {
		query := "INSERT INTO test (value) VALUES (?)"
		batchKey := "INSERT INTO test (value) VALUES (?)"
		result := manager.Enqueue(context.Background(), batchKey, query, []interface{}{fmt.Sprintf("test%d", i)})
		if result.Error != nil {
			b.Fatal(result.Error)
		}

		// Every 100 ops, check adaptive delay
		if i > 0 && i%100 == 0 {
			currentDelay := manager.GetCurrentDelay()
			opsPerSec := manager.GetOpsPerSecond()
			b.Logf("Ops: %d, Delay: %.0fms, OPS/s: %d", i, currentDelay, opsPerSec)
		}
	}
}

// Benchmark concurrent enqueues
func BenchmarkConcurrentEnqueues(b *testing.B) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		b.Fatal(err)
	}

	cfg := DefaultConfig()
	manager := New(db, cfg)
	defer manager.Close()

	concurrencyLevels := []int{1, 10, 100, 1000}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("Concurrency_%d", concurrency), func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				ctx := context.Background()
				i := 0
				for pb.Next() {
					query := "INSERT INTO test (value) VALUES (?)"
					batchKey := "INSERT INTO test (value) VALUES (?)"
					result := manager.Enqueue(ctx, batchKey, query, []interface{}{fmt.Sprintf("test%d", i)})
					if result.Error != nil {
						b.Logf("Error: %v", result.Error)
					}
					i++
				}
			})
		})
	}
}

// Benchmark batch size impact
func BenchmarkBatchSizes(b *testing.B) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		b.Fatal(err)
	}

	batchSizes := []int{10, 50, 100, 500, 1000}

	for _, size := range batchSizes {
		b.Run(fmt.Sprintf("Size_%d", size), func(b *testing.B) {
			cfg := Config{
				InitialDelayMs:  5,
				MaxDelayMs:      50,
				MinDelayMs:      1,
				MaxBatchSize:    size,
				WriteThreshold:  10000,
				AdaptiveStep:    1.5,
				MetricsInterval: 60,
			}

			manager := New(db, cfg)
			defer manager.Close()

			ctx := context.Background()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				query := "INSERT INTO test (value) VALUES (?)"
				batchKey := "INSERT INTO test (value) VALUES (?)"
				result := manager.Enqueue(ctx, batchKey, query, []interface{}{fmt.Sprintf("test%d", i)})
				if result.Error != nil {
					b.Fatal(result.Error)
				}
			}
		})
	}
}

// Benchmark memory allocation
func BenchmarkMemoryAllocation(b *testing.B) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		b.Fatal(err)
	}

	cfg := DefaultConfig()
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		query := "INSERT INTO test (value) VALUES (?)"
		batchKey := "INSERT INTO test (value) VALUES (?)"
		result := manager.Enqueue(ctx, batchKey, query, []interface{}{fmt.Sprintf("test%d", i)})
		if result.Error != nil {
			b.Fatal(result.Error)
		}
	}
}
