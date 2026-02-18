# Sub-Plan 8: Replace Adaptive System with Batching Hints

**Parent Story**: [WRITE_BATCHING.md](../WRITE_BATCHING.md)

**Status**: Not Started

**Estimated Effort**: 3-4 days

## Overview

Remove the adaptive delay system and replace it with explicit batching hints
















that allow developers to specify maximum wait times per query. This simplifies
the system while providing fine-grained control over write batching behavior.

## Prerequisites

- Parser extensions (Sub-Plan 1)

- Write batch manager core (Sub-Plan 2)


 
## Goals


 
- Remove adaptive delay logic and related code
- Add batching hint parsing from SQL comments

 
- Support `batch` hint to specify maximum batching delay in milliseconds
- Keep `max_batch_size` configuration parameter (default: 1000)

 
- Simplify configuration by removing adaptive-related parameters
- Maintain backward compatibility (queries without hints execute immediately)

 

## Tasks

 

### 1. Remove Adaptive Delay System

 
     

**Files to modify:**

 
     
- `writebatch/adaptive.go` - Delete entire file
- `writebatch/adaptive_test.go` - Delete entire file
- `writebatch/manager.go` - Remove throughput tracking and delay adjustment
  logic
     
- `writebatch/types.go` - Remove adaptive-related config fields

**Changes to `writebatch/types.go`:**

     
- [x] Remove `WriteThreshold`, `AdaptiveStep`, `MetricsInterval` from `Config`
- [x] Remove `InitialDelayMs`, `MaxDelayMs`, `MinDelayMs` fields
- [x] Add `DefaultMaxBatchSize` constant (1000)
- [x] Simplify `Config` struct
     

```go
type Config struct {
    MaxBatchSize int // Maximum number of operations in a batch (defau
     lt: 1000)
}

func DefaultConfig() Config {
    return Config{
     
        MaxBatchSize: 1000,
    }
}
```
     

**Changes to `writebatch/manager.go`:**

- [x] Remove throughput tracking fields: `opsPerSecond`, `opsCounter`,
      `lastReset`
- [x] Remove `currentDelay` atomic field

- [x] Remove `updateThroughput()` method
- [x] Remove `adjustDelay()` method
- [x] Remove goroutine that periodically adjusts delay
- [x] Simplify `Manager` struct


```go
type Manager struct {
    groups       sync.Map
    config       Config
    db           *sql.DB

    closed       atomic.Bool
}
```

### 2. Extend Parser for Batching Hints


**File**: `parser/parser.go`

- [x] Add `BatchMs` field to `ParsedQuery` struct
- [x] Parse `batch` hint from SQL comments

- [x] Default to 0 (no batching) if hint not present

```go
type ParsedQuery struct {
    Query      string

    Type       QueryType
    Tables     []string
    File       string
    Line       int
    BatchMs    int    // Maximum wait time for batching in ms (0 = no batching)

    
    // ... existing fields
}

// Example hint parsing in Parse() function:

// /* batch:10 */ INSERT INTO users VALUES (...)
// Extract batch value and set p.BatchMs = 10
```

**Hint format examples:**

```sql
/* batch:5 */ INSERT INTO logs (message) VALUES ('test');
/* batch:50 */ UPDATE users SET last_seen = NOW();
/* file:app.go line:42 batch:10 */ DELETE FROM cache WHERE expired = 1;
```

### 3. Implement Hint-Based Batching

**File**: `writebatch/manager.go`

- [x] Update `Enqueue()` to accept `batchMs` parameter
- [x] Use hint value to set batch timer duration
- [x] Execute immediately if `batchMs` is 0
- [x] Respect `MaxBatchSize` config when accumulating requests

```go
func (m *Manager) Enqueue(batchKey string, query string, params []interface{}, batchMs int) WriteResult {
    // If no wait time specified, execute immediately (no batching)
    if batchMs == 0 {
        return m.executeImmediate(query, params)
    }
    
    req := &WriteRequest{
        Query:      query,
        Params:     params,
        ResultChan: make(chan WriteResult, 1),
        EnqueuedAt: time.Now(),
    }
    
    // Get or create batch group
    groupInterface, _ := m.groups.LoadOrStore(batchKey, &BatchGroup{
        BatchKey:  batchKey,
        Requests:  make([]*WriteRequest, 0, m.config.MaxBatchSize),
        FirstSeen: time.Now(),
    })
    
    group := groupInterface.(*BatchGroup)
    group.mu.Lock()
    
    // Add request to group
    group.Requests = append(group.Requests, req)
    
    // If this is the first request, set a timer
    if len(group.Requests) == 1 {
        delay := time.Duration(batchMs) * time.Millisecond
        group.timer = time.AfterFunc(delay, func() {
            m.executeBatch(batchKey, group)
        })
    }

    
    // If batch is full, execute immediately
    if len(group.Requests) >= m.config.MaxBatchSize {
        if group.timer != nil {
            group.timer.Stop()
        }

        group.mu.Unlock()
        m.executeBatch(batchKey, group)
        return <-req.ResultChan
    }
    

    group.mu.Unlock()

 
    
    // Wait for result
    return <-req.ResultChan
}


func (m *Manager) executeImmediate(query string, params []interface{}) WriteResult {

 
    result, err := m.db.Exec(query, params...)
    if err != nil {
        return WriteResult{Error: err}
    }
    

    affected, _ := result.RowsAffected()

 
    lastID, _ := result.LastInsertId()
    
    return WriteResult{
        AffectedRows: affected,
        LastInsertID: lastID,

    }

 
}
```

