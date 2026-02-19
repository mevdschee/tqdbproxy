package writebatch

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Manager handles batching of write operations
type Manager struct {
	groups     sync.Map // map[string]*BatchGroup
	config     Config
	db         *sql.DB
	closed     atomic.Bool
	batchCount atomic.Int64
}

// BatchCount returns the total number of batches executed since the manager was created.
func (m *Manager) BatchCount() int64 {
	return m.batchCount.Load()
}

// New creates a new write batch manager
func New(db *sql.DB, config Config) *Manager {
	return &Manager{
		db:     db,
		config: config,
	}
}

// Enqueue adds a write operation to the batch queue and waits for its result
// batchMs is the maximum wait time in milliseconds (0 = execute immediately)
func (m *Manager) Enqueue(ctx context.Context, batchKey, query string, params []interface{}, batchMs int, onBatchComplete func(int)) WriteResult {
	hasReturning := hasReturningClause(query)
	log.Printf("[WriteBatch] Enqueue called: query=%q, numParams=%d, batchMs=%d, hasReturning=%v", query, len(params), batchMs, hasReturning)

	if m.closed.Load() {
		return WriteResult{Error: ErrManagerClosed}
	}

	// If no wait time specified, execute immediately (no batching)
	if batchMs == 0 {
		log.Printf("[WriteBatch] Executing immediately (batchMs=0)")
		result := m.executeImmediate(ctx, query, params)
		// Call callback even for immediate execution
		if onBatchComplete != nil {
			onBatchComplete(result.BatchSize)
		}
		return result
	}

	req := &WriteRequest{
		Query:           query,
		Params:          params,
		ResultChan:      make(chan WriteResult, 1),
		EnqueuedAt:      time.Now(),
		OnBatchComplete: onBatchComplete,
		HasReturning:    hasReturning,
	}

	// Get or create batch group
	groupInterface, loaded := m.groups.Load(batchKey)
	if !loaded {
		// Group doesn't exist, create it
		newGroup := &BatchGroup{
			BatchKey:  batchKey,
			Requests:  make([]*WriteRequest, 0, m.config.MaxBatchSize),
			FirstSeen: time.Now(),
		}
		groupInterface, loaded = m.groups.LoadOrStore(batchKey, newGroup)
	}
	group := groupInterface.(*BatchGroup)

	group.mu.Lock()
	isFirst := len(group.Requests) == 0
	if group.Requests == nil {
		// Group has been processed, this shouldn't happen but handle it
		group.mu.Unlock()
		// Retry with a fresh lookup
		return m.Enqueue(ctx, batchKey, query, params, batchMs, onBatchComplete)
	}
	group.Requests = append(group.Requests, req)
	currentSize := len(group.Requests)

	if isFirst {
		// First request - start timer with specified delay
		delay := time.Duration(batchMs) * time.Millisecond
		group.timer = time.AfterFunc(delay, func() {
			m.executeBatch(batchKey, group)
		})
		group.mu.Unlock()
	} else if currentSize >= m.config.MaxBatchSize {
		// Batch full - execute immediately
		timer := group.timer
		// Delete group from map so new requests create a fresh batch
		m.groups.Delete(batchKey)
		group.mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		go m.executeBatch(batchKey, group)
	} else {
		group.mu.Unlock()
	}

	// Wait for result
	select {
	case result := <-req.ResultChan:
		return result
	case <-ctx.Done():
		return WriteResult{Error: ctx.Err()}
	case <-time.After(30 * time.Second):
		return WriteResult{Error: ErrTimeout}
	}
}

// executeImmediate executes a query immediately without batching
func (m *Manager) executeImmediate(ctx context.Context, query string, params []interface{}) WriteResult {
	log.Printf("[WriteBatch] executeImmediate: query=%q, params=%v", query, params)
	result, err := m.db.ExecContext(ctx, query, params...)
	if err != nil {
		log.Printf("[WriteBatch] executeImmediate ERROR: %v", err)
		return WriteResult{Error: err}
	}

	affected, _ := result.RowsAffected()
	lastID, _ := result.LastInsertId()

	return WriteResult{
		AffectedRows: affected,
		LastInsertID: lastID,
	}
}

// Close shuts down the manager and waits for in-flight batches
func (m *Manager) Close() error {
	m.closed.Store(true)
	// Wait a moment for in-flight batches to complete
	time.Sleep(200 * time.Millisecond)
	return nil
}

// hasReturningClause checks if a query contains a RETURNING clause
func hasReturningClause(query string) bool {
	// Simple case-insensitive check for RETURNING keyword
	q := strings.ToUpper(query)
	return strings.Contains(q, " RETURNING ")
}
