package parser

import (
	"testing"
)

func TestParse_QueryType(t *testing.T) {
	tests := []struct {
		query    string
		expected QueryType
	}{
		{"SELECT * FROM users", QuerySelect},
		{"select id from users", QuerySelect},
		{"INSERT INTO users (name) VALUES ('test')", QueryInsert},
		{"UPDATE users SET name = 'test'", QueryUpdate},
		{"DELETE FROM users WHERE id = 1", QueryDelete},
		{"SHOW TABLES", QueryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.Type != tt.expected {
				t.Errorf("Parse(%q).Type = %v, want %v", tt.query, p.Type, tt.expected)
			}
		})
	}
}

func TestParse_TTLHint(t *testing.T) {
	tests := []struct {
		query       string
		expectedTTL int
	}{
		{"/* ttl:60 */ SELECT * FROM users", 60},
		{"/* ttl:300 */ SELECT * FROM users", 300},
		{"/*ttl:30*/ SELECT * FROM users", 30},
		{"SELECT * FROM users", 0},
		{"/* ttl:0 */ SELECT * FROM users", 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.TTL != tt.expectedTTL {
				t.Errorf("Parse(%q).TTL = %v, want %v", tt.query, p.TTL, tt.expectedTTL)
			}
		})
	}
}

func TestParse_FileLineHints(t *testing.T) {
	tests := []struct {
		query        string
		expectedFile string
		expectedLine int
	}{
		{"/* file:user.go line:42 */ SELECT * FROM users", "user.go", 42},
		{"/* ttl:60 file:api.go line:100 */ SELECT * FROM users", "api.go", 100},
		{"SELECT * FROM users", "", 0},
		// File paths with forward slashes
		{"/* file:src/controllers/UserController.php line:123 */ SELECT * FROM users", "src/controllers/UserController.php", 123},
		{"/* ttl:60 file:app/models/User.go line:55 */ SELECT * FROM users", "app/models/User.go", 55},
		{"/* file:/var/www/html/index.php line:10 */ SELECT * FROM users", "/var/www/html/index.php", 10},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.File != tt.expectedFile {
				t.Errorf("Parse(%q).File = %q, want %q", tt.query, p.File, tt.expectedFile)
			}
			if p.Line != tt.expectedLine {
				t.Errorf("Parse(%q).Line = %v, want %v", tt.query, p.Line, tt.expectedLine)
			}
		})
	}
}

func TestParsedQuery_IsCacheable(t *testing.T) {
	tests := []struct {
		query    string
		expected bool
	}{
		{"/* ttl:60 */ SELECT id FROM users", true},
		{"SELECT * FROM users", false},                       // No TTL
		{"/* ttl:60 */ INSERT INTO users VALUES (1)", false}, // Not SELECT
		{"/* ttl:0 */ SELECT * FROM users", false},           // TTL is 0
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.IsCacheable() != tt.expected {
				t.Errorf("Parse(%q).IsCacheable() = %v, want %v", tt.query, p.IsCacheable(), tt.expected)
			}
		})
	}
}

func TestCacheKey_DifferentParams(t *testing.T) {
	// Cache key is Query field - same query with different params should differ
	q1 := Parse("/* ttl:60 */ SELECT * FROM users WHERE id = 1")
	q2 := Parse("/* ttl:60 */ SELECT * FROM users WHERE id = 2")

	// Using Query as cache key means different params = different keys
	if q1.Query == q2.Query {
		t.Errorf("Queries with different parameters should have different cache keys")
	}

	// Identical queries should have same cache key
	q3 := Parse("/* ttl:60 */ SELECT * FROM users WHERE id = 1")
	if q1.Query != q3.Query {
		t.Errorf("Identical queries should have same cache key")
	}
}

func TestParsedQuery_IsWritable(t *testing.T) {
	tests := []struct {
		query    string
		writable bool
	}{
		{"SELECT * FROM users", false},
		{"INSERT INTO users (name) VALUES ('test')", true},
		{"UPDATE users SET name = 'test'", true},
		{"DELETE FROM users WHERE id = 1", true},
		{"BEGIN", false},
		{"COMMIT", false},
		{"ROLLBACK", false},
		{"/* file:app.go line:10 */ INSERT INTO logs VALUES (1)", true},
		{"/* ttl:60 */ SELECT * FROM users", false},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.IsWritable() != tt.writable {
				t.Errorf("IsWritable() = %v, want %v", p.IsWritable(), tt.writable)
			}
		})
	}
}