### 4. Update Configuration


**File**: `config/config.go`

 

- [x] Remove adaptive-related INI parameters
- [x] Keep only `write_batch_max_size` parameter
- [x] Update `LoadConfig()` to parse simplified config


```ini

 
[writebatch]
# Maximum number of operations in a single batch (default: 1000)
write_batch_max_size = 1000
```


**Example `config.ini`:**

 
```ini
[proxy]
listen_addr = :5432
database_url = postgres://localhost/mydb


[writebatch]
write_batch_max_size = 1000
 
```

### 5. Update Tests

**Files:**

- `parser/parser_test.go` - Add tests for `BatchMs` hint parsing
- `writebatch/manager_test.go` - Update tests to use hints instead of adaptive
  delays

**New tests for `parser/parser_test.go`:**

```go
func TestParsedQuery_BatchMs(t *testing.T) {
    tests := []struct {
        query       string
        expectedMs  int
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
        query       string
        expectedMs  int
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
            100, // Cap at max (100ms or configurable limit)
        },
    }
    

    for _, tt := range tests {
        t.Run(tt.query, func(t *testing.T) {
            p := Parse(tt.query)
            if p.BatchMs != tt.expectedMs {
                t.Errorf("BatchMs = %v, want %v", p.BatchMs, tt.expectedMs)
            }
`        })
    }
}

