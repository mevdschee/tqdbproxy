package writebatch

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestValidation_CorrectResultPropagation validates that results are correctly returned
func TestValidation_CorrectResultPropagation(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.InitialDelayMs = 5
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()

	// Insert and verify LastInsertID
	result := manager.Enqueue(ctx, "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{"test1"})
	if result.Error != nil {
		t.Fatalf("Insert failed: %v", result.Error)
	}

	if result.LastInsertID == 0 {
		t.Error("Expected non-zero LastInsertID")
	}

	if result.AffectedRows != 1 {
		t.Errorf("Expected 1 affected row, got %d", result.AffectedRows)
	}

	// Verify data was actually inserted
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count)
	if count != 1 {
		t.Errorf("Expected 1 row in table, got %d", count)
	}
}

// TestValidation_BatchIntegrity ensures all operations in a batch execute correctly
func TestValidation_BatchIntegrity(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.InitialDelayMs = 20 // Longer delay to ensure batching
	cfg.MaxBatchSize = 100
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()
	numOps := 50
	var wg sync.WaitGroup
	errors := make([]error, numOps)

	// Send operations concurrently
	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			query := "INSERT INTO test (value) VALUES (?)"
			result := manager.Enqueue(ctx, "INSERT", query, []interface{}{fmt.Sprintf("value%d", idx)})
			errors[idx] = result.Error
		}(i)
	}

	wg.Wait()

	// Check all succeeded
	for i, err := range errors {
		if err != nil {
			t.Errorf("Operation %d failed: %v", i, err)
		}
	}

	// Verify all data inserted
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count)
	if count != numOps {
		t.Errorf("Expected %d rows, got %d", numOps, count)
	}
}

// TestValidation_ErrorHandling tests error propagation from database
func TestValidation_ErrorHandling(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()

	// Insert a row
	result := manager.Enqueue(ctx, "INSERT", "INSERT INTO test (id, value) VALUES (?, ?)", []interface{}{1, "test"})
	if result.Error != nil {
		t.Fatalf("First insert failed: %v", result.Error)
	}

	// Try to insert duplicate (should fail)
	result = manager.Enqueue(ctx, "INSERT", "INSERT INTO test (id, value) VALUES (?, ?)", []interface{}{1, "duplicate"})
	if result.Error == nil {
		t.Error("Expected error for duplicate key, got nil")
	}
}

// TestValidation_ContextCancellation verifies context cancellation works
func TestValidation_ContextCancellation(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.InitialDelayMs = 100 // Long delay
	manager := New(db, cfg)
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// This should timeout
	result := manager.Enqueue(ctx, "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{"test"})

	// Should get timeout error
	if result.Error == nil {
		t.Error("Expected timeout error, got nil")
	}
}

// TestValidation_ManagerShutdown tests clean shutdown
func TestValidation_ManagerShutdown(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	manager := New(db, cfg)

	ctx := context.Background()

	// Enqueue some operations
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result := manager.Enqueue(ctx, "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{fmt.Sprintf("val%d", idx)})
			// May succeed or fail depending on timing
			_ = result
		}(i)
	}

	// Close manager while operations are pending
	time.Sleep(5 * time.Millisecond)
	err = manager.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	wg.Wait()

	// New enqueues should fail
	result := manager.Enqueue(ctx, "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{"after-close"})
	if result.Error != ErrManagerClosed {
		t.Errorf("Expected ErrManagerClosed, got %v", result.Error)
	}
}

// TestValidation_AdaptiveDelayBounds ensures adaptive delay stays within bounds
func TestValidation_AdaptiveDelayBounds(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		InitialDelayMs:  50,
		MaxDelayMs:      100,
		MinDelayMs:      10,
		MaxBatchSize:    1000,
		WriteThreshold:  100,
		AdaptiveStep:    2.0,
		MetricsInterval: 1,
	}

	manager := New(db, cfg)
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go manager.StartAdaptiveAdjustment(ctx)

	// Generate low load - should decrease delay
	for i := 0; i < 50; i++ {
		manager.Enqueue(context.Background(), "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{fmt.Sprintf("val%d", i)})
		time.Sleep(50 * time.Millisecond) // Very slow rate
	}

	time.Sleep(1500 * time.Millisecond) // Wait for adjustment

	delay := manager.GetCurrentDelay()
	if delay < float64(cfg.MinDelayMs) || delay > float64(cfg.MaxDelayMs) {
		t.Errorf("Delay %fms outside bounds [%d, %d]", delay, cfg.MinDelayMs, cfg.MaxDelayMs)
	}
}

// TestValidation_ConcurrentBatches tests multiple batch groups executing concurrently
func TestValidation_ConcurrentBatches(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT, type TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.InitialDelayMs = 20
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()
	var wg sync.WaitGroup

	// Send operations for different batch keys concurrently
	for batchType := 0; batchType < 3; batchType++ {
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(bt, idx int) {
				defer wg.Done()
				query := fmt.Sprintf("INSERT INTO test (value, type) VALUES (?, 'type%d')", bt)
				batchKey := fmt.Sprintf("INSERT_TYPE_%d", bt)
				result := manager.Enqueue(ctx, batchKey, query, []interface{}{fmt.Sprintf("val%d", idx)})
				if result.Error != nil {
					t.Errorf("Insert failed: %v", result.Error)
				}
			}(batchType, i)
		}
	}

	wg.Wait()

	// Verify all inserted
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count)
	if count != 30 {
		t.Errorf("Expected 30 rows, got %d", count)
	}
}

