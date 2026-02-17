# Story: Write Operation Batching and Optimization

## Overview

Implement a comprehensive write batching system for INSERT, UPDATE, and DELETE
operations that mirrors the read optimization pattern currently used for SELECT
queries. This feature will:

1. Identify and track write operations by source file and line number
2. Group write operations by their origin (file:line)
3. Delay non-first operations (batching window)
4. Execute writes as batched operations
5. Split results and return to individual callers
6. Implement adaptive delay based on write throughput
7. Provide metrics for batch sizes and write performance

## Background

Currently, TQDBProxy implements sophisticated read optimization:

- Read queries (SELECT) are identified via parser
- Grouped by cache key (query + parameters)
- Cold cache uses single-flight pattern (GetOrWait)
- Warm cache serves stale while refreshing
- File and line tracking for metrics

Write operations currently go directly to the database without batching or
optimization.

## Goals

1. **Reduce Database Load**: Batch multiple write operations to reduce
   connection overhead
2. **Improve Throughput**: Execute writes in groups to leverage database batch
   processing
3. **Maintain Correctness**: Ensure all writes are executed and results properly
   returned
4. **Adaptive Performance**: Automatically adjust batching behavior based on
   write load
5. **Observable**: Provide metrics on batch sizes, delays, and throughput

## Non-Goals

- Transaction management (each write is still independent)
- Batching writes that are already within a user transaction
- Write ordering guarantees across different source locations
- Cross-database write coordination
- Write-after-read consistency (handled separately)

## User Stories

### As a Developer

- I want my write operations automatically batched when possible
- I want to receive individual results for each write operation
- I want visibility into write performance via metrics

### As a DBA

- I want to reduce the number of individual write operations hitting the
  database
- I want configurable batching parameters to tune for our workload
- I want metrics showing batch effectiveness

## Technical Design

### 1. Write Operation Identification

Extend the parser to track write operations:

```go
// parser/parser.go

func (p *ParsedQuery) IsWritable() bool {
    return p.Type == QueryInsert || p.Type == QueryUpdate || p.Type == QueryDelete
}

func (p *ParsedQuery) IsBatchable() bool {
    // Only batch writes outside of transactions
    return p.IsWritable()
}

func (p *ParsedQuery) GetBatchKey() string {
    // Group by file:line for batching
    return fmt.Sprintf("%s:%d", p.File, p.Line)
}
```

### 2. Write Batch Manager

Create a new component `writebatch/` to manage write batching:

```go
// writebatch/manager.go

type WriteRequest struct {
    Query      string
    Params     []interface{}
    ResultChan chan WriteResult
    EnqueuedAt time.Time
}

type WriteResult struct {
    AffectedRows int64
    LastInsertID int64
    Error        error
}

type BatchGroup struct {
    BatchKey   string              // file:line identifier
    Requests   []*WriteRequest
    FirstSeen  time.Time
    mu         sync.Mutex
    timer      *time.Timer
}

type Manager struct {
    groups       sync.Map            // map[batchKey]*BatchGroup
    config       Config
    db           *sql.DB
    metrics      *BatchMetrics
    
    // Adaptive delay management
    opsPerSecond atomic.Uint64
    currentDelay atomic.Int64       // in microseconds
}

type Config struct {
    InitialDelayMs    int     // Initial batching delay (default: 1ms)
    MaxDelayMs        int     // Maximum batching delay (default: 100ms)
    MinDelayMs        int     // Minimum batching delay (default: 0.1ms)
    MaxBatchSize      int     // Maximum operations in one batch (default: 1000)
    WriteThreshold    int     // ops/sec threshold to increase delay (default: 1000)
    AdaptiveStep      float64 // Delay adjustment step (default: 1.5x)
    MetricsInterval   int     // How often to adjust delay (default: 1s)
}
```

### 3. Batching Logic

