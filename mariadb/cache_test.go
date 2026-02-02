package mariadb

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestCacheHit(t *testing.T) {
	// Connect to the proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Use a single connection for the entire test to ensure status reflects previous queries
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Failed to get connection from pool: %v", err)
	}
	defer conn.Close()

	t.Run("VerifyCacheHitStatus", func(t *testing.T) {
		// Use unique query to avoid cache collision from previous runs
		uniqueID := rand.Int63()
		query := fmt.Sprintf("/* ttl:60 */ SELECT %d", uniqueID)

		// First query - cache miss
		_, err := conn.ExecContext(context.Background(), query)
		if err != nil {
			t.Fatalf("First query failed: %v", err)
		}

		// Check status after cache miss
		var varName, value string
		rows, err := conn.QueryContext(context.Background(), "SHOW TQDB STATUS")
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

		if !strings.HasPrefix(status["Backend"], "replicas[") {
			t.Logf("Full status (miss): %v", status)
			t.Errorf("Expected Backend to start with replicas[ after first query, got %s", status["Backend"])
		}

		// Second query - cache hit (same query)
		_, err = conn.ExecContext(context.Background(), query)
		if err != nil {
			t.Fatalf("Second query failed: %v", err)
		}

		// Check status after cache hit
		rows2, err := conn.QueryContext(context.Background(), "SHOW TQDB STATUS")
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
			t.Logf("Full status (hit): %v", status2)
			t.Errorf("Expected Backend=cache after second query, got %s", status2["Backend"])
		}
	})
}

// TestStaleDataSingleFlight verifies that when cache becomes stale:
// - Only ONE request goes to the backend (single-flight)
// - Other concurrent requests serve stale data
// - After refresh, fresh data is served
func TestStaleDataSingleFlight(t *testing.T) {
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Allow multiple connections
	db.SetMaxOpenConns(10)

	uniqueID := rand.Int63()
	// Use very short TTL (1 second) so it becomes stale quickly
	query := fmt.Sprintf("/* ttl:1 */ SELECT %d", uniqueID)

	// First query - populates cache
	conn1, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
	defer conn1.Close()

	_, err = conn1.ExecContext(context.Background(), query)
	if err != nil {
		t.Fatalf("First query failed: %v", err)
	}

	// Verify cache hit
	_, err = conn1.ExecContext(context.Background(), query)
	if err != nil {
		t.Fatalf("Second query (cache hit) failed: %v", err)
	}

	status := getStatus(t, conn1)
	if status["Backend"] != "cache" {
		t.Errorf("Expected cache hit, got %s", status["Backend"])
	}

	// Wait for TTL to expire (1 second + buffer)
	t.Log("Waiting for TTL to expire...")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	<-time.After(2 * time.Second)

	// Now cache is stale. Run concurrent requests
	t.Log("Running concurrent requests on stale cache...")
	var wg sync.WaitGroup
	results := make(chan string, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := db.Conn(ctx)
			if err != nil {
				results <- fmt.Sprintf("error:%v", err)
				return
			}
			defer conn.Close()

			_, err = conn.ExecContext(ctx, query)
			if err != nil {
				results <- fmt.Sprintf("error:%v", err)
				return
			}

			status := getStatus(t, conn)
			results <- status["Backend"]
		}(i)
	}

	wg.Wait()
	close(results)

	// Count results
	staleCount := 0
	refreshCount := 0
	cacheCount := 0
	for r := range results {
		if strings.Contains(r, "stale") {
			staleCount++
		} else if strings.HasPrefix(r, "replicas[") || r == "primary" {
			refreshCount++ // This one did the refresh
		} else if r == "cache" {
			cacheCount++ // Fresh cache hit after refresh
		} else {
			t.Logf("Unexpected result: %s", r)
		}
	}

	t.Logf("Results: stale=%d, refresh=%d, cache=%d", staleCount, refreshCount, cacheCount)

	// We expect at most 1 request to hit the backend (single-flight)
	if refreshCount > 1 {
		t.Errorf("Single-flight failed: %d requests hit backend, expected at most 1", refreshCount)
	}
}

func getStatus(t *testing.T, conn *sql.Conn) map[string]string {
	rows, err := conn.QueryContext(context.Background(), "SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("SHOW TQDB STATUS failed: %v", err)
	}
	defer rows.Close()

	status := make(map[string]string)
	for rows.Next() {
		var varName, value string
		if err := rows.Scan(&varName, &value); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		status[varName] = value
	}
	return status
}

// TestColdCacheSingleFlight verifies that on cache miss:
// - Only ONE request goes to the backend (single-flight)
// - Other concurrent requests BLOCK and wait (not served stale)
// - All requests get the same result
func TestColdCacheSingleFlight(t *testing.T) {
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Allow multiple connections
	db.SetMaxOpenConns(10)

	// Use unique query to ensure cold cache
	uniqueID := rand.Int63()
	query := fmt.Sprintf("/* ttl:60 */ SELECT %d", uniqueID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run concurrent requests on cold cache
	t.Log("Running concurrent requests on cold cache...")
	var wg sync.WaitGroup
	results := make(chan string, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := db.Conn(ctx)
			if err != nil {
				results <- fmt.Sprintf("error:%v", err)
				return
			}
			defer conn.Close()

			_, err = conn.ExecContext(ctx, query)
			if err != nil {
				results <- fmt.Sprintf("error:%v", err)
				return
			}

			status := getStatus(t, conn)
			results <- status["Backend"]
		}(i)
	}

	wg.Wait()
	close(results)

	// Count results
	backendCount := 0
	cacheCount := 0
	for r := range results {
		if strings.HasPrefix(r, "replicas[") || r == "primary" {
			backendCount++ // Hit backend directly
		} else if r == "cache" {
			cacheCount++ // Got result from cache (waited for first request)
		} else if strings.Contains(r, "error") {
			t.Errorf("Request failed: %s", r)
		} else {
			t.Logf("Unexpected result: %s", r)
		}
	}

	t.Logf("Results: backend=%d, cache=%d", backendCount, cacheCount)

	// We expect exactly 1 request to hit the backend (single-flight)
	// The others should wait and get the cached result
	if backendCount != 1 {
		t.Errorf("Single-flight failed: %d requests hit backend, expected exactly 1", backendCount)
	}
	if cacheCount != 4 {
		t.Errorf("Expected 4 requests to wait for cache, got %d", cacheCount)
	}
}