func TestParsedQuery_IsBatchable(t *testing.T) {
	tests := []struct {
		query     string
		batchable bool
	}{
		{"INSERT INTO users (name) VALUES ('test')", true},
		{"UPDATE users SET name = 'test'", true},
		{"DELETE FROM users WHERE id = 1", true},
		{"SELECT * FROM users", false},
		{"BEGIN", false},
		{"/* file:app.go line:42 */ INSERT INTO logs VALUES (1)", true},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.IsBatchable() != tt.batchable {
				t.Errorf("IsBatchable() = %v, want %v", p.IsBatchable(), tt.batchable)
			}
		})
	}
}

func TestParsedQuery_GetBatchKey(t *testing.T) {
	tests := []struct {
		query       string
		expectedKey string
	}{
		{
			"/* file:app.go line:42 */ INSERT INTO users VALUES (1)",
			"INSERT INTO users VALUES (1)", // Hint is stripped
		},
		{
			"/* file:src/handler.go line:100 */ UPDATE users SET active = 1",
			"UPDATE users SET active = 1",
		},
		{
			"INSERT INTO users VALUES (1)",
			"INSERT INTO users VALUES (1)",
		},
		{
			"INSERT INTO users VALUES (2)",
			"INSERT INTO users VALUES (2)", // Different values = different key
		},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			key := p.GetBatchKey()
			if key != tt.expectedKey {
				t.Errorf("GetBatchKey() = %v, want %v", key, tt.expectedKey)
			}
		})
	}
}

func TestParsedQuery_GetBatchKey_SameQuery(t *testing.T) {
	// Identical queries should have the same batch key
	q1 := Parse("/* file:app.go line:42 */ INSERT INTO users (name) VALUES ('alice')")
	q2 := Parse("/* file:handler.go line:10 */ INSERT INTO users (name) VALUES ('alice')")

	key1 := q1.GetBatchKey()
	key2 := q2.GetBatchKey()

	// Same query (hints stripped) = same batch key
	if key1 != key2 {
		t.Errorf("Identical queries should have same batch key: %v != %v", key1, key2)
	}

	expected := "INSERT INTO users (name) VALUES ('alice')"
	if key1 != expected {
		t.Errorf("GetBatchKey() = %v, want %v", key1, expected)
	}
}

func TestParsedQuery_GetBatchKey_DifferentQueries(t *testing.T) {
	// Different queries should have different batch keys
	q1 := Parse("INSERT INTO users (name) VALUES ('alice')")
	q2 := Parse("INSERT INTO users (name) VALUES ('bob')")

	key1 := q1.GetBatchKey()
	key2 := q2.GetBatchKey()

	if key1 == key2 {
		t.Errorf("Different queries should have different batch keys")
	}

	if key1 != "INSERT INTO users (name) VALUES ('alice')" {
		t.Errorf("GetBatchKey() for q1 = %v, want 'INSERT INTO users (name) VALUES ('alice')'", key1)
	}

	if key2 != "INSERT INTO users (name) VALUES ('bob')" {
		t.Errorf("GetBatchKey() for q2 = %v, want 'INSERT INTO users (name) VALUES ('bob')'", key2)
	}
}

func TestParsedQuery_GetBatchKey_ConsistentWithCacheKey(t *testing.T) {
	// GetBatchKey should use Query field, same as caching does
	query1 := "/* ttl:60 */ SELECT * FROM users WHERE id = 1"
	query2 := "INSERT INTO users (name) VALUES ('test')"

	p1 := Parse(query1)
	p2 := Parse(query2)

	// For caching, we use p.Query (cleaned query)
	// For batching, GetBatchKey() should also return p.Query
	if p1.GetBatchKey() != p1.Query {
		t.Errorf("GetBatchKey() should return Query field for consistency with caching")
	}

	if p2.GetBatchKey() != p2.Query {
		t.Errorf("GetBatchKey() should return Query field for consistency with caching")
	}
}

