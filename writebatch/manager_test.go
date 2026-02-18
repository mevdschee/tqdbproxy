package writebatch

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE test_writes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		data TEXT,
		value INTEGER
	)`)
	if err != nil {
		t.Fatal(err)
	}

	return db
}

func TestManager_SingleWrite(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	result := m.Enqueue(ctx, "test:1", "INSERT INTO test_writes (data) VALUES (?)", []interface{}{"test"}, 0)

	if result.Error != nil {
		t.Fatalf("Expected no error, got %v", result.Error)
	}

	if result.AffectedRows != 1 {
		t.Errorf("Expected 1 affected row, got %d", result.AffectedRows)
	}

	// Verify data was written
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test_writes WHERE data = 'test'").Scan(&count)
	if count != 1 {
		t.Errorf("Expected 1 row in database, got %d", count)
	}
}

func TestManager_BatchIdenticalQueries(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	results := make(chan WriteResult, 5)

	// Enqueue 5 identical queries rapidly
	for i := 0; i < 5; i++ {
		go func(n int) {
			result := m.Enqueue(ctx, "test:batch",
				"INSERT INTO test_writes (data, value) VALUES (?, ?)",
				[]interface{}{"batch", n}, 10)
			results <- result
		}(i)
	}

	// Collect results
	for i := 0; i < 5; i++ {
		result := <-results
		if result.Error != nil {
			t.Errorf("Result %d: unexpected error %v", i, result.Error)
		}
		if result.AffectedRows != 1 {
			t.Errorf("Result %d: expected 1 affected row, got %d", i, result.AffectedRows)
		}
	}

	// Verify all writes completed
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test_writes WHERE data = 'batch'").Scan(&count)
	if count != 5 {
		t.Errorf("Expected 5 writes, got %d", count)
	}
}

func TestManager_BatchMixedQueries(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	results := make(chan WriteResult, 3)

	// Enqueue mixed queries to same batch key
	go func() {
		result := m.Enqueue(ctx, "test:mixed",
			"INSERT INTO test_writes (data) VALUES (?)",
			[]interface{}{"insert"}, 10)
		results <- result
	}()

	go func() {
		// Different query - should trigger transaction batch
		result := m.Enqueue(ctx, "test:mixed",
			"INSERT INTO test_writes (data, value) VALUES (?, ?)",
			[]interface{}{"insert2", 42}, 10)
		results <- result
	}()

	// Collect results
	for i := 0; i < 2; i++ {
		result := <-results
		if result.Error != nil {
			t.Errorf("Result %d: unexpected error %v", i, result.Error)
		}
	}

	// Verify both writes completed
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test_writes").Scan(&count)
	if count != 2 {
		t.Errorf("Expected 2 writes, got %d", count)
	}
}

func TestManager_BatchSizeLimit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	config := DefaultConfig()
	config.MaxBatchSize = 10 // Small batch size for testing
	m := New(db, config)
	defer m.Close()

	ctx := context.Background()
	results := make(chan WriteResult, 15)

	// Enqueue 15 operations (should create 2 batches: 10 + 5)
	for i := 0; i < 15; i++ {
		go func(n int) {
			result := m.Enqueue(ctx, "test:limit",
				"INSERT INTO test_writes (data, value) VALUES (?, ?)",
				[]interface{}{"batch", n}, 100)
			results <- result
		}(i)
	}

	// Collect results
	for i := 0; i < 15; i++ {
		result := <-results
		if result.Error != nil {
			t.Errorf("Result %d: unexpected error %v", i, result.Error)
		}
	}

	// Verify all writes completed
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test_writes WHERE data = 'batch'").Scan(&count)
	if count != 15 {
		t.Errorf("Expected 15 writes, got %d", count)
	}
}

func TestManager_DelayTiming(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	start := time.Now()

	result := m.Enqueue(ctx, "test:timing",
		"INSERT INTO test_writes (data) VALUES (?)",
		[]interface{}{"timing"}, 50)

	elapsed := time.Since(start)

	if result.Error != nil {
		t.Fatalf("Expected no error, got %v", result.Error)
	}

	// Should take at least the delay time
	if elapsed < 50*time.Millisecond {
		t.Errorf("Expected delay of at least 50ms, got %v", elapsed)
	}

	// But not too much longer (allowing some overhead)
	if elapsed > 200*time.Millisecond {
		t.Errorf("Expected delay under 200ms, got %v", elapsed)
	}
}

func TestManager_ConcurrentEnqueues(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	numGoroutines := 50
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errors := make(chan error, numGoroutines)

	// Launch many concurrent enqueues
	for i := 0; i < numGoroutines; i++ {
		go func(n int) {
			defer wg.Done()
			result := m.Enqueue(ctx, "test:concurrent",
				"INSERT INTO test_writes (data, value) VALUES (?, ?)",
				[]interface{}{"concurrent", n}, 5)
			if result.Error != nil {
				errors <- result.Error
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Unexpected error: %v", err)
	}

	// Verify all writes completed
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test_writes WHERE data = 'concurrent'").Scan(&count)
	if count != numGoroutines {
		t.Errorf("Expected %d writes, got %d", numGoroutines, count)
	}
}

func TestManager_ContextCancellation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	result := m.Enqueue(ctx, "test:cancel",
		"INSERT INTO test_writes (data) VALUES (?)",
		[]interface{}{"cancelled"}, 100)

	if result.Error == nil {
		t.Error("Expected context cancellation error, got nil")
	}
}

func TestManager_Close(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())

	// Close the manager
	if err := m.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	// Try to enqueue after close
	ctx := context.Background()
	result := m.Enqueue(ctx, "test:closed",
		"INSERT INTO test_writes (data) VALUES (?)",
		[]interface{}{"closed"}, 0)

	if result.Error != ErrManagerClosed {
		t.Errorf("Expected ErrManagerClosed, got %v", result.Error)
	}
}

func TestManager_ErrorHandling(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()

	// Try to insert into non-existent table
	result := m.Enqueue(ctx, "test:error",
		"INSERT INTO nonexistent (data) VALUES (?)",
		[]interface{}{"error"}, 0)

	if result.Error == nil {
		t.Error("Expected error for invalid query, got nil")
	}
}

func BenchmarkManager_SingleWrite(b *testing.B) {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()

	db.Exec(`CREATE TABLE test_writes (id INTEGER PRIMARY KEY AUTOINCREMENT, data TEXT)`)

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Enqueue(ctx, "bench:single", "INSERT INTO test_writes (data) VALUES (?)", []interface{}{"bench"}, 0)
	}
}

func BenchmarkManager_BatchedWrites(b *testing.B) {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()

	db.Exec(`CREATE TABLE test_writes (id INTEGER PRIMARY KEY AUTOINCREMENT, data TEXT)`)

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.Enqueue(ctx, "bench:batch", "INSERT INTO test_writes (data) VALUES (?)", []interface{}{"bench"}, 1)
		}
	})
}