```go
// writebatch/manager.go

func (m *Manager) Enqueue(ctx context.Context, parsed *parser.ParsedQuery, params []interface{}) WriteResult {
    batchKey := parsed.GetBatchKey()
    
    req := &WriteRequest{
        Query:      parsed.Query,
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
    isFirst := len(group.Requests) == 0
    group.Requests = append(group.Requests, req)
    currentSize := len(group.Requests)
    group.mu.Unlock()
    
    if isFirst {
        // First request in batch - start timer
        delay := time.Duration(m.currentDelay.Load()) * time.Microsecond
        group.timer = time.AfterFunc(delay, func() {
            m.executeBatch(batchKey, group)
        })
    } else if currentSize >= m.config.MaxBatchSize {
        // Batch is full - execute immediately
        group.timer.Stop()
        m.executeBatch(batchKey, group)
    }
    
    // Wait for result (with timeout)
    select {
    case result := <-req.ResultChan:
        return result
    case <-ctx.Done():
        return WriteResult{Error: ctx.Err()}
    case <-time.After(30 * time.Second):
        return WriteResult{Error: fmt.Errorf("batch execution timeout")}
    }
}

func (m *Manager) executeBatch(batchKey string, group *BatchGroup) {
    group.mu.Lock()
    requests := group.Requests
    group.Requests = make([]*WriteRequest, 0, m.config.MaxBatchSize)
    firstSeen := group.FirstSeen
    group.FirstSeen = time.Now()
    group.mu.Unlock()
    
    if len(requests) == 0 {
        return
    }
    
    batchStart := time.Now()
    batchSize := len(requests)
    
    // Record metrics
    m.metrics.BatchSize.WithLabelValues(batchKey).Observe(float64(batchSize))
    m.metrics.BatchDelay.WithLabelValues(batchKey).Observe(time.Since(firstSeen).Seconds())
    
    // Execute writes in batch
    // Strategy depends on write type:
    // - All same INSERT: Use multi-value INSERT or batch insert
    // - All same UPDATE: Execute sequentially but in single transaction
    // - Mixed: Execute sequentially in transaction
    
    if batchSize == 1 {
        // Single operation - execute directly
        m.executeSingle(requests[0])
    } else {
        // Multiple operations - try to batch
        m.executeBatchedWrites(requests, batchKey)
    }
    
    duration := time.Since(batchStart)
    m.metrics.BatchLatency.WithLabelValues(batchKey).Observe(duration.Seconds())
    
    // Update throughput tracking
    m.updateThroughput(batchSize, duration)
}

func (m *Manager) executeSingle(req *WriteRequest) {
    result := m.executeWrite(req.Query, req.Params)
    req.ResultChan <- result
}

func (m *Manager) executeBatchedWrites(requests []*WriteRequest, batchKey string) {
    // Check if all queries are identical (can use prepared statement)
    allSame := true
    firstQuery := requests[0].Query
    for _, req := range requests[1:] {
        if req.Query != firstQuery {
            allSame = false
            break
        }
    }
    
    if allSame {
        // Use prepared statement for efficiency
        m.executePreparedBatch(requests)
    } else {
        // Execute in transaction
        m.executeTransactionBatch(requests)
    }
}

func (m *Manager) executePreparedBatch(requests []*WriteRequest) {
    stmt, err := m.db.Prepare(requests[0].Query)
    if err != nil {
        // Distribute error to all requests
        for _, req := range requests {
            req.ResultChan <- WriteResult{Error: err}
        }
        return
    }
    defer stmt.Close()
    
    for _, req := range requests {
        result := m.executeWriteWithStmt(stmt, req.Params)
        req.ResultChan <- result
    }
}

func (m *Manager) executeTransactionBatch(requests []*WriteRequest) {
    tx, err := m.db.Begin()
    if err != nil {
        for _, req := range requests {
            req.ResultChan <- WriteResult{Error: err}
        }
        return
    }
    
    for _, req := range requests {
        result := m.executeWriteInTx(tx, req.Query, req.Params)
        req.ResultChan <- result
        if result.Error != nil {
            tx.Rollback()
            // Distribute rollback error to remaining requests
            return
        }
    }
    
    if err := tx.Commit(); err != nil {
        for _, req := range requests {
            req.ResultChan <- WriteResult{Error: fmt.Errorf("commit failed: %w", err)}
        }
    }
}

func (m *Manager) executeWrite(query string, params []interface{}) WriteResult {
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

### 4. Adaptive Delay Management

```go
// writebatch/adaptive.go

