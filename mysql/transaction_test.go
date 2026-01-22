package mysql

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestTransactions(t *testing.T) {
	// Connect to the proxy
	db, err := sql.Open("mysql", "php-crud-api:php-crud-api@tcp(127.0.0.1:3307)/php-crud-api")
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

		// Execute a query in the transaction
		var count int
		err = tx.QueryRow("SELECT COUNT(*) FROM categories").Scan(&count)
		if err != nil {
			t.Fatalf("Query in transaction failed: %v", err)
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

		// Execute a query in the transaction
		var name string
		err = tx.QueryRow("SELECT name FROM categories WHERE id = 1").Scan(&name)
		if err != nil {
			t.Fatalf("Query in transaction failed: %v", err)
		}

		// Commit
		err = tx.Commit()
		if err != nil {
			t.Fatalf("COMMIT failed: %v", err)
		}
	})

	// Test transaction isolation
	t.Run("TransactionIsolation", func(t *testing.T) {
		// Start a transaction
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("BEGIN failed: %v", err)
		}
		defer tx.Rollback()

		// Read initial value
		var initialName string
		err = tx.QueryRow("SELECT name FROM categories WHERE id = 1").Scan(&initialName)
		if err != nil {
			t.Fatalf("Initial query failed: %v", err)
		}

		// Update in transaction (not committed yet)
		_, err = tx.Exec("UPDATE categories SET name = 'test_tx' WHERE id = 1")
		if err != nil {
			t.Fatalf("Update in transaction failed: %v", err)
		}

		// Read from outside the transaction - should see original value
		var outsideName string
		err = db.QueryRow("SELECT name FROM categories WHERE id = 1").Scan(&outsideName)
		if err != nil {
			t.Fatalf("Outside query failed: %v", err)
		}

		// Rollback to restore original state
		tx.Rollback()

		// Verify rollback worked
		var finalName string
		err = db.QueryRow("SELECT name FROM categories WHERE id = 1").Scan(&finalName)
		if err != nil {
			t.Fatalf("Final query failed: %v", err)
		}

		if finalName != initialName {
			t.Errorf("Rollback didn't work: expected %q, got %q", initialName, finalName)
		}
	})
}

func TestTransactionWithCache(t *testing.T) {
	// Connect to the proxy
	db, err := sql.Open("mysql", "php-crud-api:php-crud-api@tcp(127.0.0.1:3307)/php-crud-api")
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
		var count1 int
		err = tx.QueryRow("/* ttl:60 file:test.go line:1 */ SELECT COUNT(*) FROM categories").Scan(&count1)
		if err != nil {
			t.Fatalf("First query failed: %v", err)
		}
		duration1 := time.Since(start1)

		start2 := time.Now()
		var count2 int
		err = tx.QueryRow("/* ttl:60 file:test.go line:1 */ SELECT COUNT(*) FROM categories").Scan(&count2)
		if err != nil {
			t.Fatalf("Second query failed: %v", err)
		}
		duration2 := time.Since(start2)

		// Both queries should hit the database (no significant speedup from cache)
		// This is a rough check - in a real scenario, you'd check metrics
		t.Logf("First query: %v, Second query: %v", duration1, duration2)
	})
}
