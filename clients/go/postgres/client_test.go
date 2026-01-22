package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

// MockDriver implements a minimal sql/driver for testing
type MockDriver struct {
	lastQuery string
}

func (d *MockDriver) Open(name string) (driver.Conn, error) {
	return &MockConn{d: d}, nil
}

type MockConn struct {
	d *MockDriver
}

func (c *MockConn) Prepare(query string) (driver.Stmt, error) {
	c.d.lastQuery = query
	return &MockStmt{}, nil
}

func (c *MockConn) Close() error              { return nil }
func (c *MockConn) Begin() (driver.Tx, error) { return nil, nil }

// Implement QueryerContext to intercept QueryContext calls
func (c *MockConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.d.lastQuery = query
	return &MockRows{}, nil
}

type MockStmt struct{}

func (s *MockStmt) Close() error                                    { return nil }
func (s *MockStmt) NumInput() int                                   { return 0 }
func (s *MockStmt) Exec(args []driver.Value) (driver.Result, error) { return nil, nil }
func (s *MockStmt) Query(args []driver.Value) (driver.Rows, error)  { return &MockRows{}, nil }

type MockRows struct{}

func (r *MockRows) Columns() []string              { return []string{} }
func (r *MockRows) Close() error                   { return nil }
func (r *MockRows) Next(dest []driver.Value) error { return nil }

func TestQueryWithTTL(t *testing.T) {
	mock := &MockDriver{}
	sql.Register("tqdbproxy-postgres-mock", mock)

	// Open using our mock driver
	db, err := Open("tqdbproxy-postgres-mock", "dsn")
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}

	ctx := context.Background()
	ttl := 60
	query := "SELECT * FROM users"

	// Call QueryWithTTL
	_, err = db.QueryWithTTL(ctx, ttl, query)
	if err != nil {
		t.Fatalf("QueryWithTTL failed: %v", err)
	}

	// Check if query was modified correctly
	expectedHint := "/* ttl:60 file:client_test.go"
	if !strings.Contains(mock.lastQuery, expectedHint) {
		t.Errorf("Query not properly decorated.\nGot: %q\nExpected to contain: %q", mock.lastQuery, expectedHint)
	}

	if !strings.Contains(mock.lastQuery, query) {
		t.Errorf("Original query not found.\nGot: %q", mock.lastQuery)
	}

	// Verify line number is present
	if !strings.Contains(mock.lastQuery, "line:") {
		t.Errorf("Line number not found in query.\nGot: %q", mock.lastQuery)
	}
}

func TestQueryRowWithTTL(t *testing.T) {
	mock := &MockDriver{}
	sql.Register("tqdbproxy-postgres-mock-row", mock)

	// Open using our mock driver
	db, err := Open("tqdbproxy-postgres-mock-row", "dsn")
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}

	ctx := context.Background()
	ttl := 120
	query := "SELECT name FROM users WHERE id = $1"

	// Call QueryRowWithTTL
	_ = db.QueryRowWithTTL(ctx, ttl, query, 1)

	// Check if query was modified correctly
	expectedHint := "/* ttl:120 file:client_test.go"
	if !strings.Contains(mock.lastQuery, expectedHint) {
		t.Errorf("Query not properly decorated.\nGot: %q\nExpected to contain: %q", mock.lastQuery, expectedHint)
	}

	if !strings.Contains(mock.lastQuery, query) {
		t.Errorf("Original query not found.\nGot: %q", mock.lastQuery)
	}

	// Verify line number is present
	if !strings.Contains(mock.lastQuery, "line:") {
		t.Errorf("Line number not found in query.\nGot: %q", mock.lastQuery)
	}
}
