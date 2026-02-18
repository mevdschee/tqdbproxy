package mariadb

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestBatchSizeReporting(t *testing.T) {
	// Connect to proxy
	// Note: Standard db.Exec() uses prepared statements which strip comments
	// We need to use a connection that forces text protocol
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	numQueries := 10

	// Set max connections to allow parallel execution
	// This lets multiple goroutines send queries simultaneously for batching
	db.SetMaxOpenConns(numQueries)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_test (id INT PRIMARY KEY AUTO_INCREMENT, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_test")

	// Prepare statement with batch hint
	// Prepared statements share the same batch key regardless of parameter values
	stmt, err := db.Prepare("/* batch:1000 */ INSERT INTO batch_test (value) VALUES (?)")
	if err != nil {
		t.Fatalf("Failed to prepare statement: %v", err)
	}
	defer stmt.Close()

	// Execute writes in parallel using goroutines with different values
	// All executions share the same batch key from the prepared statement
	type resultInfo struct {
		err          error
		lastInsertID int64
	}
	resultChan := make(chan resultInfo, numQueries)

	for i := 0; i < numQueries; i++ {
		go func(idx int) {
			result, err := stmt.Exec(idx)
			if err != nil {
				resultChan <- resultInfo{err: fmt.Errorf("exec %d failed: %v", idx, err)}
				return
			}
			lastID, _ := result.LastInsertId()
			resultChan <- resultInfo{lastInsertID: lastID}
		}(i)
	}

	// Wait for all goroutines to complete and collect results
	var lastInsertIDs []int64
	for i := 0; i < numQueries; i++ {
		res := <-resultChan
		if res.err != nil {
			t.Fatalf("Failed to execute query: %v", res.err)
		}
		if res.lastInsertID > 0 {
			lastInsertIDs = append(lastInsertIDs, res.lastInsertID)
		}
	}

	// Verify we received last insert IDs
	if len(lastInsertIDs) != numQueries {
		t.Errorf("Expected %d last insert IDs, got %d", numQueries, len(lastInsertIDs))
	}

	// All batched inserts should return valid IDs (they may be the same or sequential)
	for i, id := range lastInsertIDs {
		if id == 0 {
			t.Errorf("Last insert ID %d is 0 (invalid)", i)
		}
	}

	if len(lastInsertIDs) > 0 {
		t.Logf("✓ Received %d valid last insert IDs, first=%d, last=%d", len(lastInsertIDs), lastInsertIDs[0], lastInsertIDs[len(lastInsertIDs)-1])
	}

	// Wait a moment for batch to flush (batch:1000 means 1000ms window)
	time.Sleep(1500 * time.Millisecond)

	// Query SHOW TQDB STATUS to get batch size
	rows, err := db.Query("SHOW TQDB STATUS")
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
		t.Fatalf("LastBatchSize not found in SHOW TQDB STATUS")
	}

	if batchSize != numQueries {
		t.Errorf("Expected batch size %d, got %d", numQueries, batchSize)
	}

	t.Logf("✓ Batch size correctly reported as %d", batchSize)
}

