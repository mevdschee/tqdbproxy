package postgres

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestBatchSizeReporting(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("postgres", "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	// Set max connections to 1 to ensure we use the same connection
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_test (id SERIAL PRIMARY KEY, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_test")

	// Use direct exec with batch hint (not prepared statements, as they don't support batching yet)
	numQueries := 10
	for i := 0; i < numQueries; i++ {
		_, err := db.Exec(fmt.Sprintf("/* batch:1000 */ INSERT INTO batch_test (value) VALUES (%d)", i))
		if err != nil {
			t.Fatalf("Failed to execute query %d: %v", i, err)
		}
	}

	// Wait a moment for batch to flush (batch:1000 means 1000ms window)
	time.Sleep(1500 * time.Millisecond)

	// Query pg_tqdb_status() to get batch size
	rows, err := db.Query("SELECT * FROM pg_tqdb_status()")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}
	defer rows.Close()

	var batchSize int
	var foundBatchSize bool

	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}

		t.Logf("TQDB Status: %s = %s", variable, value)

		if variable == "LastBatchSize" {
			fmt.Sscanf(value, "%d", &batchSize)
			foundBatchSize = true
		}
	}

	if !foundBatchSize {
		t.Fatalf("LastBatchSize not found in pg_tqdb_status()")
	}

	if batchSize != numQueries {
		t.Errorf("Expected batch size %d, got %d", numQueries, batchSize)
	}

	t.Logf("✓ Batch size correctly reported as %d", batchSize)
}

func TestBatchSizeWithMultipleBatches(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("postgres", "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	// Set max connections to 1 to ensure we use the same connection
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_test (id SERIAL PRIMARY KEY, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_test")

	// Execute first batch with batch:100 hint (100ms window)
	firstBatchSize := 5
	for i := 0; i < firstBatchSize; i++ {
		_, err := db.Exec(fmt.Sprintf("/* batch:100 */ INSERT INTO batch_test (value) VALUES (%d)", i))
		if err != nil {
			t.Fatalf("Failed to execute query %d: %v", i, err)
		}
	}

	// Wait for first batch to flush
	time.Sleep(150 * time.Millisecond)

	// Check batch size
	rows, err := db.Query("SELECT * FROM pg_tqdb_status()")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}

	var firstReportedSize int
	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			rows.Close()
			t.Fatalf("Failed to scan row: %v", err)
		}
		if variable == "LastBatchSize" {
			fmt.Sscanf(value, "%d", &firstReportedSize)
		}
	}
	rows.Close()

	if firstReportedSize != firstBatchSize {
		t.Errorf("First batch: expected size %d, got %d", firstBatchSize, firstReportedSize)
	}

	// Execute second batch with different size
	secondBatchSize := 8
	for i := 0; i < secondBatchSize; i++ {
		_, err := db.Exec(fmt.Sprintf("/* batch:100 */ INSERT INTO batch_test (value) VALUES (%d)", i+100))
		if err != nil {
			t.Fatalf("Failed to execute query %d in second batch: %v", i, err)
		}
	}

	// Wait for second batch to flush
	time.Sleep(150 * time.Millisecond)

	// Check batch size again
	rows, err = db.Query("SELECT * FROM pg_tqdb_status()")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}
	defer rows.Close()

	var secondReportedSize int
	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}
		if variable == "LastBatchSize" {
			fmt.Sscanf(value, "%d", &secondReportedSize)
		}
	}

	if secondReportedSize != secondBatchSize {
		t.Errorf("Second batch: expected size %d, got %d", secondBatchSize, secondReportedSize)
	}

	t.Logf("✓ First batch size: %d, Second batch size: %d", firstReportedSize, secondReportedSize)
}
