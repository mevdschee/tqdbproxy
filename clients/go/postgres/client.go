// Package postgres provides a PostgreSQL client wrapper that adds cache TTL hints
// and caller metadata to SQL queries for use with tqdbproxy.
//
// The package wraps database/sql and provides TTL-aware query methods that
// automatically inject SQL comment hints containing cache TTL and caller
// location information.
//
// Usage:
//
//	import (
//		postgres "github.com/mevdschee/tqdbproxy/clients/go/postgres"
//		_ "github.com/lib/pq" // PostgreSQL driver
//	)
//
//	// Open a new connection
//	db, err := postgres.Open("postgres", "postgres://user:pass@localhost/db")
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer db.Close()
//
//	// Standard query (no caching)
//	rows, err := db.Query("SELECT * FROM users WHERE id = $1", 1)
//
//	// Query with 60-second cache TTL
//	ctx := context.Background()
//	rows, err = db.QueryWithTTL(ctx, 60, "SELECT * FROM users WHERE id = $1", 1)
//
// The QueryWithTTL method will automatically prepend a SQL comment hint like:
// /* ttl:60 file:main.go line:42 */ SELECT * FROM users WHERE id = $1
//
// This hint is used by tqdbproxy to cache query results with the specified TTL.
package postgres

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
