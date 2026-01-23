package mariadb

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/go-sql-driver/mysql"
)

// TestConcurrentConnections tests the proxy under concurrent load
func TestConcurrentConnections(t *testing.T) {
	dsn := "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy"
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Skipf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("Failed to ping proxy: %v", err)
	}

	// Configure connection pool
	numConns := 10
	db.SetMaxOpenConns(numConns)
	db.SetMaxIdleConns(numConns)

	var wg sync.WaitGroup
	var successCount int64
	var errorCount int64
	queriesPerConn := 100

	// Start concurrent workers
	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < queriesPerConn; j++ {
				rows, err := db.Query("SELECT 1")
				if err != nil {
					atomic.AddInt64(&errorCount, 1)
					t.Logf("Worker %d query %d failed: %v", workerID, j, err)
					continue
				}
				rows.Close()
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	totalQueries := int64(numConns * queriesPerConn)
	t.Logf("Concurrent test: %d/%d queries succeeded (%d errors)",
		successCount, totalQueries, errorCount)

	// Allow some failures but most should succeed
	successRate := float64(successCount) / float64(totalQueries) * 100
	if successRate < 90 {
		t.Errorf("Success rate too low: %.1f%% (expected >= 90%%)", successRate)
	}
}

// TestConcurrentTransactions tests concurrent transaction handling
func TestConcurrentTransactions(t *testing.T) {
	dsn := "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy"
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Skipf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("Failed to ping proxy: %v", err)
	}

	numConns := 5
	db.SetMaxOpenConns(numConns)
	db.SetMaxIdleConns(numConns)

	var wg sync.WaitGroup
	var successCount int64
	var errorCount int64

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				tx, err := db.Begin()
				if err != nil {
					atomic.AddInt64(&errorCount, 1)
					continue
				}

				_, err = tx.Exec("SELECT 1")
				if err != nil {
					tx.Rollback()
					atomic.AddInt64(&errorCount, 1)
					continue
				}

				if err := tx.Commit(); err != nil {
					atomic.AddInt64(&errorCount, 1)
					continue
				}
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	totalTx := int64(numConns * 10)
	t.Logf("Concurrent transactions: %d/%d succeeded (%d errors)",
		successCount, totalTx, errorCount)

	successRate := float64(successCount) / float64(totalTx) * 100
	if successRate < 90 {
		t.Errorf("Transaction success rate too low: %.1f%%", successRate)
	}
}
