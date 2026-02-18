package writebatch

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/mevdschee/tqdbproxy/metrics"
)

// executeBatch executes a batch of write requests
func (m *Manager) executeBatch(batchKey string, group *BatchGroup) {
	// Check if manager is closed
	if m.closed.Load() {
		group.mu.Lock()
		requests := group.Requests
		group.mu.Unlock()
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: ErrManagerClosed}
		}
		return
	}

	group.mu.Lock()
	requests := group.Requests
	batchSize := len(requests)
	firstSeen := group.FirstSeen
	group.Requests = nil
	group.mu.Unlock()

	// Try to delete this group from the map (it might already be deleted if batch was full)
	m.groups.CompareAndDelete(batchKey, group)

	if batchSize == 0 {
		return
	}

	// Log batch execution
	log.Printf("[WriteBatch] Executing batch: size=%d, batchKey=%q, delay=%v",
		batchSize, batchKey, time.Since(firstSeen))

	// Record metrics
	batchStart := time.Now()
	if requests[0] != nil {
		queryLabel := truncateQuery(requests[0].Query, 50)
		metrics.WriteBatchSize.WithLabelValues(queryLabel).Observe(float64(batchSize))
		metrics.WriteBatchDelay.WithLabelValues(queryLabel).Observe(time.Since(firstSeen).Seconds())
	}

	if batchSize == 1 {
		m.executeSingle(requests[0])
	} else {
		m.executeBatchedWrites(requests)
	}

	// Record latency
	if requests[0] != nil {
		queryLabel := truncateQuery(requests[0].Query, 50)
		metrics.WriteBatchLatency.WithLabelValues(queryLabel).Observe(time.Since(batchStart).Seconds())
		metrics.WriteBatchedTotal.WithLabelValues(getQueryType(requests[0].Query)).Add(float64(batchSize))
	}
}

// truncateQuery truncates a query for use as a metric label
func truncateQuery(query string, maxLen int) string {
	if len(query) <= maxLen {
		return query
	}
	return query[:maxLen] + "..."
}

