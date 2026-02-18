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