// TestValidation_HighConcurrency stress tests with many concurrent operations
func TestValidation_HighConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high concurrency test in short mode")
	}

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.MaxBatchSize = 500
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()
	numOps := 1000
	var wg sync.WaitGroup
	var successCount atomic.Int64
	var errorCount atomic.Int64

	start := time.Now()

	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result := manager.Enqueue(ctx, "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{fmt.Sprintf("val%d", idx)})
			if result.Error != nil {
				errorCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	t.Logf("Completed %d operations in %v (%.0f ops/sec)", numOps, duration, float64(numOps)/duration.Seconds())
	t.Logf("Success: %d, Errors: %d", successCount.Load(), errorCount.Load())

	if errorCount.Load() > 0 {
		t.Errorf("Expected 0 errors, got %d", errorCount.Load())
	}

	// Verify data integrity
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count)
	if count != numOps {
		t.Errorf("Expected %d rows, got %d", numOps, count)
	}
}

// TestValidation_BatchSizeLimit ensures batches don't exceed max size
func TestValidation_BatchSizeLimit(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.InitialDelayMs = 50
	cfg.MaxBatchSize = 10 // Small limit
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()
	numOps := 25 // More than max batch size
	var wg sync.WaitGroup

	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result := manager.Enqueue(ctx, "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{fmt.Sprintf("val%d", idx)})
			if result.Error != nil {
				t.Errorf("Insert failed: %v", result.Error)
			}
		}(i)
	}

	wg.Wait()

	// All should succeed despite batch size limit
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count)
	if count != numOps {
		t.Errorf("Expected %d rows, got %d", numOps, count)
	}
}

// TestValidation_DelayTiming verifies delay timing is accurate
func TestValidation_DelayTiming(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	delayMs := 50
	cfg := DefaultConfig()
	cfg.InitialDelayMs = delayMs
	cfg.MaxDelayMs = delayMs
	cfg.MinDelayMs = delayMs
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()

	start := time.Now()
	result := manager.Enqueue(ctx, "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{"test"})
	elapsed := time.Since(start)

	if result.Error != nil {
		t.Fatalf("Insert failed: %v", result.Error)
	}

	// Should take at least the delay time
	expectedMin := time.Duration(delayMs) * time.Millisecond
	if elapsed < expectedMin {
		t.Errorf("Operation completed too quickly: %v (expected >= %v)", elapsed, expectedMin)
	}

	// Should not take much longer (allowing 20ms overhead)
	expectedMax := time.Duration(delayMs+20) * time.Millisecond
	if elapsed > expectedMax {
		t.Logf("Warning: Operation took longer than expected: %v (expected ~%v)", elapsed, expectedMin)
	}
}

// TestValidation_MetricsAccuracy validates throughput tracking
func TestValidation_MetricsAccuracy(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	manager := New(db, cfg)
	defer manager.Close()

	ctx := context.Background()

	// Send operations at known rate
	numOps := 100
	interval := 10 * time.Millisecond
	start := time.Now()

	for i := 0; i < numOps; i++ {
		manager.Enqueue(ctx, "INSERT", "INSERT INTO test (value) VALUES (?)", []interface{}{fmt.Sprintf("val%d", i)})
		time.Sleep(interval)
	}

	duration := time.Since(start)

	// Force throughput update
	manager.forceThroughputUpdate()

	actualOpsPerSec := manager.GetOpsPerSecond()
	expectedOpsPerSec := float64(numOps) / duration.Seconds()

	t.Logf("Expected: %.0f ops/sec, Actual: %d ops/sec", expectedOpsPerSec, actualOpsPerSec)

	// Allow 20% variance
	if float64(actualOpsPerSec) < expectedOpsPerSec*0.8 || float64(actualOpsPerSec) > expectedOpsPerSec*1.2 {
		t.Errorf("Throughput tracking inaccurate: expected ~%.0f, got %d", expectedOpsPerSec, actualOpsPerSec)
	}
}