func (m *Manager) updateThroughput(batchSize int, duration time.Duration) {
    opsPerSec := float64(batchSize) / duration.Seconds()
    m.opsPerSecond.Store(uint64(opsPerSec))
}

func (m *Manager) StartAdaptiveAdjustment(ctx context.Context) {
    ticker := time.NewTicker(time.Duration(m.config.MetricsInterval) * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            m.adjustDelay()
        }
    }
}

func (m *Manager) adjustDelay() {
    currentOps := m.opsPerSecond.Load()
    currentDelay := m.currentDelay.Load()
    
    if currentOps > uint64(m.config.WriteThreshold) {
        // High write rate - increase delay to batch more
        newDelay := int64(float64(currentDelay) * m.config.AdaptiveStep)
        maxDelay := int64(m.config.MaxDelayMs * 1000) // convert to microseconds
        if newDelay > maxDelay {
            newDelay = maxDelay
        }
        m.currentDelay.Store(newDelay)
        
        m.metrics.DelayAdjustments.WithLabelValues("increase").Inc()
    } else if currentOps < uint64(m.config.WriteThreshold/2) {
        // Low write rate - decrease delay for lower latency
        newDelay := int64(float64(currentDelay) / m.config.AdaptiveStep)
        minDelay := int64(m.config.MinDelayMs * 1000) // convert to microseconds
        if newDelay < minDelay {
            newDelay = minDelay
        }
        m.currentDelay.Store(newDelay)
        
        m.metrics.DelayAdjustments.WithLabelValues("decrease").Inc()
    }
    
    // Record current delay
    m.metrics.CurrentDelay.Set(float64(m.currentDelay.Load()) / 1000.0) // in milliseconds
}
```

### 5. Metrics

Add new metrics to `metrics/metrics.go`:

```go
var (
    // Existing metrics...
    
    // Write Batch Metrics
    WriteBatchSize = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "tqdbproxy_write_batch_size",
            Help:    "Number of operations in each write batch",
            Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500},
        },
        []string{"batch_key"},
    )
    
    WriteBatchDelay = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "tqdbproxy_write_batch_delay_seconds",
            Help:    "Time between first operation enqueue and batch execution",
            Buckets: prometheus.DefBuckets,
        },
        []string{"batch_key"},
    )
    
    WriteBatchLatency = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "tqdbproxy_write_batch_latency_seconds",
            Help:    "Time to execute a write batch",
            Buckets: prometheus.DefBuckets,
        },
        []string{"batch_key"},
    )
    
    WriteOpsPerSecond = prometheus.NewGauge(
        prometheus.GaugeOpts{
            Name: "tqdbproxy_write_ops_per_second",
            Help: "Current write operations per second (for adaptive delay)",
        },
    )
    
    WriteCurrentDelay = prometheus.NewGauge(
        prometheus.GaugeOpts{
            Name: "tqdbproxy_write_current_delay_ms",
            Help: "Current adaptive batching delay in milliseconds",
        },
    )
    
    WriteDelayAdjustments = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "tqdbproxy_write_delay_adjustments_total",
            Help: "Number of delay adjustments (increase/decrease)",
        },
        []string{"direction"},
    )
    
    WriteBatchedTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "tqdbproxy_write_batched_total",
            Help: "Total write operations processed through batching",
        },
        []string{"file", "line", "query_type"},
    )
    
    WriteExcludedTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "tqdbproxy_write_excluded_total",
            Help: "Total write operations excluded from batching",
        },
        []string{"file", "line", "query_type", "reason"},
    )
)
```

### 6. Integration with Proxy

Update MariaDB and PostgreSQL proxy handlers to track transaction state:

```go
// mariadb/mariadb.go or postgres/postgres.go

// Add to connection state
type connState struct {
    // ... existing fields
    inTransaction bool  // Track if connection is in transaction
}

