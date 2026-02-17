# SQL Parser Component

The `parser` component is a lightweight SQL analyzer used to extract metadata
and hints from SQL queries.

## Functionality

- **Hint Extraction**: Uses regular expressions to find and parse comments in
  the format `/* ttl:60 file:user.go line:42 */`.
  - `ttl`: Cache duration in seconds.
  - `file`: Source file that issued the query.
  - `line`: Line number in the source file.
- **Query Type Detection**: Identifies whether a query is a `SELECT`, `INSERT`,
  `UPDATE`, or `DELETE` statement.
- **Cacheability Check**: Determines if a query is eligible for caching (must be
  a `SELECT` query with a `ttl` > 0).
- **Write Operation Detection**: Identifies INSERT, UPDATE, and DELETE
  operations for batching.
- **Batch Key Generation**: Groups write operations by source location
  (file:line) for efficient batching.

## Key Methods

### `IsCacheable() bool`

Returns true if query can be cached (SELECT with TTL > 0).

### `IsWritable() bool`

Returns true if query is a write operation (INSERT, UPDATE, or DELETE).

### `IsBatchable() bool`

Returns true if write operation can be batched. Currently returns the same as
`IsWritable()`, as transaction state is tracked at the connection level.

### `GetBatchKey() string`

Generates a key for grouping write operations:

- Returns `"file:line"` format when hint is present (e.g., `"app.go:42"`)
- Returns `"query:hash"` format as fallback when no file:line hint exists
- Operations from the same source location share the same batch key

**Example:**

```go
p := Parse("/* file:app.go line:42 */ INSERT INTO users (name) VALUES ('alice')")
key := p.GetBatchKey() // Returns "app.go:42"
```

## Implementation

The parser uses optimized regular expressions to extract information without the
overhead of a full SQL grammar parser, ensuring minimal latency in the proxy hot
path.

[Back to Index](../../README.md)
