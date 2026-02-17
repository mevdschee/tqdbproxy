package writebatch

import (
	"sync"
	"time"
)

// WriteRequest represents a single write operation to be batched
type WriteRequest struct {
	Query      string
	Params     []interface{}
	ResultChan chan WriteResult
	EnqueuedAt time.Time
}

// WriteResult contains the result of a write operation
type WriteResult struct {
	AffectedRows int64
	LastInsertID int64
	Error        error
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
	InitialDelayMs int // Initial delay before executing batch (1ms default)
	MaxDelayMs     int // Maximum delay before executing batch (100ms default)
	MinDelayMs     int // Minimum delay (0ms default)
	MaxBatchSize   int // Maximum number of operations per batch (1000 default)
}

// DefaultConfig returns the default configuration
func DefaultConfig() Config {
	return Config{
		InitialDelayMs: 1,
		MaxDelayMs:     100,
		MinDelayMs:     0,
		MaxBatchSize:   1000,
	}
}
