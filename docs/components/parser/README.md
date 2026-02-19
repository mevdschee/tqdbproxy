# SQL Parser Component

The `parser` component is a lightweight SQL analyzer used to extract metadata
and hints from SQL queries.

## Functionality

- **Hint Extraction**: Uses regular expressions to find and parse comments in
  the format `/* ttl:60 file:user.go line:42 batch:10 */`.
  - `ttl`: Cache duration in seconds (SELECT queries only).
  - `file`: Source file that issued the query.
  - `line`: Line number in the source file.
  - `batch`: Maximum batching window in milliseconds (write operations only).
- **Query Type Detection**: Identifies whether a query is a `SELECT`, `INSERT`,
  `UPDATE`, or `DELETE` statement.
- **Cacheability Check**: Determines if a query is eligible for caching (must be
  a `SELECT` query with a `ttl` > 0).
- **Write Operation Detection**: Identifies INSERT, UPDATE, and DELETE
  operations for batching.
- **Batch Key Generation**: Groups write operations by normalized query text for
  efficient batching.

## Key Methods

### `IsCacheable() bool`

Returns true if query can be cached (SELECT with TTL > 0).

### `IsWritable() bool`

Returns true if query is a write operation (INSERT, UPDATE, or DELETE).

### `IsBatchable() bool`

Returns true if write operation can be batched. A query is batchable if:

- It's a write operation (INSERT, UPDATE, or DELETE)
- It has a batch hint with `batch:N > 0`

Transaction state is tracked at the connection level - batching is disabled
inside transactions.

### `GetBatchKey() string`

Generates a key for grouping write operations for batching:

- Returns the normalized query text (with hints stripped)
- Identical queries batch together, regardless of hint differences
- Different queries create separate batches
- Parameter values are part of the key (different values = different batches)

**Examples:**

```go
// These have the same batch key (hints stripped):
p1 := Parse("/* file:app.go line:42 batch:10 */ INSERT INTO users (name) VALUES ('alice')")
p2 := Parse("/* file:handler.go line:100 batch:10 */ INSERT INTO users (name) VALUES ('alice')")
p1.GetBatchKey() == p2.GetBatchKey() // true - both return "INSERT INTO users (name) VALUES ('alice')"

// These have different batch keys (different values):
p3 := Parse("/* batch:10 */ INSERT INTO users (name) VALUES ('alice')")
p4 := Parse("/* batch:10 */ INSERT INTO users (name) VALUES ('bob')")
p3.GetBatchKey() != p4.GetBatchKey() // true - different parameter values
```

See the [Write Batching Component](../writebatch/README.md) for details on how
batch keys are used.

## Implementation

The parser uses optimized regular expressions to extract information without the
overhead of a full SQL grammar parser, ensuring minimal latency in the proxy hot
path.

[Back to Index](../../README.md)
