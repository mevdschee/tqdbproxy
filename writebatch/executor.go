package writebatch

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
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

	// Count this batch
	m.batchCount.Add(1)

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
		if !requests[0].HasReturning && isBatchableInsert(firstQuery) {
			m.executeTrueBatchedInsert(requests)
		} else if isBatchableDelete(firstQuery) {
			metrics.WriteBatchMethod.WithLabelValues("batched_delete").Inc()
			m.executeTrueBatchedDelete(requests)
		} else {
			metrics.WriteBatchMethod.WithLabelValues("prepared").Inc()
			m.executePreparedBatch(requests)
		}
	} else {
		metrics.WriteBatchMethod.WithLabelValues("transaction").Inc()
		m.executeTransactionBatch(requests)
	}
}

// isBatchableDelete checks if query is a simple DELETE FROM ... WHERE key = ?
func isBatchableDelete(query string) bool {
	// Skip leading comments and whitespace
	q := query
	for {
		q = strings.TrimSpace(q)
		if strings.HasPrefix(q, "/*") {
			// Skip comment
			endIdx := strings.Index(q, "*/")
			if endIdx == -1 {
				break
			}
			q = q[endIdx+2:]
		} else {
			break
		}
	}

	// Only support: DELETE FROM table WHERE key = ?
	if !strings.HasPrefix(strings.ToUpper(q), "DELETE FROM ") {
		return false
	}
	whereIdx := strings.Index(strings.ToUpper(q), " WHERE ")
	if whereIdx == -1 {
		return false
	}
	whereClause := q[whereIdx+7:]
	// Only support single equality predicate, e.g. id = ?
	parts := strings.Split(whereClause, "=")
	if len(parts) != 2 {
		return false
	}
	if strings.Count(whereClause, "?") != 1 {
		return false
	}
	return true
}

// executeTrueBatchedDelete combines multiple DELETEs into one DELETE ... WHERE key IN (...)
func (m *Manager) executeTrueBatchedDelete(requests []*WriteRequest) {
	// Parse key column from query
	firstQuery := requests[0].Query
	whereIdx := strings.Index(strings.ToUpper(firstQuery), " WHERE ")
	whereClause := firstQuery[whereIdx+7:]
	keyCol := strings.TrimSpace(strings.Split(whereClause, "=")[0])

	// Build combined DELETE
	baseQuery := firstQuery[:whereIdx]
	var builder strings.Builder
	builder.WriteString(baseQuery)
	builder.WriteString(" WHERE ")
	builder.WriteString(keyCol)
	builder.WriteString(" IN (")
	for i := range requests {
		if i > 0 {
			builder.WriteString(",")
		}
		builder.WriteString("?")
	}
	builder.WriteString(")")

	// Collect all key values
	allParams := make([]interface{}, 0, len(requests))
	for _, req := range requests {
		allParams = append(allParams, req.Params[0])
	}

	// Execute batched delete
	result, err := m.db.Exec(builder.String(), allParams...)
	if err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}
	affected, _ := result.RowsAffected()
	// Send results to all requests
	for _, req := range requests {
		req.ResultChan <- WriteResult{
			AffectedRows: affected, // total affected, not per-request
			BatchSize:    len(requests),
		}
		if req.OnBatchComplete != nil {
			req.OnBatchComplete(len(requests))
		}
	}
}

// executePreparedBatch executes identical queries using a prepared statement
// wrapped in a single transaction for better performance (single commit)
func (m *Manager) executePreparedBatch(requests []*WriteRequest) {
	firstQuery := requests[0].Query
	hasReturning := requests[0].HasReturning

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
			// Scan into int64 for SERIAL/BIGSERIAL columns
			var returnedValue int64
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
	// Skip leading comments and whitespace
	q := query
	for {
		q = strings.TrimSpace(q)
		if strings.HasPrefix(q, "/*") {
			// Skip comment
			endIdx := strings.Index(q, "*/")
			if endIdx == -1 {
				break
			}
			q = q[endIdx+2:]
		} else {
			break
		}
	}

	// Check for INSERT INTO ... VALUES pattern
	qUpper := strings.ToUpper(q)
	return len(q) > 12 &&
		strings.HasPrefix(qUpper, "INSERT") &&
		strings.Contains(qUpper, " VALUES") &&
		(q[len(q)-1] == ')' || q[len(q)-1] == ' ')
}

// executeTrueBatchedInsert combines multiple INSERTs into one optimized operation
// Uses PostgreSQL COPY when enabled and possible, otherwise falls back to multi-value INSERT
func (m *Manager) executeTrueBatchedInsert(requests []*WriteRequest) {
	firstQuery := requests[0].Query
	numParams := len(requests[0].Params)

	// For queries with parameters, check if we can use PostgreSQL COPY
	if m.config.UseCopy && numParams > 0 {
		isPostgres := containsPostgresPlaceholder(firstQuery)

		// Use COPY for PostgreSQL when all parameters are simple values
		if isPostgres && allParamsAreSimple(requests) {
			metrics.WriteBatchMethod.WithLabelValues("copy").Inc()
			m.executeCopyBatchedInsert(requests)
			return
		}
	}

	// Fall back to multi-row INSERT for all other cases
	metrics.WriteBatchMethod.WithLabelValues("multi_row_insert").Inc()
	m.executeTrueBatchedInsertMultiRow(requests)
}

