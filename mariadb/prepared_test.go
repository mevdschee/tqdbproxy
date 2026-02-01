package mariadb

import (
	"database/sql"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

func TestPreparedStatements(t *testing.T) {
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Try to prepare a statement
	stmt, err := db.Prepare("SELECT 1")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer stmt.Close()

	// Try to execute it
	var val int
	err = stmt.QueryRow().Scan(&val)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if val != 1 {
		t.Errorf("Expected 1, got %d", val)
	}
}

func TestPreparedStatements_Caching(t *testing.T) {
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// 1. First execution (miss)
	var val1 int
	err = db.QueryRow("/* ttl:10 */ SELECT ?", 42).Scan(&val1)
	if err != nil {
		t.Fatalf("First execute failed: %v", err)
	}

	// 2. Second execution (should be cache hit)
	var val2 int
	err = db.QueryRow("/* ttl:10 */ SELECT ?", 42).Scan(&val2)
	if err != nil {
		t.Fatalf("Second execute failed: %v", err)
	}

	if val1 != 42 || val2 != 42 {
		t.Errorf("Expected 42, got %d and %d", val1, val2)
	}

	// Double check cache hit by looking at TQDB STATUS
	var varName, value string
	err = db.QueryRow("SHOW TQDB STATUS").Scan(&varName, &value)
	if err != nil {
		t.Fatalf("SHOW TQDB STATUS failed: %v", err)
	}
	// We check for "Backend" row. Value should be "cache"
	// Wait, we need to iterate rows to find "Backend"
	rows, err := db.Query("SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("SHOW TQDB STATUS query failed: %v", err)
	}
	defer rows.Close()
	foundCache := false
	for rows.Next() {
		if err := rows.Scan(&varName, &value); err != nil {
			t.Fatal(err)
		}
		if varName == "Backend" && value == "cache" {
			foundCache = true
		}
	}
	if !foundCache {
		t.Errorf("Expected cache hit for prepared statement, but Backend was not 'cache'")
	}
}

func TestPreparedStatements_ResetClose(t *testing.T) {
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// 1. Prepare
	stmt, err := db.Prepare("SELECT ? + ?")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	// 2. Execute 1
	var sum int
	err = stmt.QueryRow(10, 20).Scan(&sum)
	if err != nil {
		t.Fatalf("Execute 1 failed: %v", err)
	}
	if sum != 30 {
		t.Errorf("Expected 30, got %d", sum)
	}

	// 3. Execute 2 (different params, same stmt - might trigger Reset depending on driver)
	err = stmt.QueryRow(5, 5).Scan(&sum)
	if err != nil {
		t.Fatalf("Execute 2 failed: %v", err)
	}
	if sum != 10 {
		t.Errorf("Expected 10, got %d", sum)
	}

	// 4. Close
	err = stmt.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// After close, trying to use it should fail (driver level check mostly, but proxy should have cleaned up too)
}