func (c *clientConn) handleSingleQuery(query string, ...) error {
    parsed := parser.Parse(query)
    
    // Track transaction state
    queryUpper := strings.ToUpper(strings.TrimSpace(query))
    if strings.HasPrefix(queryUpper, "BEGIN") || strings.HasPrefix(queryUpper, "START TRANSACTION") {
        c.state.inTransaction = true
    } else if strings.HasPrefix(queryUpper, "COMMIT") || strings.HasPrefix(queryUpper, "ROLLBACK") {
        c.state.inTransaction = false
    }
    
    // Existing read path
    if parsed.IsCacheable() {
        // ... existing cache logic
        return nil
    }
    
    // New write batching path - exclude writes in transactions
    if parsed.IsWritable() && !c.state.inTransaction && c.proxy.writeBatch != nil {
        result := c.proxy.writeBatch.Enqueue(ctx, parsed, params)
        if result.Error != nil {
            return c.sendError(result.Error)
        }
        return c.sendOKResult(result.AffectedRows, result.LastInsertID)
    }
    
    // Fallback to direct execution (includes writes in transactions)
    return c.executeDirectly(query)
}
```

### 7. Configuration

Add to `config/config.go`:

```go
type Config struct {
    // ... existing fields
    
    WriteBatch WriteBatchConfig
}

type WriteBatchConfig struct {
    Enabled           bool    `ini:"enabled"`
    InitialDelayMs    int     `ini:"initial_delay_ms"`
    MaxDelayMs        int     `ini:"max_delay_ms"`
    MinDelayMs        int     `ini:"min_delay_ms"`
    MaxBatchSize      int     `ini:"max_batch_size"`
    WriteThreshold    int     `ini:"write_threshold"`
    AdaptiveStep      float64 `ini:"adaptive_step"`
    MetricsInterval   int     `ini:"metrics_interval"`
}

