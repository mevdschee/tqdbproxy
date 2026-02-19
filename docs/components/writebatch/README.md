# Write Batching Component

The `writebatch` component provides automatic batching of write operations
(INSERT, UPDATE, DELETE) to improve throughput and reduce database load. It uses
a hint-based approach where clients specify batching windows via SQL comments.

## Overview

Write batching collects multiple write operations together and executes them as
a single batch operation, significantly improving throughput while allowing
fine-grained control over latency trade-offs through batch hints.

### Key Benefits

- **Improved Throughput**: Batch multiple operations together to achieve 10-100x
  throughput improvements
- **Reduced Database Load**: Fewer transactions and fsync operations reduce I/O
  pressure
- **Fine-Grained Control**: Each query can specify its own batching window
  (0-1000ms)
- **Transaction Safety**: Respects transaction boundaries - batching only occurs
  outside transactions
- **Automatic Grouping**: Identical queries are batched together automatically

## How It Works

### 1. Batch Hint Syntax

Clients add a `batch:N` hint to SQL queries via comment, where N is the maximum
wait time in milliseconds:

```sql
/* batch:10 */ INSERT INTO logs (message, created_at) VALUES (?, ?)
/* batch:50 */ UPDATE cache SET value = ? WHERE key = ?
/* batch:5 */ DELETE FROM sessions WHERE expired_at < ?
```

**Parameters:**

- `batch:N` - Maximum batching window in milliseconds (0-1000)
  - `batch:0` - No batching, execute immediately
  - `batch:1` - Low latency batching (1ms window)
  - `batch:10` - Moderate batching (10ms window)
  - `batch:100` - High throughput batching (100ms window)

### 2. Batching Process

When a batchable write arrives:

1. **Parse Hint**: The parser extracts the `BatchMs` value from the SQL comment
2. **Check Batchability**: Query must be INSERT/UPDATE/DELETE with `batch:N > 0`
3. **Group Formation**: Query is added to a batch group based on its batch key
4. **Timer Management**:
   - First query in group starts a timer for the specified window
   - Additional queries join the existing batch
   - Batch executes when timer fires OR max batch size reached (1000 operations)
5. **Execution**: All operations in the batch are executed together
6. **Result Distribution**: Each operation receives its individual result

### 3. Batch Key Generation

Queries are grouped by their **batch key** for batching:

```go
// GetBatchKey returns the normalized query (without hints)
func (p *ParsedQuery) GetBatchKey() string {
    return p.Query  // Hints are already stripped
}
```

**Grouping Rules:**

- Identical queries batch together
- Different queries create separate batches
- Hints (ttl, file, line, batch) are stripped before comparison
- Parameter values are part of the key (different values = different batches)

**Examples:**

```sql
-- These batch together (identical after hint removal):
/* batch:10 file:app.go line:42 */ INSERT INTO users (name) VALUES ('alice')
/* batch:10 file:handler.go line:100 */ INSERT INTO users (name) VALUES ('alice')

-- These do NOT batch together (different values):
/* batch:10 */ INSERT INTO users (name) VALUES ('alice')
/* batch:10 */ INSERT INTO users (name) VALUES ('bob')
```

## Configuration

The write batch manager is configured in
[config.ini](../../configuration/README.md):

```ini
[writebatch]
max_batch_size = 1000  # Maximum operations per batch (default: 1000)
```

**Configuration Options:**

- `max_batch_size`: Maximum number of operations to batch together
  - Default: 1000
  - Range: 1-10000
  - When limit reached, batch executes immediately

## Usage Examples

### Basic INSERT Batching

```go
// Go client with 10ms batching window
db.Exec("/* batch:10 */ INSERT INTO logs (message) VALUES (?)", "Event occurred")
```

```php
// PHP client with 50ms batching window
$db->exec("/* batch:50 */ INSERT INTO events (type) VALUES (?)", [$type]);
```

```typescript
// TypeScript client with 25ms batching window
await db.query("/* batch:25 */ INSERT INTO metrics (value) VALUES ($1)", [
    value,
]);
```

### UPDATE Batching

```sql
-- Cache invalidation with 5ms batching
/* batch:5 */ UPDATE cache SET expired = TRUE WHERE key = 'user:123'
```

### DELETE Batching

```sql
-- Session cleanup with 100ms batching (low priority)
/* batch:100 */ DELETE FROM sessions WHERE last_seen < NOW() - INTERVAL '1 hour'
```

### Choosing Batch Windows

