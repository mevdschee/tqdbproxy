package mariadb

import (
	"database/sql"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

func TestDatabaseSelectionFromDSN(t *testing.T) {
	// Connect with database specified in DSN
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Verify connection works
	if err := db.Ping(); err != nil {
		t.Fatalf("Failed to ping: %v", err)
	}

	// This should work if database was selected - it requires a database context
	_, err = db.Exec("DROP TABLE IF EXISTS test_db_selection")
	if err != nil {
		t.Fatalf("DROP TABLE failed (database not selected?): %v", err)
	}

	// Create a table - this also requires database to be selected
	_, err = db.Exec("CREATE TABLE test_db_selection (id INT PRIMARY KEY, value VARCHAR(100))")
	if err != nil {
		t.Fatalf("CREATE TABLE failed (database not selected?): %v", err)
	}

	// Insert data to verify table exists and is usable
	_, err = db.Exec("INSERT INTO test_db_selection (id, value) VALUES (1, 'test')")
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	// Query to verify
	var value string
	err = db.QueryRow("SELECT value FROM test_db_selection WHERE id = 1").Scan(&value)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}

	if value != "test" {
		t.Errorf("Expected 'test', got %q", value)
	}

	// Cleanup
	db.Exec("DROP TABLE test_db_selection")
}

func TestDatabaseSelectionWithoutDSN(t *testing.T) {
	// Connect WITHOUT database in DSN
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Verify connection works
	if err := db.Ping(); err != nil {
		t.Fatalf("Failed to ping: %v", err)
	}

	// This SHOULD fail because no database is selected
	_, err = db.Exec("DROP TABLE IF EXISTS test_table")
	if err == nil {
		t.Fatal("Expected error when no database selected, but got none")
	}

	// Error should mention "No database selected"
	if err.Error() != "Error 1046 (3D000): No database selected" {
		t.Logf("Got expected error (different format): %v", err)
	}

	// Now select a database explicitly
	_, err = db.Exec("USE tqdbproxy")
	if err != nil {
		t.Fatalf("USE database failed: %v", err)
	}

	// This should now work
	_, err = db.Exec("DROP TABLE IF EXISTS test_db_selection2")
	if err != nil {
		t.Fatalf("DROP TABLE failed after USE: %v", err)
	}

	// Cleanup
	db.Exec("DROP TABLE IF EXISTS test_db_selection2")
}
