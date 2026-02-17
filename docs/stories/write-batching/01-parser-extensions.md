# Sub-Plan 1: Parser and Type System Extensions

**Parent Story**: [WRITE_BATCHING.md](../WRITE_BATCHING.md)

**Status**: Not Started

**Estimated Effort**: 2-3 days

## Overview

Extend the SQL parser to identify and classify write operations (INSERT, UPDATE,
DELETE) and provide the necessary type system support for write batching.

## Goals

- Add write operation detection to parser
- Implement batch key generation
- Add transaction state awareness
- Support for batchable write identification

## Tasks

### 1. Extend ParsedQuery Type

- [ ] Add `IsWritable()` method to identify INSERT/UPDATE/DELETE
- [ ] Add `IsBatchable()` method to check if write can be batched
- [ ] Add `GetBatchKey()` method to generate file:line identifier
- [ ] Update existing `IsCacheable()` to exclude write operations explicitly

**File**: `parser/parser.go`

```go
// Add these methods to ParsedQuery

func (p *ParsedQuery) IsWritable() bool {
    return p.Type == QueryInsert || 
           p.Type == QueryUpdate || 
           p.Type == QueryDelete
}

func (p *ParsedQuery) IsBatchable() bool {
    // Only batch writes outside of transactions
    // Note: Transaction state is tracked at connection level
    return p.IsWritable()
}

func (p *ParsedQuery) GetBatchKey() string {
    if p.File == "" || p.Line == 0 {
        // If no file/line info, use query hash as key
        return fmt.Sprintf("query:%x", md5.Sum([]byte(p.Query)))
    }
    return fmt.Sprintf("%s:%d", p.File, p.Line)
}
```

### 2. Update Tests

**File**: `parser/parser_test.go`

- [ ] Add test for `IsWritable()` with INSERT/UPDATE/DELETE queries
- [ ] Add test for `IsBatchable()` returning true for writes
- [ ] Add test for `GetBatchKey()` with file:line hints
- [ ] Add test for `GetBatchKey()` fallback to query hash
- [ ] Add test ensuring `IsCacheable()` returns false for writes

```go
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

func TestParsedQuery_GetBatchKey(t *testing.T) {
    tests := []struct {
        query       string
        expectedKey string
    }{
        {
            "/* file:app.go line:42 */ INSERT INTO users VALUES (1)",
            "app.go:42",
        },
        {
            "/* file:src/handler.go line:100 */ UPDATE users SET active = 1",
            "src/handler.go:100",
        },
        {
            // No file/line - should use query hash
            "INSERT INTO users VALUES (1)",
            "query:", // prefix, actual hash will vary
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.query, func(t *testing.T) {
            p := Parse(tt.query)
            key := p.GetBatchKey()
            if tt.expectedKey == "query:" {
                if !strings.HasPrefix(key, "query:") {
                    t.Errorf("GetBatchKey() = %v, want prefix 'query:'", key)
                }
            } else {
                if key != tt.expectedKey {
                    t.Errorf("GetBatchKey() = %v, want %v", key, tt.expectedKey)
                }
            }
        })
    }
}
```

### 3. Documentation

- [ ] Update `docs/components/parser/README.md` with write operation detection
- [ ] Add examples of batch key generation
- [ ] Document the difference between `IsWritable()` and `IsBatchable()`

## Dependencies

- None (this is the foundation for other sub-plans)

## Deliverables

- [ ] Parser methods implemented and tested
- [ ] All tests passing
- [ ] Documentation updated
- [ ] Code reviewed

## Validation

```bash
# Run parser tests
go test ./parser -v -run "TestParsedQuery_IsWritable|TestParsedQuery_GetBatchKey|TestParsedQuery_IsBatchable"

# Run all parser tests to ensure no regression
go test ./parser -v
```

## Next Steps

After completion, proceed to:

- [02-write-batch-manager.md](02-write-batch-manager.md) - Implement the core
  batch manager