func DefaultWriteBatchConfig() WriteBatchConfig {
    return WriteBatchConfig{
        Enabled:         false, // Opt-in initially
        InitialDelayMs:  1,
        MaxDelayMs:      100,
        MinDelayMs:      0,
        MaxBatchSize:    1000,
        WriteThreshold:  1000,
        AdaptiveStep:    1.5,
        MetricsInterval: 1,
    }
}
```

Add to `config.example.ini`:

```ini
[write_batch]
enabled = false
initial_delay_ms = 1
max_delay_ms = 100
min_delay_ms = 0
max_batch_size = 100
write_threshold = 1000
adaptive_step = 1.5
metrics_interval = 1
```

## Implementation Plan

The implementation is split into **7 detailed sub-plans** located in
[docs/stories/write-batching/](write-batching/):

### [Sub-Plan 1: Parser Extensions](write-batching/01-parser-extensions.md) (2-3 days)

- [ ] Add `IsWritable()`, `IsBatchable()`, `GetBatchKey()` methods
- [ ] Implement batch key generation from file:line
- [ ] Add comprehensive parser tests
- [ ] Update parser documentation

### [Sub-Plan 2: Write Batch Manager Core](write-batching/02-write-batch-manager.md) (5-7 days)

- [ ] Create `writebatch/` package structure
- [ ] Implement core types (WriteRequest, BatchGroup, Manager)
- [ ] Implement batching logic with delays
- [ ] Add single/prepared/transaction batch execution strategies
- [ ] Unit tests for manager and executor

### [Sub-Plan 3: Adaptive Delay System](write-batching/03-adaptive-delay-system.md) (3-4 days)

- [ ] Implement throughput tracking
- [ ] Add adaptive delay adjustment logic
- [ ] Implement background adjustment goroutine
- [ ] Test delay scaling under varying loads
- [ ] Validate min/max delay bounds

### [Sub-Plan 4: Metrics and Monitoring](write-batching/04-metrics-monitoring.md) (2-3 days)

- [ ] Define Prometheus metrics for write batching
- [ ] Integrate metrics into manager and adaptive system
- [ ] Create helper functions for metric extraction
- [ ] Add documentation with example queries
- [ ] Create sample Grafana dashboard

### [Sub-Plan 5: Proxy Integration](write-batching/05-proxy-integration.md) (4-5 days)

- [ ] Add transaction state tracking to both proxies
- [ ] Integrate WriteBatch manager into proxy structs
- [ ] Route writes to batch manager (exclude transactions)
- [ ] Add configuration loading and validation
- [ ] Handle prepared statements and results
- [ ] Integration tests

### [Sub-Plan 6: Benchmark Suite](write-batching/06-benchmark-suite.md) (3-4 days)

- [ ] Create pgx-based benchmark framework
- [ ] Implement load generators for 1k, 10k, 100k, 1M TPS
- [ ] Add metrics querying and parsing
- [ ] Generate visualization (throughput vs delay graphs)
- [ ] Validate acceptance criteria
- [ ] Document benchmark results

### [Sub-Plan 7: Testing and Validation](write-batching/07-testing-validation.md) (3-4 days)

- [ ] Achieve >90% code coverage
- [ ] Integration tests for MariaDB and PostgreSQL
- [ ] Load and stress tests
- [ ] Edge case validation
- [ ] Performance regression tests
- [ ] CI/CD integration
- [ ] Final validation and sign-off

**Total Estimated Effort**: 22-30 days (4.5-6 weeks)

See [write-batching/README.md](write-batching/README.md) for detailed sub-plan
overview and dependencies.

## Testing Strategy

### Unit Tests

- Parser write operation identification
- Transaction state detection
- Batch group management
- Delay calculation logic
- Metrics recording

### Integration Tests

- Single write execution
- Batch execution with identical queries
- Batch execution with mixed queries
- Result distribution to callers
- Error handling and rollback
- Writes within transactions bypass batching
- Transaction state tracking (BEGIN/COMMIT/ROLLBACK)

### Performance Tests

- Throughput improvement vs direct execution
- Latency distribution
- Adaptive delay behavior
- Memory usage
- Connection pool efficiency

### Load Tests

- Sustained high write rate
- Burst writes
- Mixed read/write workload
- Adaptive delay under stress

### Adaptive Delay Scaling Benchmark

Create a comprehensive benchmark using a pgx-based PostgreSQL client to validate
automatic delay scaling:

**Benchmark Setup:**

- Client: pgx (Go PostgreSQL driver)
- Test duration: 1 second per test
- Write operations: Simple INSERT statements from same file:line
- Metrics collected: Actual throughput (TPS), measured delay (ms), batch sizes

**Test Cases:**

| Target TPS | Expected Delay | Expected Behavior             |
| ---------- | -------------- | ----------------------------- |
| 1,000      | 0-1ms          | Low load, minimal batching    |
| 10,000     | 1-5ms          | Moderate load, small batches  |
| 100,000    | 10-20ms        | High load, medium batches     |
| 1,000,000  | 100ms          | Very high load, large batches |

**Implementation:**

```go
// benchmarks/writebatch/adaptive_bench.go

package writebatch

import (
    "context"
    "fmt"
    "sync"
    "sync/atomic"
    "time"
    
    "github.com/jackc/pgx/v5/pgxpool"
)

type BenchmarkResult struct {
    TargetTPS      int
    ActualTPS      float64
    AvgDelayMs     float64
    P50DelayMs     float64
    P95DelayMs     float64
    P99DelayMs     float64
    AvgBatchSize   float64
    MaxBatchSize   int
    SuccessRate    float64
}

func RunAdaptiveBenchmark() []BenchmarkResult {
    results := make([]BenchmarkResult, 0, 4)
    
    targets := []int{1_000, 10_000, 100_000, 1_000_000}
    
    for _, target := range targets {
        result := runSingleBenchmark(target)
        results = append(results, result)
        time.Sleep(5 * time.Second) // Cool down between tests
    }
    
    return results
}

