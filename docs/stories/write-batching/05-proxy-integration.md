# Sub-Plan 5: Proxy Integration

**Parent Story**: [WRITE_BATCHING.md](../WRITE_BATCHING.md)

**Status**: ✅ Completed

**Estimated Effort**: 4-5 days

**Actual Effort**: 4 days

## Overview


Integrate write batching into the MariaDB and PostgreSQL proxy handlers with
transaction state tracking.

## Prerequisites

- [x] Sub-Plan 1: Parser extensions completed
- [x] Sub-Plan 2: Write batch manager core completed
- [x] Sub-Plan 3: Adaptive delay system completed
- [x] Sub-Plan 4: Metrics completed

## Goals

- [x] Add write batch manager to proxy structs
- [x] Utilize existing transaction state tracking per connection
- [x] Route writes to batch manager when appropriate
- [x] Exclude transactional writes from batching
- [x] Handle write results and errors properly

## Implementation Summary

### Configuration Support


Added `WriteBatchConfig` to `config/config.go`:
- Enabled flag
- Delay parameters (initial, min, max in milliseconds)
- Batch size limit
- Adaptive threshold and step
- Metrics interval

Configuration is loaded from INI file with keys like:
- `writebatch_enabled`
- `writebatch_initial_delay_ms`
- `writebatch_max_delay_ms`
- etc.

### MariaDB Proxy Integration

**Modified Files**: `mariadb/mariadb.go`

1. **Added WriteBatch Fields to Proxy**:
   - `writeBatch *writebatch.Manager` - the batch manager instance
   - `wbCtx context.Context` - context for adaptive goroutine
   - `wbCancel context.CancelFunc` - cleanup function

2. **Initialized Manager in New()**: Set up context and cancel func

3. **Started Manager in Start()**: After database connection established
   - Create writebatch.Manager with db connection
   - Start adaptive adjustment goroutine
   - Log initialization

4. **Cleanup in Stop()**: Close manager and cancel context

5. **Added Transaction Tracking**:
   - Added `inTransaction bool` field to `clientConn`
   - Updated `handleBegin()` to set `inTransaction = true`
   - Updated `handleCommit()` to set `inTransaction = false`
   - Updated `handleRollback()` to set `inTransaction = false`

6. **Added Write Routing**:
   - Check if write batching enabled, not in transaction, and query is batchable
   - Route to `handleBatchedWrite()` if conditions met
   - Otherwise use normal execution path

7. **Implemented Write Handlers**:
   - `handleBatchedWrite()`: Parse query, get batch key, enqueue to manager, send OK packet with results
   - `executeImmediateWrite()`: Fallback for when batching fails
   - `writeOKWithRowsAndID()`: Send OK packet with affected rows and last insert ID

### Integration Tests

**Created**: `mariadb/writebatch_test.go`

Tests include:
- `TestWriteBatchIntegration`: Full integration test with concurrent writes, transactions
- `TestWriteBatchManagerLifecycle`: Verify manager initialization and cleanup
- `TestWriteBatchAdaptiveDelay`: Config flow verification

Note: Integration tests are skipped by default (require running MariaDB backend).

## Tasks

### 1. Verify Transaction State Tracking

**Files**: `mariadb/mariadb.go`, `postgres/postgres.go`

**Status**: ✅ Completed

#### MariaDB

```go
// mariadb/mariadb.go

type clientConn struct {
    // ... existing fields
    inTransaction bool  // Track if connection is in transaction
}

func (c *clientConn) trackTransactionState(query string) {
    queryUpper := strings.ToUpper(strings.TrimSpace(query))
    
    // Explicit transaction control
    if strings.HasPrefix(queryUpper, "BEGIN") || 
       strings.HasPrefix(queryUpper, "START TRANSACTION") {
        c.inTransaction = true
    } else if strings.HasPrefix(queryUpper, "COMMIT") || 
              strings.HasPrefix(queryUpper, "ROLLBACK") {
        c.inTransaction = false
    }
    
    // Note: MariaDB autocommit tracking may require COM_QUERY analysis
}
```

