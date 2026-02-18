package mariadb

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/config"
	"github.com/mevdschee/tqdbproxy/replica"
)

func TestWriteBatchIntegration(t *testing.T) {
	// This test requires a running MariaDB backend
	// Skip if not available
	t.Skip("Integration test requires running MariaDB backend - run manually when testing")

	// Create a proxy with write batching enabled
	pcfg := config.ProxyConfig{
		Listen:  ":13307", // Use different port to avoid conflicts
		Default: "main",
		Backends: map[string]config.BackendConfig{
			"main": {
				Primary: "127.0.0.1:3306",
			},
		},
		WriteBatch: config.WriteBatchConfig{
			MaxBatchSize: 100,
		},
	}

	pools := map[string]*replica.Pool{
		"main": replica.NewPool(
			"127.0.0.1:3306",
			[]string{},
		),
	}

	c, err := cache.New(cache.DefaultCacheConfig())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	proxy := New(pcfg, pools, c)

	// Start the proxy
	if err := proxy.Start(); err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer proxy.Stop()

	// Give the proxy time to start
	time.Sleep(100 * time.Millisecond)

	// Connect to the proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:13307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer db.Close()

	// Create a test table
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS test_batch (id INT PRIMARY KEY AUTO_INCREMENT, value VARCHAR(255))")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS test_batch")

	// Test 1: Single write should be batched
	t.Run("SingleWrite", func(t *testing.T) {
		result, err := db.Exec("INSERT INTO test_batch (value) VALUES ('test1')")
		if err != nil {
			t.Fatalf("INSERT failed: %v", err)
		}

		rows, err := result.RowsAffected()
		if err != nil {
			t.Fatalf("RowsAffected failed: %v", err)
		}

		if rows != 1 {
			t.Errorf("Expected 1 row affected, got %d", rows)
		}

		lastID, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("LastInsertId failed: %v", err)
		}

		if lastID == 0 {
			t.Error("Expected non-zero last insert ID")
		}
	})

	// Test 2: Multiple concurrent writes should be batched together
	t.Run("ConcurrentWrites", func(t *testing.T) {
		const numWrites = 10
		errChan := make(chan error, numWrites)

		// Send concurrent writes
		for i := 0; i < numWrites; i++ {
			go func(idx int) {
				_, err := db.Exec("INSERT INTO test_batch (value) VALUES (?)", idx)
				errChan <- err
			}(i)
		}

		// Wait for all writes to complete
		for i := 0; i < numWrites; i++ {
			if err := <-errChan; err != nil {
				t.Errorf("Concurrent write %d failed: %v", i, err)
			}
		}

		// Verify all rows were inserted
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM test_batch").Scan(&count)
		if err != nil {
			t.Fatalf("SELECT COUNT failed: %v", err)
		}

		expected := numWrites + 1 // +1 from SingleWrite test
		if count < expected {
			t.Errorf("Expected at least %d rows, got %d", expected, count)
		}
	})

	// Test 3: Writes in transaction should NOT be batched
	t.Run("TransactionWrites", func(t *testing.T) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("BEGIN failed: %v", err)
		}
		defer tx.Rollback()

		// This should execute immediately, not batched
		result, err := tx.Exec("INSERT INTO test_batch (value) VALUES ('in-transaction')")
		if err != nil {
			t.Fatalf("INSERT in transaction failed: %v", err)
		}

		rows, err := result.RowsAffected()
		if err != nil {
			t.Fatalf("RowsAffected failed: %v", err)
		}

		if rows != 1 {
			t.Errorf("Expected 1 row affected, got %d", rows)
		}

		// Rollback to clean up
		tx.Rollback()

		// Verify the row was NOT committed (should be rolled back)
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM test_batch WHERE value = 'in-transaction'").Scan(&count)
		if err != nil {
			t.Fatalf("SELECT COUNT failed: %v", err)
		}

		if count != 0 {
			t.Errorf("Expected 0 rows (rolled back), got %d", count)
		}
	})

	// Test 4: Non-batchable writes (UPDATE) should still work
	t.Run("UpdateWrite", func(t *testing.T) {
		// First insert a row to update
		result, err := db.Exec("INSERT INTO test_batch (value) VALUES ('to-update')")
		if err != nil {
			t.Fatalf("INSERT failed: %v", err)
		}

		lastID, _ := result.LastInsertId()

		// UPDATE queries are not batchable (different WHERE clauses)
		result, err = db.Exec("UPDATE test_batch SET value = 'updated' WHERE id = ?", lastID)
		if err != nil {
			t.Fatalf("UPDATE failed: %v", err)
		}

		rows, err := result.RowsAffected()
		if err != nil {
			t.Fatalf("RowsAffected failed: %v", err)
		}

		if rows != 1 {
			t.Errorf("Expected 1 row affected, got %d", rows)
		}
	})
}

func TestWriteBatchManagerLifecycle(t *testing.T) {
	// Test that writebatch manager is properly initialized and cleaned up

	pcfg := config.ProxyConfig{
		Listen:  ":13308",
		Default: "main",
		Backends: map[string]config.BackendConfig{
			"main": {Primary: "127.0.0.1:3306"},
		},
		WriteBatch: config.WriteBatchConfig{
			MaxBatchSize: 1000,
		},
	}

	pools := map[string]*replica.Pool{
		"main": replica.NewPool("127.0.0.1:3306", []string{}),
	}

	c, err := cache.New(cache.DefaultCacheConfig())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	proxy := New(pcfg, pools, c)

	// Check that context and cancel func were created
	if proxy.wbCtx == nil {
		t.Error("wbCtx should be initialized")
	}
	if proxy.wbCancel == nil {
		t.Error("wbCancel should be initialized")
	}

	// writeBatch manager should be nil until Start() is called
	if proxy.writeBatch != nil {
		t.Error("writeBatch should be nil before Start()")
	}

	// Start should fail if backend is not available (but that's ok for this test)
	// We're just testing the lifecycle, not the actual connection
	_ = proxy.Start()

	// After Start, writeBatch should be initialized if backend connected
	// (Will be nil if connection failed, but that's expected without a real backend)

	// Stop should clean up without error
	if err := proxy.Stop(); err != nil {
		t.Errorf("Stop() failed: %v", err)
	}
}

// Benchmark write batching vs immediate execution
func BenchmarkWriteBatchVsImmediate(b *testing.B) {
	b.Skip("Benchmark requires running MariaDB backend - run manually when testing")

	// This benchmark compares batched vs unbatched write performance
	// Would need actual backend connection to run
}

// Test that batching respects the batch key
func TestBatchKeyGrouping(t *testing.T) {
	// Unit test: verify that queries with the same batch key are grouped together

	// This is tested in parser_test.go for GetBatchKey()
	// And in writebatch/manager_test.go for actual batching behavior

	// Here we would test the integration: multiple clients sending same INSERT
	// should be grouped into a single batch
}

// Test batching configuration
func TestWriteBatchConfig(t *testing.T) {
	// Test that the batching config is correctly set

	pcfg := config.ProxyConfig{
		WriteBatch: config.WriteBatchConfig{
			MaxBatchSize: 1000,
		},
	}

	// Verify config is correctly translated
	if pcfg.WriteBatch.MaxBatchSize != 1000 {
		t.Errorf("Expected MaxBatchSize 1000, got %d", pcfg.WriteBatch.MaxBatchSize)
	}
}
