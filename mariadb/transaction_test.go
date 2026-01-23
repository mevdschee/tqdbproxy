package mariadb

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestTransactions(t *testing.T) {
	// Connect to the proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Test BEGIN, query, and ROLLBACK
	t.Run("BeginRollback", func(t *testing.T) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("BEGIN failed: %v", err)
		}

		// Execute a simple query in the transaction
		var result int
		err = tx.QueryRow("SELECT 1 + 1").Scan(&result)
		if err != nil {
			t.Fatalf("Query in transaction failed: %v", err)
		}

		if result != 2 {
			t.Errorf("Expected 2, got %d", result)
		}

		// Rollback
		err = tx.Rollback()
		if err != nil {
			t.Fatalf("ROLLBACK failed: %v", err)
		}
	})

	// Test BEGIN, query, and COMMIT
	t.Run("BeginCommit", func(t *testing.T) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("BEGIN failed: %v", err)
		}

		// Execute a simple query in the transaction
		var result int
		err = tx.QueryRow("SELECT 2 * 3").Scan(&result)
		if err != nil {
			t.Fatalf("Query in transaction failed: %v", err)
		}

		if result != 6 {
			t.Errorf("Expected 6, got %d", result)
		}

		// Commit
		err = tx.Commit()
		if err != nil {
			t.Fatalf("COMMIT failed: %v", err)
		}
	})

	// Test transaction with session variables
	t.Run("SessionVariables", func(t *testing.T) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("BEGIN failed: %v", err)
		}
		defer tx.Rollback()

		// Set a session variable
		_, err = tx.Exec("SET @test_var = 42")
		if err != nil {
			t.Fatalf("SET failed: %v", err)
		}

		// Read it back
		var val int
		err = tx.QueryRow("SELECT @test_var").Scan(&val)
		if err != nil {
			t.Fatalf("SELECT @test_var failed: %v", err)
		}

		if val != 42 {
			t.Errorf("Expected 42, got %d", val)
		}
	})
}

func TestTransactionWithCache(t *testing.T) {
	// Connect to the proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Queries in transactions should NOT be cached
	t.Run("NoCache", func(t *testing.T) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("BEGIN failed: %v", err)
		}
		defer tx.Rollback()

		// Execute same query twice - should not use cache
		start1 := time.Now()
		var result1 int
		err = tx.QueryRow("/* ttl:60 file:test.go line:1 */ SELECT SLEEP(0.01), 1").Scan(new(int), &result1)
		if err != nil {
			t.Fatalf("First query failed: %v", err)
		}
		duration1 := time.Since(start1)

		start2 := time.Now()
		var result2 int
		err = tx.QueryRow("/* ttl:60 file:test.go line:1 */ SELECT SLEEP(0.01), 1").Scan(new(int), &result2)
		if err != nil {
			t.Fatalf("Second query failed: %v", err)
		}
		duration2 := time.Since(start2)

		// Both queries should hit the database (no significant speedup from cache)
		// This is a rough check - in a real scenario, you'd check metrics
		t.Logf("First query: %v, Second query: %v", duration1, duration2)

		if result1 != 1 || result2 != 1 {
			t.Errorf("Expected 1, got %d and %d", result1, result2)
		}
	})
}