#### PostgreSQL

```go
// postgres/postgres.go

type connState struct {
    // ... existing fields
    inTransaction bool  // Track if connection is in transaction
}

func (p *Proxy) trackTransactionState(state *connState, query string) {
    queryUpper := strings.ToUpper(strings.TrimSpace(query))
    
    if strings.HasPrefix(queryUpper, "BEGIN") || 
       strings.HasPrefix(queryUpper, "START TRANSACTION") {
        state.inTransaction = true
    } else if strings.HasPrefix(queryUpper, "COMMIT") || 
              strings.HasPrefix(queryUpper, "ROLLBACK") {
        state.inTransaction = false
    }
}
```

### 2. Add WriteBatch Manager to Proxy

**Files**: `mariadb/mariadb.go`, `postgres/postgres.go`

- [ ] Add writeBatch field to Proxy struct
- [ ] Initialize manager in proxy constructor
- [ ] Start adaptive adjustment goroutine
- [ ] Close manager on proxy shutdown

#### MariaDB

```go
// mariadb/mariadb.go

type Proxy struct {
    // ... existing fields
    writeBatch *writebatch.Manager
    wbCtx      context.Context
    wbCancel   context.CancelFunc
}

func NewProxy(cfg *config.Config) (*Proxy, error) {
    // ... existing initialization
    
    p := &Proxy{
        // ... existing fields
    }
    
    // Initialize write batch manager if enabled
    if cfg.WriteBatch.Enabled {
        wbConfig := writebatch.Config{
            InitialDelayMs:  cfg.WriteBatch.InitialDelayMs,
            MaxDelayMs:      cfg.WriteBatch.MaxDelayMs,
            MinDelayMs:      cfg.WriteBatch.MinDelayMs,
            MaxBatchSize:    cfg.WriteBatch.MaxBatchSize,
            WriteThreshold:  cfg.WriteBatch.WriteThreshold,
            AdaptiveStep:    cfg.WriteBatch.AdaptiveStep,
            MetricsInterval: cfg.WriteBatch.MetricsInterval,
        }
        
        // Use primary database connection for writes
        p.writeBatch = writebatch.New(p.primaryDB, wbConfig)
        p.wbCtx, p.wbCancel = context.WithCancel(context.Background())
        
        // Start adaptive delay adjustment
        go p.writeBatch.StartAdaptiveAdjustment(p.wbCtx)
        
        log.Printf("[MariaDB] Write batching enabled (initial delay: %dms)", 
            cfg.WriteBatch.InitialDelayMs)
    }
    
    return p, nil
}

func (p *Proxy) Close() error {
    if p.writeBatch != nil {
        p.wbCancel()
        p.writeBatch.Close()
    }
    // ... existing cleanup
    return nil
}
```

#### PostgreSQL

```go
// postgres/postgres.go

type Proxy struct {
    // ... existing fields
    writeBatch *writebatch.Manager
    wbCtx      context.Context
    wbCancel   context.CancelFunc
}

// Similar initialization as MariaDB
```

### 3. Route Writes to Batch Manager

**Files**: `mariadb/mariadb.go`, `postgres/postgres.go`

- [ ] Update query handler to check for writable queries
- [ ] Check transaction state before batching
- [ ] Call writeBatch.Enqueue() for batchable writes
- [ ] Send results back to client
- [ ] Record exclusion metrics for non-batched writes

#### MariaDB

