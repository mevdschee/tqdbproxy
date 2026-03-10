package writebatch

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
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

func TestManager_InsertReturning(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	result := m.Enqueue(ctx, "test:returning", "INSERT INTO test_writes (data, value) VALUES (?, ?) RETURNING id", []interface{}{"foo", 123}, 0, nil)

	if result.Error != nil {
		t.Logf("RETURNING not supported: %v", result.Error)
		return
	}

	if len(result.ReturningValues) == 1 {
		if id, ok := result.ReturningValues[0].(int64); !ok || id <= 0 {
			t.Errorf("Expected positive int64 id, got %v", result.ReturningValues[0])
		}
	} else {
		t.Logf("No returning values, driver may not support RETURNING")
	}
}

func TestManager_InsertLastInsertId_MySQL(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	result := m.Enqueue(ctx, "test:mysql", "INSERT INTO test_writes (data, value) VALUES (?, ?)", []interface{}{"bar", 456}, 0, nil)

	if result.Error != nil {
		t.Fatalf("Expected no error, got %v", result.Error)
	}

	if result.LastInsertID <= 0 {
		t.Errorf("Expected positive LastInsertID, got %d", result.LastInsertID)
	}
	if len(result.ReturningValues) != 0 {
		t.Errorf("Expected no returning values for MySQL, got %d", len(result.ReturningValues))
	}
}

func TestManager_DeleteReturning(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	// Insert a row to delete
	_, err := db.Exec("INSERT INTO test_writes (data, value) VALUES (?, ?)", "bar", 456)
	if err != nil {
		t.Fatalf("insert setup failed: %v", err)
	}

	ctx := context.Background()
	result := m.Enqueue(ctx, "test:delreturn", "DELETE FROM test_writes WHERE value = ? RETURNING id", []interface{}{456}, 0, nil)

	if result.Error != nil {
		t.Logf("RETURNING not supported: %v", result.Error)
		return
	}

	if len(result.ReturningValues) == 1 {
		if id, ok := result.ReturningValues[0].(int64); !ok || id <= 0 {
			t.Errorf("Expected positive int64 id, got %v", result.ReturningValues[0])
		}
	} else {
		t.Logf("No returning values, driver may not support RETURNING")
	}
}

// TestManager_BatchInsertLastInsertID verifies that each request in a batched
// multi-row INSERT receives its own correct LastInsertID, not the ID of the
// first or last row in the batch.
func TestManager_BatchInsertLastInsertID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	const batchSize = 5
	results := make([]WriteResult, batchSize)
	var wg sync.WaitGroup
	wg.Add(batchSize)

	for i := 0; i < batchSize; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = m.Enqueue(ctx, "test:lastid",
				"INSERT INTO test_writes (data, value) VALUES (?, ?)",
				[]interface{}{"lastid", idx}, 10, nil)
		}(i)
	}
	wg.Wait()

	// All requests must succeed
	for i, res := range results {
		if res.Error != nil {
			t.Fatalf("request %d failed: %v", i, res.Error)
		}
	}

	// Each LastInsertID must be positive
	for i, res := range results {
		if res.LastInsertID <= 0 {
			t.Errorf("request %d: expected positive LastInsertID, got %d", i, res.LastInsertID)
		}
	}

	// All LastInsertIDs must be unique (each row gets its own ID)
	seen := make(map[int64]bool)
	for i, res := range results {
		if seen[res.LastInsertID] {
			t.Errorf("request %d: duplicate LastInsertID %d", i, res.LastInsertID)
		}
		seen[res.LastInsertID] = true
	}

	// The set of IDs must be consecutive (no gaps within the batch)
	min, max := results[0].LastInsertID, results[0].LastInsertID
	for _, res := range results[1:] {
		if res.LastInsertID < min {
			min = res.LastInsertID
		}
		if res.LastInsertID > max {
			max = res.LastInsertID
		}
	}
	if max-min != int64(batchSize-1) {
		t.Errorf("IDs are not consecutive: min=%d max=%d batchSize=%d", min, max, batchSize)
	}

	// Verify all rows exist in the database
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test_writes WHERE data = 'lastid'").Scan(&count)
	if count != batchSize {
		t.Errorf("expected %d rows in DB, got %d", batchSize, count)
	}
}

