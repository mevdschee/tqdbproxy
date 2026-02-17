package writebatch

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"time"
)

// Manager handles batching of write operations
type Manager struct {
	groups       sync.Map // map[string]*BatchGroup
	config       Config
	db           *sql.DB
	currentDelay atomic.Int64 // in microseconds
	closed       atomic.Bool
}

// New creates a new write batch manager
func New(db *sql.DB, config Config) *Manager {
	m := &Manager{
		db:     db,
		config: config,
	}
	m.currentDelay.Store(int64(config.InitialDelayMs * 1000))
	return m
}

// Enqueue adds a write operation to the batch queue and waits for its result
func (m *Manager) Enqueue(ctx context.Context, batchKey, query string, params []interface{}) WriteResult {
	if m.closed.Load() {
		return WriteResult{Error: ErrManagerClosed}
	}

	req := &WriteRequest{
		Query:      query,
		Params:     params,
		ResultChan: make(chan WriteResult, 1),
		EnqueuedAt: time.Now(),
	}

	// Get or create batch group
	groupInterface, _ := m.groups.LoadOrStore(batchKey, &BatchGroup{
		BatchKey:  batchKey,
		Requests:  make([]*WriteRequest, 0, m.config.MaxBatchSize),
		FirstSeen: time.Now(),
	})
	group := groupInterface.(*BatchGroup)

	group.mu.Lock()
	isFirst := len(group.Requests) == 0
	if group.Requests == nil {
		// Group has been processed, this shouldn't happen but handle it
		group.mu.Unlock()
		// Retry with a fresh lookup
		return m.Enqueue(ctx, batchKey, query, params)
	}
	group.Requests = append(group.Requests, req)
	currentSize := len(group.Requests)

	if isFirst {
		// First request - start timer
		delay := time.Duration(m.currentDelay.Load()) * time.Microsecond
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

// SetDelay updates the current delay for new batches (for adaptive delay system)
func (m *Manager) SetDelay(delayMicros int64) {
	m.currentDelay.Store(delayMicros)
}

// GetDelay returns the current delay in microseconds
func (m *Manager) GetDelay() int64 {
	return m.currentDelay.Load()
}

// Close shuts down the manager and waits for in-flight batches
func (m *Manager) Close() error {
	m.closed.Store(true)
	// Wait for in-flight batches to complete
	time.Sleep(time.Duration(m.config.MaxDelayMs) * time.Millisecond * 2)
	return nil
}
