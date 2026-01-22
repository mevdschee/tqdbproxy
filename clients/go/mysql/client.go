package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
)

// DB wraps sql.DB to provide TTL-aware query methods
type DB struct {
	*sql.DB
}

// Open opens a database specified by its database driver name and a
// driver-specific data source name, typically consisting of at least a
// database name and connection information.
func Open(driverName, dataSourceName string) (*DB, error) {
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, err
	}
	return &DB{DB: db}, nil
}

// Wrap wraps an existing *sql.DB
func Wrap(db *sql.DB) *DB {
	return &DB{DB: db}
}

// QueryWithTTL executes a query with a cache TTL hint and caller metadata
func (db *DB) QueryWithTTL(ctx context.Context, ttl int, query string, args ...any) (*sql.Rows, error) {
	// Capture caller info
	_, file, line, ok := runtime.Caller(1)
	if !ok {
		file = "unknown"
		line = 0
	} else {
		file = filepath.Base(file)
	}

	// Construct hint
	hint := fmt.Sprintf("/* ttl:%d file:%s line:%d */", ttl, file, line)

	// Prepend hint to query
	hintedQuery := hint + " " + query

	// Execute query
	return db.DB.QueryContext(ctx, hintedQuery, args...)
}

// QueryRowWithTTL executes a query that is expected to return at most one row
func (db *DB) QueryRowWithTTL(ctx context.Context, ttl int, query string, args ...any) *sql.Row {
	// Capture caller info
	_, file, line, ok := runtime.Caller(1)
	if !ok {
		file = "unknown"
		line = 0
	} else {
		file = filepath.Base(file)
	}

	hint := fmt.Sprintf("/* ttl:%d file:%s line:%d */", ttl, file, line)
	hintedQuery := hint + " " + query

	return db.DB.QueryRowContext(ctx, hintedQuery, args...)
}
