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
	Type  QueryType
	TTL   int    // TTL in seconds, 0 means no caching
	File  string // Source file from hint
	Line  int    // Source line from hint
	Query string // Original query
}

var (
	// Match /* ttl:60 */ or /*ttl:60*/ or /* ttl:60 file:user.go line:42 */
	hintRegex = regexp.MustCompile(`/\*\s*(ttl:(\d+))?\s*(file:(\S+))?\s*(line:(\d+))?\s*\*/`)
	// Match query type (allows comments before keyword)
	queryTypeRegex = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE)\b`)
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

	return p
}

// IsCacheable returns true if query can be cached
func (p *ParsedQuery) IsCacheable() bool {
	return p.Type == QuerySelect && p.TTL > 0
}