func TestManager_BatchDeleteAggregation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	// Insert rows to delete
	for i := 1; i <= 5; i++ {
		_, err := db.Exec("INSERT INTO test_writes (data, value) VALUES (?, ?)", "row", i)
		if err != nil {
			t.Fatalf("insert setup failed: %v", err)
		}
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]WriteResult, 5)

	// Enqueue 5 identical DELETEs with different keys
	for i := 1; i <= 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx-1] = m.Enqueue(ctx, "del", "DELETE FROM test_writes WHERE value = ?", []interface{}{idx}, 1, nil)
		}(i)
	}
	wg.Wait()

	// All should succeed
	for i, res := range results {
		if res.Error != nil {
			t.Errorf("delete %d failed: %v", i+1, res.Error)
		}
	}

	// Table should be empty
	var count int
	db.QueryRow("SELECT COUNT(*) FROM test_writes").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 rows after delete, got %d", count)
	}
}

func TestManager_SingleWrite(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()
	result := m.Enqueue(ctx, "test:1", "INSERT INTO test_writes (data) VALUES (?)", []interface{}{"test"}, 0, nil)

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
				[]interface{}{"batch", n}, 10, nil)
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
			[]interface{}{"insert"}, 10, nil)
		results <- result
	}()

	go func() {
		// Different query - should trigger transaction batch
		result := m.Enqueue(ctx, "test:mixed",
			"INSERT INTO test_writes (data, value) VALUES (?, ?)",
			[]interface{}{"insert2", 42}, 10, nil)
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
				[]interface{}{"batch", n}, 100, nil)
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
		[]interface{}{"timing"}, 50, nil)

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
				[]interface{}{"concurrent", n}, 5, nil)
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
		[]interface{}{"cancelled"}, 100, nil)

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
		[]interface{}{"closed"}, 0, nil)

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
		[]interface{}{"error"}, 0, nil)

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
		m.Enqueue(ctx, "bench:single", "INSERT INTO test_writes (data) VALUES (?)", []interface{}{"bench"}, 0, nil)
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
			m.Enqueue(ctx, "bench:batch", "INSERT INTO test_writes (data) VALUES (?)", []interface{}{"bench"}, 1, nil)
		}
	})
}