| Use Case            | Batch Hint  | Rationale                              |
| ------------------- | ----------- | -------------------------------------- |
| Real-time logging   | `batch:1`   | Low latency, some batching benefit     |
| Event tracking      | `batch:10`  | Balanced throughput/latency            |
| Analytics ingestion | `batch:50`  | High throughput, moderate latency      |
| Background cleanup  | `batch:100` | Maximum throughput, latency acceptable |
| Critical writes     | `batch:0`   | No batching, immediate execution       |

## Implementation Details

### Architecture

```
┌─────────────────┐
│  Client Query   │
│  /* batch:10 */ │
│  INSERT ...     │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│     Parser      │
│  Extract BatchMs│
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  IsBatchable()  │
│  Check Type &   │
│  BatchMs > 0    │
└────────┬────────┘
         │
         ▼
┌─────────────────────────┐
│   Write Batch Manager   │
│                         │
│  ┌─────────────────┐   │
│  │  Batch Groups   │   │
│  │  (by BatchKey)  │   │
│  └─────────────────┘   │
│                         │
│  • Group by query       │
│  • Start timer          │
│  • Collect requests     │
│  • Execute batch        │
└─────────┬───────────────┘
          │
          ▼
┌─────────────────────┐
│  Database Backend   │
│  • Execute batch    │
│  • Return results   │
└─────────────────────┘
```

### Key Components

#### Manager ([manager.go](../../../writebatch/manager.go))

The `Manager` coordinates all batching operations:

```go
type Manager struct {
    groups     sync.Map          // map[batchKey]*BatchGroup
    config     Config
    db         *sql.DB
    closed     atomic.Bool
    batchCount atomic.Int64
}
```

**Key Methods:**

- `Enqueue()`: Add a write operation to a batch queue
- `executeBatch()`: Execute a batch of writes
- `executeImmediate()`: Execute single operation without batching

#### BatchGroup ([types.go](../../../writebatch/types.go))

Groups requests with the same batch key:

```go
type BatchGroup struct {
    BatchKey  string
    Requests  []*WriteRequest
    FirstSeen time.Time
    mu        sync.Mutex
    timer     *time.Timer
}
```

#### WriteRequest ([types.go](../../../writebatch/types.go))

Represents a single write operation:

```go
type WriteRequest struct {
    Query           string
    Params          []interface{}
    ResultChan      chan WriteResult
    EnqueuedAt      time.Time
    OnBatchComplete func(batchSize int)
    HasReturning    bool
}
```

### Execution Strategies

The manager uses different execution strategies based on batch characteristics:

1. **Single Operation** (`batchSize == 1`):
   ```go
   m.executeSingle(requests[0])
   ```

2. **Batched Writes** (`batchSize > 1`):
   ```go
   m.executeBatchedWrites(requests)
   ```

The batched execution can use:

- **Multi-row INSERT**: Combines multiple INSERT operations
- **Prepared Statement Reuse**: Executes identical queries efficiently
- **Individual Execution**: Falls back for complex cases

### Transaction Handling

Batching is **disabled inside transactions**:

```go
// In mariadb.go
if c.proxy.writeBatch != nil && !c.inTransaction && parsed.IsWritable() && parsed.IsBatchable() {
    return c.handleBatchedWrite(...)
}
```

**Rationale:**

- Transactions require immediate execution for ACID guarantees
- Each statement must complete before the next begins
- Transaction isolation levels must be respected
- Batching occurs only in auto-commit mode

## Metrics

The write batching component exposes several Prometheus metrics:

### Batch Metrics

```go
// Number of operations in each batch
writebatch_batch_size{query="INSERT INTO..."}

// Time between first request and batch execution
writebatch_batch_delay_seconds{query="INSERT INTO..."}

// Batch execution time
writebatch_batch_latency_seconds{query="INSERT INTO..."}

// Total number of operations batched
writebatch_batched_total{type="INSERT"}
```

### Custom Metrics

View batch statistics via:

```sql
SHOW TQDB STATUS;
```

Returns metrics including:

- `writebatch.batches.total` - Total batches executed

## Performance Characteristics

### Throughput Improvements

Based on [benchmarks](../../../benchmarks/batching/README.md):

| Batch Hint  | Throughput Gain | Typical Latency |
| ----------- | --------------- | --------------- |
| `batch:0`   | 1x (baseline)   | ~0.5ms          |
| `batch:1`   | 5-10x           | ~1-2ms          |
| `batch:10`  | 50-100x         | ~10-15ms        |
| `batch:100` | 500-1000x       | ~100-150ms      |

### Latency Trade-offs

- **Batching adds latency**: Operations wait up to `BatchMs` milliseconds
- **Predictable delays**: Maximum delay is bounded by the hint value
- **No latency for first request**: Timer starts immediately
- **Amortized benefits**: Throughput gains often outweigh latency costs

