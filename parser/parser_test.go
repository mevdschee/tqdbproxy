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