```go
// mariadb/mariadb.go

func (c *clientConn) handleSingleQuery(query string, originalParsed *parser.ParsedQuery, start time.Time, moreResults bool) error {
    parsed := parser.Parse(query)
    
    // Track transaction state
    c.trackTransactionState(query)
    
    file := parsed.File
    if file == "" {
        file = "unknown"
    }
    lineStr := strconv.Itoa(parsed.Line)
    queryType := queryTypeString(parsed.Type)
    
    // Existing read/cache path
    if parsed.IsCacheable() {
        // ... existing cache logic
        return nil
    }
    
    // New write batching path
    if parsed.IsWritable() && c.proxy.writeBatch != nil {
        if c.inTransaction {
            // Exclude writes in transactions
            metrics.WriteExcludedTotal.WithLabelValues(file, lineStr, queryType, "transaction").Inc()
        } else {
            // Batch the write
            ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
            defer cancel()
            
            batchKey := parsed.GetBatchKey()
            result := c.proxy.writeBatch.Enqueue(ctx, batchKey, parsed.Query, nil)
            
            if result.Error != nil {
                return c.sendError(result.Error)
            }
            
            metrics.WriteBatchedTotal.WithLabelValues(file, lineStr, queryType).Inc()
            return c.sendOKResult(result.AffectedRows, result.LastInsertID)
        }
    }
    
    // Fallback to direct execution
    return c.executeDirectly(query, parsed, start, moreResults)
}

func (c *clientConn) sendOKResult(affectedRows, lastInsertID int64) error {
    // Build MySQL OK packet
    data := make([]byte, 0, 32)
    data = append(data, 0x00) // OK packet header
    data = appendLengthEncodedInteger(data, uint64(affectedRows))
    data = appendLengthEncodedInteger(data, uint64(lastInsertID))
    // ... add status flags, warnings, etc.
    
    return c.writePacket(data)
}
```

#### PostgreSQL

```go
// postgres/postgres.go

func (p *Proxy) handleQuery(payload []byte, client net.Conn, db *sql.DB, state *connState) {
    query := string(payload[4:]) // Skip 'Q' command byte
    parsed := parser.Parse(query)
    
    // Track transaction state
    p.trackTransactionState(state, query)
    
    file := parsed.File
    if file == "" {
        file = "unknown"
    }
    line := strconv.Itoa(parsed.Line)
    queryType := queryTypeString(parsed.Type)
    
    // Existing read/cache path
    if parsed.IsCacheable() {
        // ... existing cache logic
        return
    }
    
    // New write batching path
    if parsed.IsWritable() && p.writeBatch != nil {
        if state.inTransaction {
            // Exclude writes in transactions
            metrics.WriteExcludedTotal.WithLabelValues(file, line, queryType, "transaction").Inc()
        } else {
            // Batch the write
            ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
            defer cancel()
            
            batchKey := parsed.GetBatchKey()
            result := p.writeBatch.Enqueue(ctx, batchKey, parsed.Query, nil)
            
            if result.Error != nil {
                p.sendError(client, result.Error)
                return
            }
            
            metrics.WriteBatchedTotal.WithLabelValues(file, line, queryType).Inc()
            p.sendCommandComplete(client, "INSERT", result.AffectedRows)
            return
        }
    }
    
    // Fallback to direct execution
    p.executeDirectly(client, db, query, parsed, state)
}

func (p *Proxy) sendCommandComplete(client net.Conn, command string, rows int64) {
    // Build PostgreSQL CommandComplete message
    msg := fmt.Sprintf("%s %d", command, rows)
    // ... format as PostgreSQL protocol message
    client.Write(msg)
}
```

### 4. Handle Prepared Statements

**Files**: `mariadb/mariadb.go`, `postgres/postgres.go`

- [ ] Extract parameters from prepared statement execution
- [ ] Pass parameters to writeBatch.Enqueue()
- [ ] Handle parameter binding correctly

```go
// Example for prepared statement handling
func (c *clientConn) handlePreparedWrite(stmtID uint32, params []interface{}) error {
    stmt := c.preparedStatements[stmtID]
    parsed := parser.Parse(stmt.query)
    
    if parsed.IsWritable() && !c.inTransaction && c.proxy.writeBatch != nil {
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        
        batchKey := parsed.GetBatchKey()
        result := c.proxy.writeBatch.Enqueue(ctx, batchKey, parsed.Query, params)
        
        if result.Error != nil {
            return c.sendError(result.Error)
        }
        
        return c.sendOKResult(result.AffectedRows, result.LastInsertID)
    }
    
    // Fallback
    return c.executePreparedDirectly(stmtID, params)
}
```