// parseInsertStatement extracts table name and column names from INSERT statement
// Returns (tableName, []columnNames, error)
func parseInsertStatement(query string) (string, []string, error) {
	// Normalize query
	q := strings.TrimSpace(query)
	upperQ := strings.ToUpper(q)

	// Find INSERT INTO position
	insertPos := strings.Index(upperQ, "INSERT")
	if insertPos == -1 {
		return "", nil, fmt.Errorf("not an INSERT statement")
	}

	intoPos := strings.Index(upperQ[insertPos:], "INTO")
	if intoPos == -1 {
		return "", nil, fmt.Errorf("missing INTO clause")
	}
	intoPos += insertPos + 4 // Position after "INTO"

	// Find table name (between INTO and either '(' or VALUES)
	remaining := strings.TrimSpace(q[intoPos:])

	// Find the end of table name
	tableEnd := -1
	for i, ch := range remaining {
		if ch == '(' || ch == ' ' {
			tableEnd = i
			break
		}
	}

	if tableEnd == -1 {
		return "", nil, fmt.Errorf("cannot parse table name")
	}

	tableName := strings.TrimSpace(remaining[:tableEnd])

	// Find column list (between first '(' and ')')
	openParen := strings.Index(remaining, "(")
	if openParen == -1 {
		return "", nil, fmt.Errorf("missing column list")
	}

	closeParen := strings.Index(remaining[openParen:], ")")
	if closeParen == -1 {
		return "", nil, fmt.Errorf("unclosed column list")
	}

	columnList := remaining[openParen+1 : openParen+closeParen]

	// Split columns by comma
	columns := strings.Split(columnList, ",")
	for i := range columns {
		columns[i] = strings.TrimSpace(columns[i])
	}

	return tableName, columns, nil
}

// allParamsAreSimple checks if all parameters across all requests are simple values
// (not expressions, functions, or complex types)
func allParamsAreSimple(requests []*WriteRequest) bool {
	for _, req := range requests {
		for _, param := range req.Params {
			// Check if the param is a basic type
			switch param.(type) {
			case nil, bool, int, int8, int16, int32, int64,
				uint, uint8, uint16, uint32, uint64,
				float32, float64, string, []byte:
				// Simple type, continue
			default:
				// Complex type, not suitable for COPY
				return false
			}
		}
	}
	return true
}

// executeCopyBatchedInsert uses PostgreSQL COPY for bulk insert
func (m *Manager) executeCopyBatchedInsert(requests []*WriteRequest) {
	firstQuery := requests[0].Query

	// Parse INSERT statement to extract table and column names
	tableName, columns, err := parseInsertStatement(firstQuery)
	if err != nil {
		// Fall back to multi-row INSERT
		m.executeTrueBatchedInsertMultiRow(requests)
		return
	}

	// Prepare COPY statement
	txn, err := m.db.Begin()
	if err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	stmt, err := txn.Prepare(pq.CopyIn(tableName, columns...))
	if err != nil {
		txn.Rollback()
		// Fall back to multi-row INSERT
		m.executeTrueBatchedInsertMultiRow(requests)
		return
	}

	// Execute COPY with all rows
	for _, req := range requests {
		_, err := stmt.Exec(req.Params...)
		if err != nil {
			stmt.Close()
			txn.Rollback()
			for _, r := range requests {
				r.ResultChan <- WriteResult{Error: err}
			}
			return
		}
	}

	// Close the COPY statement
	_, err = stmt.Exec()
	if err != nil {
		stmt.Close()
		txn.Rollback()
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	err = stmt.Close()
	if err != nil {
		txn.Rollback()
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	// Commit transaction
	err = txn.Commit()
	if err != nil {
		for _, req := range requests {
			req.ResultChan <- WriteResult{Error: err}
		}
		return
	}

	// Send results to all requests
	// Note: COPY doesn't return LastInsertId, so we set it to 0
	for _, req := range requests {
		req.ResultChan <- WriteResult{
			AffectedRows: 1,
			LastInsertID: 0, // COPY doesn't provide last insert ID
			BatchSize:    len(requests),
		}
		// Notify connection of batch completion
		if req.OnBatchComplete != nil {
			req.OnBatchComplete(len(requests))
		}
	}
}

// executeTrueBatchedInsertMultiRow is the original multi-row INSERT implementation
func (m *Manager) executeTrueBatchedInsertMultiRow(requests []*WriteRequest) {
	firstQuery := requests[0].Query

	// Find the VALUES clause (case-insensitive)
	valuesIdx := -1
	upperQuery := strings.ToUpper(firstQuery)
	valuesPos := strings.Index(upperQuery, "VALUES")
	if valuesPos != -1 {
		valuesIdx = valuesPos + 6 // Position after "VALUES"
	}

	if valuesIdx == -1 {
		// Fallback if we can't parse
		m.executePreparedBatchFallback(requests)
		return
	}

	// Build multi-value INSERT
	baseQuery := firstQuery[:valuesIdx]
	numParams := len(requests[0].Params)

	// Handle direct queries (no parameters) - extract the VALUES clause from the original query
	if numParams == 0 {
		// Extract the VALUES clause from the original query
		valuesClause := strings.TrimSpace(firstQuery[valuesIdx:])

		// Use strings.Builder for efficient string concatenation
		var builder strings.Builder
		estimatedSize := len(baseQuery) + len(requests)*len(valuesClause) + len(requests)*2
		builder.Grow(estimatedSize)

		builder.WriteString(baseQuery)
		builder.WriteString(" ")

		// Repeat the VALUES clause for each request
		for reqIdx := range requests {
			if reqIdx > 0 {
				builder.WriteString(", ")
			}
			builder.WriteString(valuesClause)
		}

		// Execute batched query (no parameters needed)
		batchQuery := builder.String()
		result, err := m.db.Exec(batchQuery)
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
		return
	}

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
