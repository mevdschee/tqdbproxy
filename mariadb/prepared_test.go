package mariadb

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

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

	// 1. Prepare (using a unique comment to avoid collisions with previous runs)
	stmt, err := db.Prepare(fmt.Sprintf("/* ttl:10 */ SELECT ? -- %d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer stmt.Close()

	// 1. First execution (miss)
	var val1 int
	err = stmt.QueryRow(42).Scan(&val1)
	if err != nil {
		t.Fatalf("First execute failed: %v", err)
	}

	// 2. Second execution (should be cache hit)
	var val2 int
	err = stmt.QueryRow(42).Scan(&val2)
	if err != nil {
		t.Fatalf("Second execute failed: %v", err)
	}

	if val1 != 42 || val2 != 42 {
		t.Errorf("Expected 42, got %d and %d", val1, val2)
	}

	// Verify hit
	if !isCacheHit(t, db) {
		t.Errorf("Expected cache hit for same parameter")
	}

	// 3. Third execution with different param (should be cache miss)
	var val3 int
	err = stmt.QueryRow(43).Scan(&val3)
	if err != nil {
		t.Fatalf("Third execute failed: %v", err)
	}
	if isCacheHit(t, db) {
		t.Errorf("Expected cache miss for different parameter (43)")
	}
}

func isCacheHit(t *testing.T, db *sql.DB) bool {
	rows, err := db.Query("SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("SHOW TQDB STATUS query failed: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var varName, value string
		if err := rows.Scan(&varName, &value); err != nil {
			t.Fatal(err)
		}
		if varName == "Backend" && strings.HasPrefix(value, "cache") {
			return true
		}
	}
	return false
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
func TestPreparedStatements_CrossSession(t *testing.T) {
	// 1. Session A: Executes and caches
	db1, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db1.Close()

	var val1 int
	err = db1.QueryRow("/* ttl:10 */ SELECT 1212").Scan(&val1)
	if err != nil {
		t.Fatalf("Session A execute failed: %v", err)
	}

	// 2. Session B: Should hit cache even with different connection/session
	db2, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to second session: %v", err)
	}
	defer db2.Close()

	var val2 int
	err = db2.QueryRow("/* ttl:10 */ SELECT 1212").Scan(&val2)
	if err != nil {
		t.Fatalf("Session B execute failed: %v", err)
	}

	if !isCacheHit(t, db2) {
		t.Errorf("Expected cross-session cache hit")
	}
}
func TestPreparedStatements_MultipleIDs(t *testing.T) {
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// 1. Prepare multiple different statements
	stmt1, err := db.Prepare("SELECT 101")
	if err != nil {
		t.Fatalf("Prepare 1 failed: %v", err)
	}
	defer stmt1.Close()

	stmt2, err := db.Prepare("SELECT 202")
	if err != nil {
		t.Fatalf("Prepare 2 failed: %v", err)
	}
	defer stmt2.Close()

	stmt3, err := db.Prepare("SELECT ? + 303")
	if err != nil {
		t.Fatalf("Prepare 3 failed: %v", err)
	}
	defer stmt3.Close()

	// 2. Execute them in interleaved fashion to test ID routing
	var v1, v2, v3 int

	if err := stmt3.QueryRow(100).Scan(&v3); err != nil {
		t.Fatalf("Execute 3 failed: %v", err)
	}
	if v3 != 403 {
		t.Errorf("Expected 403, got %d", v3)
	}

	if err := stmt1.QueryRow().Scan(&v1); err != nil {
		t.Fatalf("Execute 1 failed: %v", err)
	}
	if v1 != 101 {
		t.Errorf("Expected 101, got %d", v1)
	}

	if err := stmt2.QueryRow().Scan(&v2); err != nil {
		t.Fatalf("Execute 2 failed: %v", err)
	}
	if v2 != 202 {
		t.Errorf("Expected 202, got %d", v2)
	}
}