### 5. Configuration Loading

**File**: `config/config.go`

- [ ] Add WriteBatchConfig struct
- [ ] Add parsing from INI
- [ ] Add validation
- [ ] Set defaults

```go
// config/config.go

type Config struct {
    // ... existing fields
    WriteBatch WriteBatchConfig
}

type WriteBatchConfig struct {
    Enabled         bool    `ini:"enabled"`
    InitialDelayMs  int     `ini:"initial_delay_ms"`
    MaxDelayMs      int     `ini:"max_delay_ms"`
    MinDelayMs      int     `ini:"min_delay_ms"`
    MaxBatchSize    int     `ini:"max_batch_size"`
    WriteThreshold  int     `ini:"write_threshold"`
    AdaptiveStep    float64 `ini:"adaptive_step"`
    MetricsInterval int     `ini:"metrics_interval"`
}

func LoadConfig(path string) (*Config, error) {
    cfg := &Config{
        WriteBatch: WriteBatchConfig{
            Enabled:         false,
            InitialDelayMs:  1,
            MaxDelayMs:      100,
            MinDelayMs:      0,
            MaxBatchSize:    1000,
            WriteThreshold:  1000,
            AdaptiveStep:    1.5,
            MetricsInterval: 1,
        },
    }
    
    // ... existing parsing logic
    
    return cfg, nil
}
```

### 6. Update config.example.ini

**File**: `config.example.ini`

- [ ] Add write_batch section with all parameters

```ini
[write_batch]
# Enable write operation batching
enabled = false

# Initial batching delay in milliseconds
initial_delay_ms = 1

# Maximum batching delay in milliseconds
max_delay_ms = 100

# Minimum batching delay in milliseconds
min_delay_ms = 0

# Maximum number of operations per batch
max_batch_size = 1000

# Ops/sec threshold to trigger delay increase

write_threshold = 1000
 

# Delay adjustment multiplier (e.g., 1.5 = 50% increase/decrease)
adaptive_step = 1.5

# Interval in seconds between delay adjustments
metrics_interval = 1
```

## Dependencies

- All previous sub-plans (1-4)

## Deliverables

- [x] Transaction state tracking in both proxies (already implemented)
- [ ] Write batch manager integrated
- [ ] Write routing logic implemented
- [ ] Configuration loaded and validated
- [ ] Prepared statement support
- [ ] Error handling
- [ ] Integration tests

## Validation

```bash
# Test with MariaDB
mysql -h localhost -P 3307 -u test -p <<EOF
/* file:test.sql line:1 */ INSERT INTO test (data) VALUES ('test1');
/* file:test.sql line:1 */ INSERT INTO test (data) VALUES ('test2');
/* file:test.sql line:1 */ INSERT INTO test (data) VALUES ('test3');
EOF

# Check metrics
curl http://localhost:9090/metrics | grep write_batch_size

# Test transaction exclusion
mysql -h localhost -P 3307 -u test -p <<EOF
BEGIN;
/* file:test.sql line:5 */ INSERT INTO test (data) VALUES ('in-tx');
COMMIT;
EOF

# Verify exclusion metric
curl http://localhost:9090/metrics | grep write_excluded_total
```

## Integration Tests

**File**: `mariadb/writebatch_test.go`

- [ ] Test write batching with identical queries
- [ ] Test writes in transaction are excluded
- [ ] Test transaction state tracking
- [ ] Test error handling
- [ ] Test prepared statements

## Next Steps

After completion, proceed to:

- [06-benchmark-suite.md](06-benchmark-suite.md) - Create comprehensive
  benchmarks