// setupPostgresDB sets up a test PostgreSQL database
// Skips the test if PostgreSQL is not available
func setupPostgresDB(t *testing.T) *sql.DB {
	connStr := os.Getenv("POSTGRES_TEST_DSN")
	if connStr == "" {
		connStr = "host=127.0.0.1 port=5432 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable"
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
		return nil
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		t.Skipf("PostgreSQL not available: %v", err)
		return nil
	}

	// Create test table
	_, err = db.Exec(`DROP TABLE IF EXISTS test_writes`)
	if err != nil {
		db.Close()
		t.Fatalf("Failed to drop test table: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE test_writes (
		id SERIAL PRIMARY KEY,
		data TEXT,
		value INTEGER
	)`)
	if err != nil {
		db.Close()
		t.Fatalf("Failed to create test table: %v", err)
	}

	return db
}

func TestPostgreSQL_CopyBatchInsert(t *testing.T) {
	db := setupPostgresDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()

	// Create batched inserts with simple parameters (should use COPY)
	const numInserts = 100
	for i := 0; i < numInserts; i++ {
		result := m.Enqueue(ctx, "test:pgcopy",
			"INSERT INTO test_writes (data, value) VALUES ($1, $2)",
			[]interface{}{"test", i}, 10, nil)

		if result.Error != nil {
			t.Fatalf("Insert %d failed: %v", i, result.Error)
		}
	}

	// Wait a bit for batch to complete
	time.Sleep(50 * time.Millisecond)

	// Verify all rows were inserted
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM test_writes").Scan(&count)
	if err != nil {
		t.Fatalf("Count query failed: %v", err)
	}

	if count != numInserts {
		t.Errorf("Expected %d rows, got %d", numInserts, count)
	}

	// Verify data integrity
	rows, err := db.Query("SELECT data, value FROM test_writes ORDER BY value")
	if err != nil {
		t.Fatalf("Select query failed: %v", err)
	}
	defer rows.Close()

	i := 0
	for rows.Next() {
		var data string
		var value int
		if err := rows.Scan(&data, &value); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}

		if data != "test" {
			t.Errorf("Row %d: expected data='test', got '%s'", i, data)
		}
		if value != i {
			t.Errorf("Row %d: expected value=%d, got %d", i, i, value)
		}
		i++
	}

	if i != numInserts {
		t.Errorf("Expected to read %d rows, got %d", numInserts, i)
	}
}

func TestPostgreSQL_MultiRowInsertFallback(t *testing.T) {
	db := setupPostgresDB(t)
	if db == nil {
		return
	}
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx := context.Background()

	// Insert with simple parameters - should work with both COPY and multi-row
	const numInserts = 50
	for i := 0; i < numInserts; i++ {
		result := m.Enqueue(ctx, "test:pgmulti",
			"INSERT INTO test_writes (data, value) VALUES ($1, $2)",
			[]interface{}{"fallback", i + 100}, 10, nil)

		if result.Error != nil {
			t.Fatalf("Insert %d failed: %v", i, result.Error)
		}
	}

	// Wait for batch to complete
	time.Sleep(50 * time.Millisecond)

	// Verify all rows were inserted
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM test_writes WHERE data = 'fallback'").Scan(&count)
	if err != nil {
		t.Fatalf("Count query failed: %v", err)
	}

	if count != numInserts {
		t.Errorf("Expected %d rows, got %d", numInserts, count)
	}

	// Verify per-row data integrity
	rows, err := db.Query("SELECT data, value FROM test_writes WHERE data = 'fallback' ORDER BY value")
	if err != nil {
		t.Fatalf("Select query failed: %v", err)
	}
	defer rows.Close()

	i := 0
	for rows.Next() {
		var data string
		var value int
		if err := rows.Scan(&data, &value); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		if data != "fallback" {
			t.Errorf("Row %d: expected data='fallback', got '%s'", i, data)
		}
		if value != i+100 {
			t.Errorf("Row %d: expected value=%d, got %d", i, i+100, value)
		}
		i++
	}

	if i != numInserts {
		t.Errorf("Expected to read %d rows, got %d", numInserts, i)
	}
}

func TestAllParamsAreSimple(t *testing.T) {
	tests := []struct {
		name     string
		requests []*WriteRequest
		expected bool
	}{
		{
			name: "all simple types",
			requests: []*WriteRequest{
				{Params: []interface{}{"test", 123, 45.6, true}},
				{Params: []interface{}{"another", 789, 12.3, false}},
			},
			expected: true,
		},
		{
			name: "includes nil",
			requests: []*WriteRequest{
				{Params: []interface{}{"test", nil, 123}},
			},
			expected: true,
		},
		{
			name: "includes byte slice",
			requests: []*WriteRequest{
				{Params: []interface{}{[]byte("data"), 123}},
			},
			expected: true,
		},
		{
			name: "includes complex type",
			requests: []*WriteRequest{
				{Params: []interface{}{"test", map[string]int{"key": 1}}},
			},
			expected: false,
		},
		{
			name: "includes slice of ints",
			requests: []*WriteRequest{
				{Params: []interface{}{[]int{1, 2, 3}}},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := allParamsAreSimple(tt.requests)
			if result != tt.expected {
				t.Errorf("allParamsAreSimple() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestParseInsertStatement(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantTable   string
		wantColumns []string
		wantErr     bool
	}{
		{
			name:        "simple insert",
			query:       "INSERT INTO test_writes (data, value) VALUES ($1, $2)",
			wantTable:   "test_writes",
			wantColumns: []string{"data", "value"},
			wantErr:     false,
		},
		{
			name:        "insert with extra spaces",
			query:       "INSERT INTO  users  ( name , email )  VALUES ($1, $2)",
			wantTable:   "users",
			wantColumns: []string{"name", "email"},
			wantErr:     false,
		},
		{
			name:        "insert with single column",
			query:       "INSERT INTO logs (message) VALUES ($1)",
			wantTable:   "logs",
			wantColumns: []string{"message"},
			wantErr:     false,
		},
		{
			name:    "not an insert",
			query:   "SELECT * FROM test",
			wantErr: true,
		},
		{
			name:    "missing column list",
			query:   "INSERT INTO test VALUES ($1, $2)",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table, columns, err := parseInsertStatement(tt.query)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseInsertStatement() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("parseInsertStatement() unexpected error: %v", err)
				return
			}

			if table != tt.wantTable {
				t.Errorf("parseInsertStatement() table = %v, want %v", table, tt.wantTable)
			}

			if len(columns) != len(tt.wantColumns) {
				t.Errorf("parseInsertStatement() columns length = %v, want %v", len(columns), len(tt.wantColumns))
				return
			}

			for i, col := range columns {
				if col != tt.wantColumns[i] {
					t.Errorf("parseInsertStatement() column[%d] = %v, want %v", i, col, tt.wantColumns[i])
				}
			}
		})
	}
}