### Resource Utilization

**Benefits:**

- Fewer database connections needed
- Reduced fsync operations (1 per batch vs. 1 per operation)
- Lower CPU overhead from protocol/parsing
- Better L1/L2 cache utilization

**Costs:**

- Memory for queued operations (bounded by `max_batch_size`)
- Timer management overhead
- Mutex contention for batch groups

## Best Practices

### 1. Choose Appropriate Batch Windows

```sql
-- ✅ Good: Low latency for critical updates
/* batch:1 */ UPDATE user_sessions SET last_active = NOW() WHERE id = ?

-- ✅ Good: High throughput for analytics
/* batch:50 */ INSERT INTO page_views (url, timestamp) VALUES (?, ?)

-- ❌ Bad: Too high for user-facing operations
/* batch:100 */ INSERT INTO user_actions (action) VALUES (?)

-- ❌ Bad: Batch hint on SELECT (ignored, no effect)
/* batch:10 */ SELECT * FROM users
```

### 2. Consistent Batch Keys

```sql
-- ✅ Good: Same query structure batches together
/* batch:10 */ INSERT INTO logs (level, message) VALUES (?, ?)
/* batch:10 */ INSERT INTO logs (level, message) VALUES (?, ?)

-- ❌ Bad: Different queries won't batch together
/* batch:10 */ INSERT INTO logs (level, message) VALUES (?, ?)
/* batch:10 */ INSERT INTO logs (message, level) VALUES (?, ?)
```

### 3. Avoid Batching in Transactions

```sql
-- ❌ Bad: Batching inside transaction (ignored)
BEGIN;
/* batch:10 */ INSERT INTO accounts (balance) VALUES (?);
/* batch:10 */ INSERT INTO accounts (balance) VALUES (?);
COMMIT;

-- ✅ Good: Batch outside transactions
/* batch:10 */ INSERT INTO accounts (balance) VALUES (?);
/* batch:10 */ INSERT INTO accounts (balance) VALUES (?);
```

### 4. Monitor Batch Performance

```go
// Check batch statistics
rows, _ := db.Query("SHOW TQDB STATUS")
for rows.Next() {
    var key, value string
    rows.Scan(&key, &value)
    if key == "writebatch.batches.total" {
        log.Printf("Total batches: %s", value)
    }
}
```

### 5. Handle RETURNING Clauses

```sql
-- Batching with RETURNING is supported
/* batch:10 */ INSERT INTO users (name) VALUES (?) RETURNING id
```

Note: Each operation receives its own RETURNING values even in a batch.

## Testing

The component includes comprehensive tests:

- [manager_test.go](../../../writebatch/manager_test.go): Manager functionality
- [mariadb_test.go](../../../mariadb/writebatch_test.go): MariaDB integration
- [postgres_test.go](../../../postgres/batchsize_test.go): PostgreSQL
  integration

Run tests:

```bash
# Test write batch manager
cd writebatch && go test -v

# Test MariaDB integration
cd mariadb && go test -v -run TestWriteBatch

# Test PostgreSQL integration  
cd postgres && go test -v -run TestBatchSize
```

## Benchmarks

Comprehensive benchmarking is available:

- [Direct Batching](../../../benchmarks/batching/README.md): Standalone
  performance
- [Proxy Batching](../../../benchmarks/proxybatch/README.md): End-to-end proxy
  performance

Run benchmarks:

```bash
cd benchmarks/batching
go run main.go
gnuplot plot_bars.gnu
```

## Limitations

### Current Limitations

1. **Query Grouping**: Only identical queries batch together
   - Different parameter values create separate batches
   - Query normalization could improve this

2. **No Cross-Database Batching**: Batches respect database boundaries
   - Operations to different databases don't batch together

3. **Transaction Exclusion**: No batching inside transactions
   - Required for ACID compliance
   - Could potentially batch independent transactions in the future

4. **Maximum Batch Size**: Hard limit of 1000 operations per batch
   - Configurable but bounded for safety
   - Prevents unbounded memory growth

### Future Enhancements

Potential improvements:

- **Query Normalization**: Batch queries with different parameter values
- **Adaptive Windows**: Automatically adjust batch windows based on load
- **Priority Queuing**: High-priority operations can skip batching
- **Batch Compression**: Compress large batches before sending to backend

## See Also

- [Parser Component](../parser/README.md) - Batch hint extraction
- [Configuration](../../configuration/README.md) - Batching configuration
- [Metrics](../metrics/README.md) - Batch performance metrics
- [Benchmarks](../../../benchmarks/batching/README.md) - Performance testing
