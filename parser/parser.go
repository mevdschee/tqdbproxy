// Package parser provides lightweight SQL query analysis for extracting
// metadata hints and query classification.
//
// The parser extracts SQL comment hints in the format:
//
//	/* ttl:60 file:app.go line:42 batch:10 */
//
// Where:
//   - ttl: Cache TTL in seconds (SELECT queries only)
//   - file: Source file name (for metrics and debugging)
//   - line: Line number in source file
//   - batch: Maximum batching window in milliseconds (write operations only)
//
// The parser is intentionally lightweight, using regex patterns rather than
// a full SQL grammar parser to minimize latency in the proxy hot path.
package parser

import (
	"regexp"
	"strconv"
	"strings"
)

// QueryType represents the type of SQL query
type QueryType int

const (
	QueryUnknown QueryType = iota
	QuerySelect
	QueryInsert
	QueryUpdate
	QueryDelete
)

// ParsedQuery contains extracted information from a SQL query
type ParsedQuery struct {
	Type    QueryType
	TTL     int    // TTL in seconds, 0 means no caching
	DB      string // Database name from FQN
	File    string // Source file from hint
	Line    int    // Source line from hint
	BatchMs int    // Maximum wait time for batching in ms (0 = no batching)
	Query   string // Original query
}

var (
	// Match /* ttl:60 */ or /*ttl:60*/ or /* ttl:60 file:user.go line:42 batch:10 */
	hintRegex = regexp.MustCompile(`/\*\s*(ttl:(\d+))?\s*(file:(\S+))?\s*(line:(\d+))?\s*(batch:(\d+))?\s*\*/`)
	// Match query type (allows comments before keyword)
	queryTypeRegex = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE)\b`)
	// Match Fully Qualified Names (FQN) like db.table or `db`.`table`
	fqnRegex = regexp.MustCompile("(?i)\\b(?:FROM|JOIN|INTO|UPDATE)\\s+(['\"`]?)([a-zA-Z0-9_$]+)['\"`]?\\s*\\.\\s*(['\"`]?)([a-zA-Z0-9_$]+)['\"`]?")
	// Match string literals
	stringLiteralRegex = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	// Match numbers
	numberRegex = regexp.MustCompile(`\b\d+\.?\d*\b`)
)

// Parse extracts metadata from a SQL query
func Parse(query string) *ParsedQuery {
	p := &ParsedQuery{
		Query: query,
		Type:  QueryUnknown,
	}

	// Determine query type
	if matches := queryTypeRegex.FindStringSubmatch(query); matches != nil {
		switch strings.ToUpper(matches[1]) {
		case "SELECT":
			p.Type = QuerySelect
		case "INSERT":
			p.Type = QueryInsert
		case "UPDATE":
			p.Type = QueryUpdate
		case "DELETE":
			p.Type = QueryDelete
		}
	}

	// Extract database from FQN
	if matches := fqnRegex.FindStringSubmatch(query); matches != nil {
		p.DB = matches[2]
	}

	// Extract hints from comments
	if matches := hintRegex.FindStringSubmatch(query); matches != nil {
		if matches[2] != "" {
			p.TTL, _ = strconv.Atoi(matches[2])
		}
		if matches[4] != "" {
			p.File = matches[4]
		}
		if matches[6] != "" {
			p.Line, _ = strconv.Atoi(matches[6])
		}
		if matches[8] != "" {
			batchMs, _ := strconv.Atoi(matches[8])
			// Reject negative values - batching delay must be non-negative
			if batchMs < 0 {
				batchMs = 0
			}
			p.BatchMs = batchMs
		}
		// Remove the hint comment from the query so it's not sent to backend
		// This also ensures identical queries batch together regardless of hint differences
		p.Query = hintRegex.ReplaceAllString(query, "")
		p.Query = strings.TrimSpace(p.Query)
	}

	// TTL is silently ignored for writes - caching only applies to SELECT queries
	if p.IsWritable() && p.TTL > 0 {
		p.TTL = 0
	}

	return p
}

// IsCacheable returns true if query can be cached
func (p *ParsedQuery) IsCacheable() bool {
	return p.Type == QuerySelect && p.TTL > 0
}

// IsWritable returns true if query is a write operation (INSERT, UPDATE, DELETE)
func (p *ParsedQuery) IsWritable() bool {
	return p.Type == QueryInsert ||
		p.Type == QueryUpdate ||
		p.Type == QueryDelete
}

// IsBatchable returns true if write can be batched
// INSERTs, UPDATEs, and DELETEs can be batched when they have a batch hint
//
// Note: For UPDATE and DELETE, batching works when queries are identical.
// Transaction state is tracked at connection level - batching is disabled
// inside transactions to maintain ACID guarantees.
func (p *ParsedQuery) IsBatchable() bool {
	// A query is batchable if it's a write operation and has a batch hint > 0
	return (p.Type == QueryInsert || p.Type == QueryUpdate || p.Type == QueryDelete) && p.BatchMs > 0
}

// GetBatchKey returns a key for grouping writes for batching
//
// The batch key is the normalized query (with hints stripped). This ensures:
//   - Identical queries batch together, regardless of hint metadata
//   - Different queries create separate batches
//   - Parameter values are part of the key (different values = different batches)
//
// Example:
//
//	Query 1: "/* batch:10 file:a.go line:1 */ INSERT INTO t VALUES (1)"
//	Query 2: "/* batch:10 file:b.go line:2 */ INSERT INTO t VALUES (1)"
//	Both have batch key: "INSERT INTO t VALUES (1)" - they batch together
//
//	Query 3: "/* batch:10 */ INSERT INTO t VALUES (2)"
//	Has batch key: "INSERT INTO t VALUES (2)" - separate batch
func (p *ParsedQuery) GetBatchKey() string {
	return p.Query
}