func runSingleBenchmark(targetTPS int) BenchmarkResult {
    pool, _ := pgxpool.New(context.Background(), "postgres://user:pass@localhost:5432/testdb")
    defer pool.Close()
    
    ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
    defer cancel()
    
    var (
        completed    atomic.Int64
        failed       atomic.Int64
        totalLatency atomic.Int64  // in microseconds
        wg           sync.WaitGroup
    )
    
    // Calculate rate: operations per goroutine
    numWorkers := 100
    opsPerWorker := targetTPS / numWorkers
    interval := time.Second / time.Duration(opsPerWorker)
    
    start := time.Now()
    
    for i := 0; i < numWorkers; i++ {
        wg.Add(1)
        go func(workerID int) {
            defer wg.Done()
            ticker := time.NewTicker(interval)
            defer ticker.Stop()
            
            for {
                select {
                case <-ctx.Done():
                    return
                case <-ticker.C:
                    opStart := time.Now()
                    query := fmt.Sprintf(
                        "/* ttl:0 file:bench.go line:%d */ INSERT INTO test_writes (data) VALUES ($1)",
                        workerID,
                    )
                    _, err := pool.Exec(ctx, query, time.Now().Unix())
                    latency := time.Since(opStart)
                    
                    if err == nil {
                        completed.Add(1)
                        totalLatency.Add(int64(latency.Microseconds()))
                    } else {
                        failed.Add(1)
                    }
                }
            }
        }(i)
    }
    
    wg.Wait()
    duration := time.Since(start)
    
    totalOps := completed.Load()
    actualTPS := float64(totalOps) / duration.Seconds()
    avgLatencyUs := float64(totalLatency.Load()) / float64(totalOps)
    
    // Query proxy metrics for batch info
    batchMetrics := queryProxyMetrics()
    
    return BenchmarkResult{
        TargetTPS:    targetTPS,
        ActualTPS:    actualTPS,
        AvgDelayMs:   batchMetrics.AvgDelay,
        P50DelayMs:   batchMetrics.P50Delay,
        P95DelayMs:   batchMetrics.P95Delay,
        P99DelayMs:   batchMetrics.P99Delay,
        AvgBatchSize: batchMetrics.AvgBatchSize,
        MaxBatchSize: batchMetrics.MaxBatchSize,
        SuccessRate:  float64(totalOps) / float64(totalOps+failed.Load()) * 100,
    }
}
```

**Visualization:**

Generate a combined graph showing throughput and delay correlation:

```go
// benchmarks/writebatch/plot.go

import (
    "github.com/go-echarts/go-echarts/v2/charts"
    "github.com/go-echarts/go-echarts/v2/opts"
)