func TestBatchSizeWithDirectQueries(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	numQueries := 10

	// Set max connections to allow parallel execution
	db.SetMaxOpenConns(numQueries)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_test (id INT PRIMARY KEY AUTO_INCREMENT, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_test")

	// Execute the SAME query multiple times using direct Exec (not prepared statements)
	// All queries have the same batch key because the SQL is identical
	type resultInfo struct {
		err          error
		lastInsertID int64
	}
	resultChan := make(chan resultInfo, numQueries)

	for i := 0; i < numQueries; i++ {
		go func() {
			result, err := db.Exec("/* batch:1000 */ INSERT INTO batch_test (value) VALUES (42)")
			if err != nil {
				resultChan <- resultInfo{err: fmt.Errorf("exec failed: %v", err)}
				return
			}
			lastID, _ := result.LastInsertId()
			resultChan <- resultInfo{lastInsertID: lastID}
		}()
	}

	// Wait for all goroutines to complete and collect results
	var lastInsertIDs []int64
	for i := 0; i < numQueries; i++ {
		res := <-resultChan
		if res.err != nil {
			t.Fatalf("Failed to execute query: %v", res.err)
		}
		if res.lastInsertID > 0 {
			lastInsertIDs = append(lastInsertIDs, res.lastInsertID)
		}
	}

	// Verify we received last insert IDs
	if len(lastInsertIDs) != numQueries {
		t.Errorf("Expected %d last insert IDs, got %d", numQueries, len(lastInsertIDs))
	}

	// All batched inserts should return valid IDs
	for i, id := range lastInsertIDs {
		if id == 0 {
			t.Errorf("Last insert ID %d is 0 (invalid)", i)
		}
	}

	if len(lastInsertIDs) > 0 {
		t.Logf("✓ Received %d valid last insert IDs, first=%d, last=%d", len(lastInsertIDs), lastInsertIDs[0], lastInsertIDs[len(lastInsertIDs)-1])
	}

	// Wait for batch to flush (batch:1000 means 1000ms window)
	time.Sleep(1500 * time.Millisecond)

	// Query SHOW TQDB STATUS to get batch size
	rows, err := db.Query("SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}
	defer rows.Close()

	var batchSize int
	var foundBatchSize bool
	var backend string

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
		if variable == "Backend" {
			backend = value
		}
	}

	if !foundBatchSize {
		t.Fatalf("LastBatchSize not found in SHOW TQDB STATUS")
	}

	if backend != "write-batch" {
		t.Errorf("Expected backend 'write-batch', got '%s'", backend)
	}

	if batchSize != numQueries {
		t.Errorf("Expected batch size %d, got %d", numQueries, batchSize)
	}

	t.Logf("✓ Batch size correctly reported as %d for direct queries", batchSize)
}

func TestBatchSizeWithMultipleBatches(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
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

	_, err = db.Exec("CREATE TABLE batch_test (id INT PRIMARY KEY AUTO_INCREMENT, value INT)")
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
	rows, err := db.Query("SHOW TQDB STATUS")
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
	rows, err = db.Query("SHOW TQDB STATUS")
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

func TestWriteBatchBackendReporting(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	// Set max connections to 1 to ensure we use the same connection
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_backend_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_backend_test (id INT PRIMARY KEY AUTO_INCREMENT, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_backend_test")

	// Execute batched inserts
	numQueries := 5
	for i := 0; i < numQueries; i++ {
		_, err := db.Exec(fmt.Sprintf("/* batch:500 */ INSERT INTO batch_backend_test (value) VALUES (%d)", i))
		if err != nil {
			t.Fatalf("Failed to execute query %d: %v", i, err)
		}
	}

	// Wait for batch to flush (500ms window + buffer)
	time.Sleep(700 * time.Millisecond)

	// Query SHOW TQDB STATUS
	rows, err := db.Query("SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}
	defer rows.Close()

	var backend string
	var batchSize int
	var foundBackend, foundBatchSize bool

	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}

		t.Logf("TQDB Status: %s = %s", variable, value)

		if variable == "Backend" {
			backend = value
			foundBackend = true
		}
		if variable == "LastBatchSize" {
			fmt.Sscanf(value, "%d", &batchSize)
			foundBatchSize = true
		}
	}

	if !foundBackend {
		t.Fatalf("Backend not found in SHOW TQDB STATUS")
	}

	// Verify backend is write-batch (this confirms batching is enabled and working)
	if backend != "write-batch" {
		t.Errorf("Expected backend 'write-batch', got '%s' - write batching may not be enabled", backend)
	}

	if !foundBatchSize {
		t.Errorf("LastBatchSize not found in SHOW TQDB STATUS")
	} else if batchSize != numQueries {
		t.Errorf("Expected batch size %d, got %d", numQueries, batchSize)
	}

	if backend == "write-batch" && foundBatchSize && batchSize == numQueries {
		t.Logf("✓ Write batching is enabled and working correctly")
		t.Logf("✓ Backend: %s, Batch size: %d", backend, batchSize)
	}
}

func TestBatchSizeWithUpdatePrepared(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	numQueries := 10

	// Set max connections to allow parallel execution
	db.SetMaxOpenConns(numQueries)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_test (id INT PRIMARY KEY, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_test")

	// Insert test data
	for i := 0; i < numQueries; i++ {
		_, err = db.Exec("INSERT INTO batch_test (id, value) VALUES (?, ?)", i, i*10)
		if err != nil {
			t.Fatalf("Failed to insert test data: %v", err)
		}
	}

	// Prepare UPDATE statement with batch hint
	stmt, err := db.Prepare("/* batch:1000 */ UPDATE batch_test SET value = ? WHERE id = ?")
	if err != nil {
		t.Fatalf("Failed to prepare statement: %v", err)
	}
	defer stmt.Close()

	// Execute updates in parallel
	errChan := make(chan error, numQueries)

	for i := 0; i < numQueries; i++ {
		go func(idx int) {
			_, err := stmt.Exec(idx*100, idx)
			if err != nil {
				errChan <- fmt.Errorf("exec %d failed: %v", idx, err)
				return
			}
			errChan <- nil
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numQueries; i++ {
		if err := <-errChan; err != nil {
			t.Fatalf("Failed to execute query: %v", err)
		}
	}

	// Wait for batch to flush
	time.Sleep(1500 * time.Millisecond)

	// Query SHOW TQDB STATUS to get batch size
	rows, err := db.Query("SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}
	defer rows.Close()

	var batchSize int
	var foundBatchSize bool
	var backend string

	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}

		if variable == "LastBatchSize" {
			fmt.Sscanf(value, "%d", &batchSize)
			foundBatchSize = true
		}
		if variable == "Backend" {
			backend = value
		}
	}

	if !foundBatchSize {
		t.Fatalf("LastBatchSize not found in SHOW TQDB STATUS")
	}

	if backend != "write-batch" {
		t.Errorf("Expected backend 'write-batch', got '%s'", backend)
	}

	if batchSize != numQueries {
		t.Errorf("Expected batch size %d, got %d", numQueries, batchSize)
	}

	t.Logf("✓ UPDATE batch size correctly reported as %d", batchSize)
}

func TestBatchSizeWithUpdateDirect(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	numQueries := 10

	// Set max connections to allow parallel execution
	db.SetMaxOpenConns(numQueries)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_test (id INT PRIMARY KEY, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_test")

	// Insert test data
	for i := 0; i < numQueries; i++ {
		_, err = db.Exec("INSERT INTO batch_test (id, value) VALUES (?, ?)", i, i*10)
		if err != nil {
			t.Fatalf("Failed to insert test data: %v", err)
		}
	}

	// Execute the SAME UPDATE query multiple times using direct Exec
	errChan := make(chan error, numQueries)

	for i := 0; i < numQueries; i++ {
		go func() {
			_, err := db.Exec("/* batch:1000 */ UPDATE batch_test SET value = 999 WHERE value < 1000")
			if err != nil {
				errChan <- fmt.Errorf("exec failed: %v", err)
				return
			}
			errChan <- nil
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < numQueries; i++ {
		if err := <-errChan; err != nil {
			t.Fatalf("Failed to execute query: %v", err)
		}
	}

	// Wait for batch to flush
	time.Sleep(1500 * time.Millisecond)

	// Query SHOW TQDB STATUS to get batch size
	rows, err := db.Query("SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}
	defer rows.Close()

	var batchSize int
	var foundBatchSize bool
	var backend string

	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}

		if variable == "LastBatchSize" {
			fmt.Sscanf(value, "%d", &batchSize)
			foundBatchSize = true
		}
		if variable == "Backend" {
			backend = value
		}
	}

	if !foundBatchSize {
		t.Fatalf("LastBatchSize not found in SHOW TQDB STATUS")
	}

	if backend != "write-batch" {
		t.Errorf("Expected backend 'write-batch', got '%s'", backend)
	}

	if batchSize != numQueries {
		t.Errorf("Expected batch size %d, got %d", numQueries, batchSize)
	}

	t.Logf("✓ Direct UPDATE batch size correctly reported as %d", batchSize)
}

func TestBatchSizeWithDeletePrepared(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	numQueries := 10

	// Set max connections to allow parallel execution
	db.SetMaxOpenConns(numQueries)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_test (id INT PRIMARY KEY, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_test")

	// Insert test data
	for i := 0; i < numQueries*2; i++ {
		_, err = db.Exec("INSERT INTO batch_test (id, value) VALUES (?, ?)", i, i*10)
		if err != nil {
			t.Fatalf("Failed to insert test data: %v", err)
		}
	}

	// Prepare DELETE statement with batch hint
	stmt, err := db.Prepare("/* batch:1000 */ DELETE FROM batch_test WHERE id = ?")
	if err != nil {
		t.Fatalf("Failed to prepare statement: %v", err)
	}
	defer stmt.Close()

	// Execute deletes in parallel
	errChan := make(chan error, numQueries)

	for i := 0; i < numQueries; i++ {
		go func(idx int) {
			_, err := stmt.Exec(idx)
			if err != nil {
				errChan <- fmt.Errorf("exec %d failed: %v", idx, err)
				return
			}
			errChan <- nil
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numQueries; i++ {
		if err := <-errChan; err != nil {
			t.Fatalf("Failed to execute query: %v", err)
		}
	}

	// Wait for batch to flush
	time.Sleep(1500 * time.Millisecond)

	// Query SHOW TQDB STATUS to get batch size
	rows, err := db.Query("SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}
	defer rows.Close()

	var batchSize int
	var foundBatchSize bool
	var backend string

	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}

		if variable == "LastBatchSize" {
			fmt.Sscanf(value, "%d", &batchSize)
			foundBatchSize = true
		}
		if variable == "Backend" {
			backend = value
		}
	}

	if !foundBatchSize {
		t.Fatalf("LastBatchSize not found in SHOW TQDB STATUS")
	}

	if backend != "write-batch" {
		t.Errorf("Expected backend 'write-batch', got '%s'", backend)
	}

	if batchSize != numQueries {
		t.Errorf("Expected batch size %d, got %d", numQueries, batchSize)
	}

	t.Logf("✓ DELETE batch size correctly reported as %d", batchSize)
}

func TestBatchSizeWithDeleteDirect(t *testing.T) {
	// Connect to proxy
	db, err := sql.Open("mysql", "tqdbproxy:tqdbproxy@tcp(127.0.0.1:3307)/tqdbproxy")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer db.Close()

	numQueries := 10

	// Set max connections to allow parallel execution
	db.SetMaxOpenConns(numQueries)
	db.SetMaxIdleConns(1)

	// Create test table
	_, err = db.Exec("DROP TABLE IF EXISTS batch_test")
	if err != nil {
		t.Fatalf("Failed to drop table: %v", err)
	}

	_, err = db.Exec("CREATE TABLE batch_test (id INT PRIMARY KEY, value INT)")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer db.Exec("DROP TABLE IF EXISTS batch_test")

	// Insert test data
	for i := 0; i < 100; i++ {
		_, err = db.Exec("INSERT INTO batch_test (id, value) VALUES (?, ?)", i, i*10)
		if err != nil {
			t.Fatalf("Failed to insert test data: %v", err)
		}
	}

	// Execute the SAME DELETE query multiple times using direct Exec
	errChan := make(chan error, numQueries)

	for i := 0; i < numQueries; i++ {
		go func() {
			_, err := db.Exec("/* batch:1000 */ DELETE FROM batch_test WHERE value >= 500")
			if err != nil {
				errChan <- fmt.Errorf("exec failed: %v", err)
				return
			}
			errChan <- nil
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < numQueries; i++ {
		if err := <-errChan; err != nil {
			t.Fatalf("Failed to execute query: %v", err)
		}
	}

	// Wait for batch to flush
	time.Sleep(1500 * time.Millisecond)

	// Query SHOW TQDB STATUS to get batch size
	rows, err := db.Query("SHOW TQDB STATUS")
	if err != nil {
		t.Fatalf("Failed to query TQDB status: %v", err)
	}
	defer rows.Close()

	var batchSize int
	var foundBatchSize bool
	var backend string

	for rows.Next() {
		var variable, value string
		if err := rows.Scan(&variable, &value); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}

		if variable == "LastBatchSize" {
			fmt.Sscanf(value, "%d", &batchSize)
			foundBatchSize = true
		}
		if variable == "Backend" {
			backend = value
		}
	}

	if !foundBatchSize {
		t.Fatalf("LastBatchSize not found in SHOW TQDB STATUS")
	}

	if backend != "write-batch" {
		t.Errorf("Expected backend 'write-batch', got '%s'", backend)
	}

	if batchSize != numQueries {
		t.Errorf("Expected batch size %d, got %d", numQueries, batchSize)
	}

	t.Logf("✓ Direct DELETE batch size correctly reported as %d", batchSize)
}