```

**Update tests in `writebatch/manager_test.go`:**

```go
fun
````c TestManager_HintBasedBatching(t *testing.T) {
`    db := setupTestDB(t)
    defer db.Close()   
 

    mgr := New(db, DefaultConfig())
    defer mgr.Close()
    
    query := "INSERT INTO test_table (value) VALUES (?)"
    
   
```` // Start multiple writes with 50ms max wait
`    var wg sync.WaitGroup
    results := make([]WriteResult, 10)   
 

    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            results[idx] = mgr.Enqueue("test:1", query, []interface{}{idx}, 50)
   
````     }(i)
`    }
       
 wg.Wait()
     


   `    for i, res := range results {
        if res.Error != nil {
            t.Errorf("Request %d failed: %v", i, res.Error)
   
````     }
`    }
} 

 func Te
tManager_NoHintNobatching(t *testing.T) {

   `    defer db.Close()
    
    mgr := New(db, DefaultConfig())
    defer mgr.Close()
    
    query := "INSERT INTO test_table (value) VALUES (?)"
    
    start := time.Now()
    result := mgr.Enqueue("test:1", query, []interface{}{1}, 0) // batchMs = 0
    elapsed := time.Since(start)
    
    if result.Error != nil {
        t.Fatalf("Insert failed: %v", result.Error)
    }
    
    // Should execute immediately (< 5ms)
    if elapsed > 5*time.Millisecond {
        t.Errorf("Took too long: %v", elapsed)
   ` }
}
```

```

### 6. Update Documentation

**Files to update:**

- `README.md` - Document batching hint syntax
- ``docs/stories/WRITE_BATCHING.md` - Update with new approach
- `docs/stories/write-batching/README.md` - Update sub-plan list

**Documentation examples:**

```markdown
## Write Batching with Hints

TQDBProxy supports write batching through explicit hints in SQL comments:

### Basic Usage

```sql
/* batch:10 */ INSERT INTO logs (message) VALUES ('event');
```
`
This tells the proxy to wait up to 10ms to batch multiple INSERT operations to
the same location (file:line).

### Combining with File/Line Hints

```sql
/* file:app.go line:42 batch:5 */ 
INSERT INTO users (name) VALUES ('john');

```

###` Choosing Wait Times


- `0ms` - No batching (immediate execution)
- `1-5ms` - Low latency batching for high-frequency writes
- `10-50ms` - Balanced batching for moderate write loads
- `50-100ms` - Maximum batching for bulk operations


### Configuration

```ini

[writebatch]
write_batch_max_size = 1000
```


````
The `write_batch_max_size` parameter limits how many operations can be batched
together, regardless of wait time.
```



`
### 7. Update Benchmark Suite

**Rename and update benchmark:**



- [x] Rename `benchmarks/adaptive/` to `benchmarks/batching/`
- [x] Update benchmark to test hint-based batching

- [`
````
- [x] Configure batching scenarios for 10k, 100k, and 1M TPS with appropriate wait times


**File**: `benchmarks/batching/main.go`

`
Update benchmark to test different `batch` values:


```go
// 

````Benchmark configurations
` benchmarks = []struct {


   `    targetTPS  int

    batchMs    int

    duration   time.Duration
}{
   ` {"baseline-1k", 1000, 0, 30 * time.Second},        // Baseline: no batching
    {"batching-10k", 10000, 1, 30 * time.Second},      // 1ms wait for 10k TPS


   
```` {"batching-100k", 100000, 10, 30 * time.Second},   // 10ms wait for 100k TPS

   `}
```
`


**File**: `benchmarks/batching/plot_bars.gnu`


Update gnuplot script to show baseline and batching bars:


```gnuplot
set title "Write Batching Performance Comparison"


`````` xlabel "Throughput Target"
set ylabel "Actual Throughput (TPS)"

set style data histogram


set style histogram cluster gap 1
set style fill solid border -1
set boxwidth 0.9

set xtics ("1k (baseline)" 0, "10k" 1, "100k" 2, "1M" 3)
                          | Mitigation                                     
 ------------------------ | ----------------------------------- 
plot 'batching_data.csv' using 2:xtic(1) title "Throughput" linecolor              rgb "#27ae60"
```               
`                    
          
**F`
Update documentation:

```markdown

# Write Batching Benchmark

This benchmark tests the performance of hint-based write batching across
different throughput levels.

## Test Scenarios

                          | Mitigation                                     
# ## 1k ------------------------ | ----------------------------------PS (Baseline- )
- **Configuration**: No batch hint (immediate execution)             
- **Purpose**: Establish ba      seline performance without batching         
                    
### 10k TPS          
- **Configuration**: `batch:1` (1ms batching window)
- **Purpose**: Low latency batching for high-frequency writes

### 100k TPS
- **Configuration**: `batch:10` (10ms batching window)
- **Purpose**: Balanced batching for moderate write loads

### 1M                           | Mitigation                                     
-  **Con------------------------ | ----------------------------------iguration**:-  `batch:100` (100ms batching window)
- **Purpose**: Maximum batching for extreme write loads             
               
## Running the Benchmar          k          
          
```bash
cd benchmarks/batching
go build
./batching
```

## Expected Results

The benchmark measures:
- Throughput (operations/second)
- Latency (p50, p95, p99)
- Batch size distribution
- Aggregation factor (speedup)

Batching should show:
- Higher throughput for batchable operations
- Controlled latency based on hint values
- Batch sizes that match the wait window
- Baseline (1k) shows performance without batching overhead
```

## Deliverables

- [x] Remove all adaptive delay code
- [x] Implement `batch` hint parsing in parser
- [x] Update write batch manager to use hints
- [x] Simplify configuration (single parameter)
- [x] Update all tests
- [x] Update documentation
- [x] Rename `benchmarks/adaptive/` to `benchmarks/batching/`
- [x] Add baseline scenario at 1k TPS (no batching)
- [x] Add batching scenarios at 10k, 100k, 1M TPS
- [x] Configure latency values: 1ms (10k), 10ms (100k), 100ms (1M)

## Validation Steps

1. **Parser Tests**: All hint parsing tests pass
   ```bash
   go test ./parser -v
   ```

2. **Manager Tests**: Hint-based batching works correctly
   ```bash
   go test ./writebatch -v
   ```

3. **Integration Test**: Test with real queries
   ```bash
   # Write test queries with various batch values
   # Verify batching behavior matches hints
   ```

4. **Backward Compatibility**: Queries without hints work (no batching)
   ```bash
   # Send queries without batch hint
   # Verify immediate execution
   ```

5. **Benchmark Suite**: Verify baseline and batching performance
   ```bash
   cd benchmarks/batching
   go build && ./batching
   # Should show baseline at 1k TPS (no hint)
   # Batching at 10k (1ms), 100k (10ms), 1M (100ms)
   ```

## Migration Notes

For users upgrading from the adaptive system:

1. **Configuration Changes:**
   - Remove: `write_batch_initial_delay_ms`, `write_batch_max_delay_ms`, 
     `write_batch_min_delay_ms`, `write_batch_threshold`, 
     `write_batch_adaptive_step`, `write_batch_metrics_interval`
   - Keep: `write_batch_max_size`

2. **Client Changes:**
   - Add `batch` hints to queries that should be batched
   - Start with conservative values (5-10ms)
   - Monitor metrics to tune values

3. **Benefits:**
   - Explicit control over batching behavior
   - Simpler configuration
   - Predictable latency
   - No hidden adaptive behavior

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Developers forget to add hints | Document clearly, provide examples |
| Hints parsed incorrectly | Comprehensive parser tests, validation |
| Too large wait times | Document best practices, add warnings |
| Migration complexity | Provide migration guide, backward compatibility |

## Success Criteria

- [x] All adaptive delay code removed
- [x] Hint parsing implemented and tested
- [x] Configuration simplified
- [x] Tests updated and passing
- [x] Documentation complete
- [x] Backward compatible (queries without hints work)