// getQueryType extracts query type from query string
func getQueryType(query string) string {
	// Simple extraction - look for first SQL keyword
	q := query
	if len(q) > 20 {
		q = q[:20]
	}
	q = " " + q + " "
	if contains(q, " INSERT ") || contains(q, " insert ") {
		return "INSERT"
	}
	if contains(q, " UPDATE ") || contains(q, " update ") {
		return "UPDATE"
	}
	if contains(q, " DELETE ") || contains(q, " delete ") {
		return "DELETE"
	}
	return "UNKNOWN"
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr ||
		s[len(s)-len(substr):] == substr ||
		indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// executeSingle executes a single write request
func (m *Manager) executeSingle(req *WriteRequest) {
	result := m.executeWrite(req.Query, req.Params)
	req.ResultChan <- result
}

// executeBatchedWrites executes multiple write requests
func (m *Manager) executeBatchedWrites(requests []*WriteRequest) {
	// Check if all queries are identical
	allSame := true
	firstQuery := requests[0].Query
	for _, req := range requests[1:] {
		if req.Query != firstQuery {
			allSame = false
			break
		}
	}

	if allSame {
		m.executePreparedBatch(requests)
	} else {
		m.executeTransactionBatch(requests)
	}
}

// executePreparedBatch executes identical queries using a prepared statement
// wrapped in a single transaction for better performance (single commit)
func (m *Manager) executePreparedBatch(requests []*WriteRequest) {
	firstQuery := requests[0].Query
	hasReturning := requests[0].HasReturning

	log.Printf("[WriteBatch] executePreparedBatch: query=%q, numRequests=%d, firstParams=%v, hasReturning=%v",
		firstQuery, len(requests), requests[0].Params, hasReturning)

	// Start a transaction for the batch
	tx, err := m.db.Begin()
	if err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	// Prepare statement within transaction
	stmt, err := tx.Prepare(firstQuery)
	if err != nil {
		log.Printf("[WriteBatch] Prepare error: %v", err)
		tx.Rollback()
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}
	defer stmt.Close()

	// Execute each query and collect results
	results := make([]WriteResult, len(requests))
	hasError := false

	for i, req := range requests {
		if hasReturning {
			// For RETURNING queries, use QueryRow to capture the returned value
			var returnedValue interface{}
			err := stmt.QueryRow(req.Params...).Scan(&returnedValue)
			if err != nil {
				results[i] = WriteResult{Error: err}
				hasError = true
				continue
			}
			results[i] = WriteResult{
				AffectedRows:    1,
				ReturningValues: []interface{}{returnedValue},
				BatchSize:       len(requests),
			}
		} else {
			result, err := stmt.Exec(req.Params...)
			if err != nil {
				results[i] = WriteResult{Error: err}
				hasError = true
				continue
			}

			affected, _ := result.RowsAffected()
			lastID, _ := result.LastInsertId()
			results[i] = WriteResult{
				AffectedRows: affected,
				LastInsertID: lastID,
				BatchSize:    len(requests),
			}
		}
	}

	// If any error occurred, rollback and send errors
	if hasError {
		tx.Rollback()
		for i, req := range requests {
			req.ResultChan <- results[i]
		}
		return
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	// Send successful results
	for i, req := range requests {
		req.ResultChan <- results[i]
		// Notify connection of batch completion
		if req.OnBatchComplete != nil {
			req.OnBatchComplete(len(requests))
		}
	}
}

// isBatchableInsert checks if query is a simple INSERT that can be batched
func isBatchableInsert(query string) bool {
	// Simple check for INSERT INTO ... VALUES pattern
	return len(query) > 12 &&
		(query[0:6] == "INSERT" || query[0:6] == "insert") &&
		(query[len(query)-1] == ')' || query[len(query)-1] == ' ')
}

// executeTrueBatchedInsert combines multiple INSERTs into one multi-value INSERT
func (m *Manager) executeTrueBatchedInsert(requests []*WriteRequest) {
	firstQuery := requests[0].Query

	// Find the VALUES clause
	valuesIdx := -1
	for i := 0; i < len(firstQuery)-6; i++ {
		if firstQuery[i:i+6] == "VALUES" || firstQuery[i:i+6] == "values" {
			valuesIdx = i + 6
			break
		}
	}

	if valuesIdx == -1 {
		// Fallback if we can't parse
		m.executePreparedBatchFallback(requests)
		return
	}

	// Build multi-value INSERT
	baseQuery := firstQuery[:valuesIdx]
	numParams := len(requests[0].Params)

	// Detect if using PostgreSQL placeholders ($1) or MySQL/SQLite placeholders (?)
	isPostgres := len(firstQuery) > 0 && containsPostgresPlaceholder(firstQuery)

	// Pre-allocate allParams slice to avoid reallocations
	totalParams := len(requests) * numParams
	allParams := make([]interface{}, 0, totalParams)

	// Use strings.Builder for efficient string concatenation
	var builder strings.Builder
	// Pre-allocate buffer: baseQuery + estimated size for value clauses
	estimatedSize := len(baseQuery) + len(requests)*(numParams*4+3) // rough estimate
	builder.Grow(estimatedSize)

	builder.WriteString(baseQuery)
	builder.WriteString(" ")

	paramIndex := 1
	for reqIdx, req := range requests {
		if reqIdx > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString("(")

		for i := 0; i < numParams; i++ {
			if i > 0 {
				builder.WriteString(",")
			}
			if isPostgres {
				builder.WriteString("$")
				builder.WriteString(strconv.Itoa(paramIndex))
				paramIndex++
			} else {
				builder.WriteString("?")
			}
		}
		builder.WriteString(")")
		allParams = append(allParams, req.Params...)
	}

	// Execute batched query
	batchQuery := builder.String()
	result, err := m.db.Exec(batchQuery, allParams...)
	if err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	_, _ = result.RowsAffected()
	firstID, _ := result.LastInsertId()

	// Send results to all requests
	for i, req := range requests {
		req.ResultChan <- WriteResult{
			AffectedRows: 1, // Each request affects 1 row
			LastInsertID: firstID + int64(i),
			BatchSize:    len(requests),
		}
		// Notify connection of batch completion
		if req.OnBatchComplete != nil {
			req.OnBatchComplete(len(requests))
		}
	}
}

// containsPostgresPlaceholder checks if query uses PostgreSQL $1 syntax
func containsPostgresPlaceholder(query string) bool {
	for i := 0; i < len(query)-1; i++ {
		if query[i] == '$' && query[i+1] >= '0' && query[i+1] <= '9' {
			return true
		}
	}
	return false
}

// executePreparedBatchFallback is the original implementation
func (m *Manager) executePreparedBatchFallback(requests []*WriteRequest) {
	stmt, err := m.db.Prepare(requests[0].Query)
	if err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}
	defer stmt.Close()

	for _, req := range requests {
		result, err := stmt.Exec(req.Params...)
		if err != nil {
			req.ResultChan <- WriteResult{Error: err}
			continue
		}

		affected, _ := result.RowsAffected()
		lastID, _ := result.LastInsertId()
		req.ResultChan <- WriteResult{
			AffectedRows: affected,
			LastInsertID: lastID,
			BatchSize:    len(requests),
		}
		// Notify connection of batch completion
		if req.OnBatchComplete != nil {
			req.OnBatchComplete(len(requests))
		}
	}
}

// executeTransactionBatch executes mixed queries in a transaction
func (m *Manager) executeTransactionBatch(requests []*WriteRequest) {
	tx, err := m.db.Begin()
	if err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	results := make([]WriteResult, len(requests))

	for i, req := range requests {
		result, err := tx.Exec(req.Query, req.Params...)
		if err != nil {
			tx.Rollback()
			// Send error to all requests
			for j := 0; j <= i; j++ {
				requests[j].ResultChan <- WriteResult{Error: err}
			}
			for j := i + 1; j < len(requests); j++ {
				requests[j].ResultChan <- WriteResult{Error: err}
			}
			return
		}

		affected, _ := result.RowsAffected()
		lastID, _ := result.LastInsertId()
		results[i] = WriteResult{
			AffectedRows: affected,
			LastInsertID: lastID,
			BatchSize:    len(requests),
		}
	}

	if err := tx.Commit(); err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	// Send results to all requests
	for i, req := range requests {
		req.ResultChan <- results[i]
		// Notify connection of batch completion
		if req.OnBatchComplete != nil {
			req.OnBatchComplete(len(requests))
		}
	}
}

// executeWrite executes a single write operation
func (m *Manager) executeWrite(query string, params []interface{}) WriteResult {
	result, err := m.db.Exec(query, params...)
	if err != nil {
		return WriteResult{Error: err}
	}

	affected, _ := result.RowsAffected()
	lastID, _ := result.LastInsertId()

	return WriteResult{
		AffectedRows: affected,
		LastInsertID: lastID,
		BatchSize:    1,
	}
}
