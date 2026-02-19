// Package writebatch implements automatic batching of write operations
// (INSERT, UPDATE, DELETE) to improve database throughput.
//
// The batching system uses a hint-based approach where clients specify
// batching windows via SQL comment hints:
//
//	/* batch:10 */ INSERT INTO logs (message) VALUES (?)
//
// How it works:
//  1. Parser extracts batch hint (BatchMs) from SQL comment
//  2. Write operation is added to a batch group based on its batch key
//  3. First operation in group starts a timer for BatchMs milliseconds
//  4. Additional operations join the batch until timer expires or max size reached
//  5. Batch executes and each operation receives its individual result
//
// Key features:
//   - Automatic grouping by query structure
//   - Configurable batch windows (0-1000ms per query)
//   - Maximum batch size limit (default 1000 operations)
//   - Transaction-aware (batching disabled inside transactions)
//   - Per-operation result delivery with batch size metadata
//
// See docs/components/writebatch/README.md for detailed documentation.
package writebatch

import (
	"sync"
	"time"
)

// WriteRequest represents a single write operation to be batched
type WriteRequest struct {
	Query           string
	Params          []interface{}
	ResultChan      chan WriteResult
	EnqueuedAt      time.Time
	OnBatchComplete func(batchSize int) // Called when batch executes to update connection state
	HasReturning    bool                // True if query has RETURNING clause
}

// WriteResult contains the result of a write operation
type WriteResult struct {
	AffectedRows    int64
	LastInsertID    int64
	BatchSize       int           // Number of operations in the batch that executed this request
	ReturningValues []interface{} // Values returned by RETURNING clause
	Error           error
}

// BatchGroup holds a group of write requests with the same batch key
type BatchGroup struct {
	BatchKey  string
	Requests  []*WriteRequest
	FirstSeen time.Time
	mu        sync.Mutex
	timer     *time.Timer
}

// Config holds configuration for the write batch manager
type Config struct {
	MaxBatchSize int // Maximum number of operations per batch (1000 default)
}

// DefaultConfig returns the default configuration
func DefaultConfig() Config {
	return Config{
		MaxBatchSize: 1000,
	}
}

// DefaultMaxBatchSize is the default maximum batch size
const DefaultMaxBatchSize = 1000
