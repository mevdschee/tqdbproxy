package writebatch

import "errors"

var (
	// ErrManagerClosed is returned when operations are attempted on a closed manager
	ErrManagerClosed = errors.New("write batch manager is closed")

	// ErrTimeout is returned when a write operation times out
	ErrTimeout = errors.New("write batch operation timeout")

	// ErrBatchFull is returned when a batch group is full
	ErrBatchFull = errors.New("batch group is full")
)