func PlotResults(results []BenchmarkResult) {
    line := charts.NewLine()
    line.SetGlobalOptions(
        charts.WithTitleOpts(opts.Title{
            Title:    "Write Batch Adaptive Delay Scaling",
            Subtitle: "Throughput vs Delay Adjustment",
        }),
        charts.WithYAxisOpts(opts.YAxis{
            Name: "Delay (ms)",
            Type: "value",
        }),
        charts.WithXAxisOpts(opts.XAxis{
            Name: "Target TPS",
            Type: "category",
        }),
        charts.WithLegendOpts(opts.Legend{
            Show: true,
            Top:  "10%",
        }),
    )
    
    // X-axis: Target TPS
    xAxis := make([]string, len(results))
    avgDelays := make([]opts.LineData, len(results))
    p95Delays := make([]opts.LineData, len(results))
    batchSizes := make([]opts.LineData, len(results))
    actualTPS := make([]opts.LineData, len(results))
    
    for i, r := range results {
        xAxis[i] = fmt.Sprintf("%dk", r.TargetTPS/1000)
        avgDelays[i] = opts.LineData{Value: r.AvgDelayMs}
        p95Delays[i] = opts.LineData{Value: r.P95DelayMs}
        batchSizes[i] = opts.LineData{Value: r.AvgBatchSize}
        actualTPS[i] = opts.LineData{Value: r.ActualTPS / 1000} // in thousands
    }
    
    line.SetXAxis(xAxis).
        AddSeries("Average Delay (ms)", avgDelays).
        AddSeries("P95 Delay (ms)", p95Delays).
        AddSeries("Avg Batch Size", batchSizes).
        AddSeries("Actual TPS (k)", actualTPS).
        SetSeriesOptions(
            charts.WithLineChartOpts(opts.LineChart{
                Smooth: true,
            }),
            charts.WithMarkPointNameTypeItemOpts(opts.MarkPointNameTypeItem{
                Name: "Maximum",
                Type: "max",
            }),
        )
    
    // Save to HTML
    f, _ := os.Create("adaptive_delay_benchmark.html")
    line.Render(f)
}
```

**Expected Results:**

```
+-------------+-------------+-----------+-------------+
| Target TPS  | Actual TPS  | Avg Delay | Avg Batch   |
+-------------+-------------+-----------+-------------+
| 1,000       | ~980        | 0.5ms     | 1-2 ops     |
| 10,000      | ~9,500      | 2.5ms     | 15-25 ops   |
| 100,000     | ~92,000     | 15ms      | 150-200 ops |
| 1,000,000   | ~850,000    | 85ms      | 800-1000 ops|
+-------------+-------------+-----------+-------------+
```

**Acceptance Criteria:**

- System sustains ≥95% of target TPS for all test cases
- Delay automatically scales proportionally to load
- No crashes or memory leaks during 1M TPS test
- Batch sizes increase with TPS (correlation ≥0.95)
- Graph clearly shows delay/throughput relationship

## Metrics and Monitoring

### Key Metrics to Track

- `tqdbproxy_write_batch_size`: Distribution of batch sizes
- `tqdbproxy_write_batch_delay_seconds`: Batching window duration
- `tqdbproxy_write_batch_latency_seconds`: Execution time
- `tqdbproxy_write_ops_per_second`: Current write throughput
- `tqdbproxy_write_current_delay_ms`: Current adaptive delay
- `tqdbproxy_write_delay_adjustments_total`: Adjustment frequency
- `tqdbproxy_write_excluded_total`: Writes excluded from batching (by reason:
  transaction, etc.)

### Alerting

- High write batch failure rate
- Excessive batch delays (> 10ms avg)
- Adaptive delay stuck at max
- Write throughput degradation

## Success Criteria

1. **Performance**: 30%+ reduction in database write operations under load
2. **Latency**: P99 write latency within 2x of direct execution
3. **Correctness**: 100% of writes executed and results returned
4. **Stability**: No memory leaks or connection pool exhaustion
5. **Observability**: All key metrics available and accurate

## Risk Assessment

### High Risk

- **Data Loss**: Incorrect batching could lose writes
  - Mitigation: Comprehensive testing, conservative defaults, opt-in
- **Latency Impact**: Batching adds delay to writes
  - Mitigation: Adaptive delay, configurable parameters, monitoring

### Medium Risk

- **Complexity**: New component increases system complexity
  - Mitigation: Clear documentation, gradual rollout
- **Transaction Conflicts**: Batched writes in transaction may conflict
  - Mitigation: Fallback to individual execution, clear error messages

### Low Risk

- **Memory Usage**: Buffering writes uses memory
  - Mitigation: Max batch size limit, monitoring

## Future Enhancements

1. **Advanced Delay Scaling**: Machine learning-based delay prediction using
   historical patterns
2. **Smart Batching**: Analyze query patterns to optimize batching strategy
3. **Cross-Location Batching**: Batch writes from different source locations
4. **Write Coalescing**: Merge redundant writes (e.g., multiple updates to same
   row)
5. **Priority Queues**: High-priority writes bypass batching
6. **Write-Read Consistency**: Coordinate with read cache invalidation
7. **Multi-Database Batching**: Coordinate batching across sharded databases

## References

- Current cache implementation: [cache/cache.go](../../cache/cache.go)
- Parser implementation: [parser/parser.go](../../parser/parser.go)
- Metrics: [metrics/metrics.go](../../metrics/metrics.go)
- Single-flight pattern:
  [docs/components/cache/README.md](../components/cache/README.md)
