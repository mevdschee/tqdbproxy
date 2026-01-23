package mariadb

import (
	"database/sql"
	"fmt"
	"math/rand"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

func TestCacheHit(t *testing.T) {
	// Connect to the proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	t.Run("VerifyCacheHitStatus", func(t *testing.T) {
		// Use unique query to avoid cache collision from previous runs
		uniqueID := rand.Int63()
		query := fmt.Sprintf("/* ttl:60 */ SELECT %d", uniqueID)

		// First query - cache miss
		_, err := db.Exec(query)
		if err != nil {
			t.Fatalf("First query failed: %v", err)
		}

		// Check status after cache miss
		var varName, value string
		rows, err := db.Query("SHOW TQDB STATUS")
		if err != nil {
			t.Fatalf("SHOW TQDB STATUS failed: %v", err)
		}
		defer rows.Close()

		status := make(map[string]string)
		for rows.Next() {
			if err := rows.Scan(&varName, &value); err != nil {
				t.Fatalf("Scan failed: %v", err)
			}
			status[varName] = value
		}

		if status["Backend"] != "primary" {
			t.Errorf("Expected Backend=primary after first query, got %s", status["Backend"])
		}
		if status["Cache_hit"] != "0" {
			t.Errorf("Expected Cache_hit=0 after first query, got %s", status["Cache_hit"])
		}

		// Second query - cache hit (same query)
		_, err = db.Exec(query)
		if err != nil {
			t.Fatalf("Second query failed: %v", err)
		}

		// Check status after cache hit
		rows2, err := db.Query("SHOW TQDB STATUS")
		if err != nil {
			t.Fatalf("SHOW TQDB STATUS failed: %v", err)
		}
		defer rows2.Close()

		status2 := make(map[string]string)
		for rows2.Next() {
			if err := rows2.Scan(&varName, &value); err != nil {
				t.Fatalf("Scan failed: %v", err)
			}
			status2[varName] = value
		}

		if status2["Backend"] != "cache" {
			t.Errorf("Expected Backend=cache after second query, got %s", status2["Backend"])
		}
		if status2["Cache_hit"] != "1" {
			t.Errorf("Expected Cache_hit=1 after second query, got %s", status2["Cache_hit"])
		}
	})
}
