package writebatch

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
	group.Requests = nil
	group.mu.Unlock()

	// Try to delete this group from the map (it might already be deleted if batch was full)
	m.groups.CompareAndDelete(batchKey, group)

	if batchSize == 0 {
		return
	}

	if batchSize == 1 {
		m.executeSingle(requests[0])
	} else {
		m.executeBatchedWrites(requests)
	}

	// Update throughput metrics
	m.updateThroughput(batchSize)
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
func (m *Manager) executePreparedBatch(requests []*WriteRequest) {
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
	}
}
