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
			// Cap at 100ms and reject negative values
			if batchMs < 0 {
				batchMs = 0
			} else if batchMs > 100 {
				batchMs = 100
			}
			p.BatchMs = batchMs
		}
		// Remove the hint comment from the query
		p.Query = hintRegex.ReplaceAllString(query, "")
		p.Query = strings.TrimSpace(p.Query)
	}

	// TTL is silently ignored for writes
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
// Only INSERT queries can be batched - UPDATE and DELETE have different
// WHERE clauses and cannot be combined into multi-value statements
// Note: Transaction state is tracked at connection level
func (p *ParsedQuery) IsBatchable() bool {
	return p.Type == QueryInsert
}

// GetBatchKey returns a key for grouping writes for batching
// Uses the same approach as cache keys (the query itself)
func (p *ParsedQuery) GetBatchKey() string {
	return p.Query
}