func TestParsedQuery_IsCacheable_ExcludesWrites(t *testing.T) {
	// Ensure IsCacheable explicitly excludes write operations
	tests := []struct {
		query    string
		expected bool
	}{
		{"/* ttl:60 */ SELECT * FROM users", true},
		{"/* ttl:60 */ INSERT INTO users VALUES (1)", false},
		{"/* ttl:60 */ UPDATE users SET active = 1", false},
		{"/* ttl:60 */ DELETE FROM users WHERE id = 1", false},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.IsCacheable() != tt.expected {
				t.Errorf("IsCacheable() = %v, want %v (writes should not be cacheable)", p.IsCacheable(), tt.expected)
			}
		})
	}
}

func TestParsedQuery_PreparedStatements(t *testing.T) {
	// Test that prepared statements are correctly identified as batchable
	tests := []struct {
		query     string
		writeable bool
		batchable bool
	}{
		{"INSERT INTO users (name, email) VALUES (?, ?)", true, true},
		{"UPDATE users SET name = ? WHERE id = ?", true, true},
		{"DELETE FROM users WHERE id = ?", true, true},
		{"SELECT * FROM users WHERE id = ?", false, false},
		{"INSERT INTO users (name) VALUES ($1)", true, true},
		{"UPDATE users SET active = $1 WHERE id = $2", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.IsWritable() != tt.writeable {
				t.Errorf("IsWritable() = %v, want %v", p.IsWritable(), tt.writeable)
			}
			if p.IsBatchable() != tt.batchable {
				t.Errorf("IsBatchable() = %v, want %v", p.IsBatchable(), tt.batchable)
			}
		})
	}
}

func TestParsedQuery_WriteTTLWarning(t *testing.T) {
	// Test that TTL is ignored for write operations
	tests := []struct {
		query       string
		expectedTTL int
		description string
	}{
		{
			query:       "/* ttl:60 */ INSERT INTO users VALUES (1)",
			expectedTTL: 0,
			description: "INSERT with TTL should have TTL set to 0",
		},
		{
			query:       "/* ttl:300 */ UPDATE users SET active = 1",
			expectedTTL: 0,
			description: "UPDATE with TTL should have TTL set to 0",
		},
		{
			query:       "/* ttl:120 */ DELETE FROM users WHERE id = 1",
			expectedTTL: 0,
			description: "DELETE with TTL should have TTL set to 0",
		},
		{
			query:       "/* ttl:60 */ SELECT * FROM users",
			expectedTTL: 60,
			description: "SELECT with TTL should preserve TTL",
		},
		{
			query:       "INSERT INTO users VALUES (1)",
			expectedTTL: 0,
			description: "INSERT without TTL should have TTL 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			p := Parse(tt.query)
			if p.TTL != tt.expectedTTL {
				t.Errorf("Parse(%q).TTL = %v, want %v: %s", tt.query, p.TTL, tt.expectedTTL, tt.description)
			}
		})
	}
}

func TestParsedQuery_BatchMs(t *testing.T) {
	tests := []struct {
		query      string
		expectedMs int
	}{
		{
			"INSERT INTO users VALUES (1)",
			0, // No hint = no batching
		},
		{
			"/* batch:10 */ INSERT INTO users VALUES (1)",
			10,
		},
		{
			"/* batch:50 */ UPDATE users SET active = 1",
			50,
		},
		{
			"/* file:app.go line:42 batch:5 */ DELETE FROM cache",
			5,
		},
		{
			"/* ttl:60 batch:25 */ SELECT * FROM users",
			25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.BatchMs != tt.expectedMs {
				t.Errorf("BatchMs = %v, want %v", p.BatchMs, tt.expectedMs)
			}
		})
	}
}

func TestParsedQuery_BatchMs_InvalidValues(t *testing.T) {
	tests := []struct {
		query      string
		expectedMs int
	}{
		{
			"/* batch:-10 */ INSERT INTO users VALUES (1)",
			0, // Negative values default to 0
		},
		{
			"/* batch:abc */ INSERT INTO users VALUES (1)",
			0, // Invalid values default to 0
		},
		{
			"/* batch:1000000 */ INSERT INTO users VALUES (1)",
			100, // Cap at max (100ms)
		},
		{
			"/* batch:150 */ INSERT INTO users VALUES (1)",
			100, // Values > 100 capped at 100ms
		},
		{
			"/* batch:0 */ INSERT INTO users VALUES (1)",
			0, // Explicit 0 is allowed
		},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			p := Parse(tt.query)
			if p.BatchMs != tt.expectedMs {
				t.Errorf("BatchMs = %v, want %v", p.BatchMs, tt.expectedMs)
			}
		})
	}
}
