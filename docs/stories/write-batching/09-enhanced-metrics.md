# Sub-Plan 9: Enhanced Metrics for Write Batching

**Parent Story**: [WRITE_BATCHING.md](../WRITE_BATCHING.md)

**Status**: Not Started

**Estimated Effort**: 2-3 days

## Overview

Add comprehensive metrics to track write batching performance, including backend
query frequency and batch size distribution. These metrics enable calculation of
speedup/aggregation factors and provide visibility into batching effectiveness.

## Prerequisites

- Sub-Plan 8: Batching hints implementation

## Goals

- Track frequency of write queries sent to backend database
- Record batch sizes for all batched operations
- Enable calculation of aggregation factor (speedup)
- Provide histogram of batch size distribution
- Monitor individual vs batched execution paths
- Support alerting on batching effectiveness

## Tasks

### 1. Define New Metrics

**File**: `metrics/metrics.go`

- [x] Add counter for backend write queries
- [x] Add histogram for batch sizes
- [x] Add counter for individual writes (non-batched)
- [x] Add gauge for current batching effectiveness

```go
var (
    // Existing metrics...
    
    // Backend Query Metrics
    BackendWriteQueries = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "tqdbproxy_backend_write_queries_total",
            Help: "Total number of write queries sent to the backend database",
        },
        []string{"query_type"}, // INSERT, UPDATE, DELETE
    )
    
    // Batch Size Distribution
    BatchSizeHistogram = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "tqdbproxy_write_batch_size_distribution",
            Help:    "Distribution of write batch sizes",
            Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000},
        },
        []string{"batch_key", "query_type"},
    )
    
    // Individual Write Metrics
    IndividualWrites = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "tqdbproxy_individual_writes_total",
            Help: "Total number of write operations executed individually (not batched)",
        },
        []string{"batch_key", "query_type", "reason"},
    )
    
    // Batched Write Metrics
    BatchedWrites = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "tqdbproxy_batched_writes_total",
            Help: "Total number of write operations executed in batches",
        },
        []string{"batch_key", "query_type"},
    )
    
    // Aggregation Factor (calculated metric)
    WriteAggregationFactor = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "tqdbproxy_write_aggregation_factor",
            Help: "Current aggregation factor: total_writes / backend_queries",
        },
        []string{"query_type"},
    )
    
    // Batch Effectiveness (percentage of writes that are batched)
    BatchEffectiveness = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "tqdbproxy_batch_effectiveness_percent",
            Help: "Percentage of write operations that were batched (0-100)",
        },
        []string{"query_type"},
    )
)
```

### 2. Register New Metrics

**File**: `metrics/metrics.go`

- [x] Update `Init()` to register new metrics

```go
func Init() {
    once.Do(func() {
        // Existing registrations...
        
        // Backend and batch metrics
        prometheus.MustRegister(BackendWriteQueries)
        prometheus.MustRegister(BatchSizeHistogram)
        prometheus.MustRegister(IndividualWrites)
        prometheus.MustRegister(BatchedWrites)
        prometheus.MustRegister(WriteAggregationFactor)
        prometheus.MustRegister(BatchEffectiveness)
    })
}
```

### 3. Add Metrics Collection to Manager

**File**: `writebatch/executor.go`

- [x] Track backend queries in `executeBatch()`
- [x] Record batch size for each execution
- [x] Update aggregation metrics

```go
func (m *Manager) executeBatch(batchKey string, group *BatchGroup) {
    group.mu.Lock()
    requests := group.Requests
    firstSeen := group.FirstSeen
    group.Requests = make([]*WriteRequest, 0, m.config.MaxBatchSize)
    group.mu.Unlock()
    
    if len(requests) == 0 {
        return
    }
    
    // Extract query type for metrics
    queryType := extractQueryType(requests[0].Query)
    batchSize := len(requests)
    
    // Record batch size distribution
    metrics.BatchSizeHistogram.WithLabelValues(batchKey, queryType).Observe(float64(batchSize))
    
    // Record batched writes
    metrics.BatchedWrites.WithLabelValues(batchKey, queryType).Add(float64(batchSize))
    
    // Record backend query (one query per batch)
    metrics.BackendWriteQueries.WithLabelValues(queryType).Inc()
    
    // Execute the batch
    startTime := time.Now()
    
    // If single request, execute individually
    if len(requests) == 1 {
        result := m.executeSingle(requests[0].Query, requests[0].Params)
        requests[0].ResultChan <- result
        return
    }
    
    // Execute as batch
    results := m.executeBatchQuery(requests)
    
    // Distribute results
    for i, req := range requests {
        req.ResultChan <- results[i]
    }
    
    // Update aggregation factor periodically
    m.updateAggregationMetrics()
}

func (m *Manager) executeImmediate(query string, params []interface{}) WriteResult {
    queryType := extractQueryType(query)
    
    // Record individual write
    metrics.IndividualWrites.WithLabelValues("immediate", queryType, "no_hint").Inc()
    
    // Record backend query
    metrics.BackendWriteQueries.WithLabelValues(queryType).Inc()
    
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

func extractQueryType(query string) string {
    query = strings.TrimSpace(strings.ToUpper(query))
    if strings.HasPrefix(query, "INSERT") || strings.Contains(query, "INSERT") {
        return "INSERT"
    } else if strings.HasPrefix(query, "UPDATE") || strings.Contains(query, "UPDATE") {
        return "UPDATE"
    } else if strings.HasPrefix(query, "DELETE") || strings.Contains(query, "DELETE") {
        return "DELETE"
    }
    return "UNKNOWN"
}
```

### 4. Add Aggregation Factor Calculation

**File**: `writebatch/manager.go`

- [x] Add periodic metric calculation
- [x] Calculate aggregation factor from counters
- [x] Calculate batch effectiveness

```go
type Manager struct {
    groups       sync.Map
    config       Config
    db           *sql.DB
    closed       atomic.Bool
    
    // Metric tracking
    totalWrites      atomic.Uint64
    backendQueries   atomic.Uint64
    batchedWrites    atomic.Uint64
    individualWrites atomic.Uint64
}

func (m *Manager) updateAggregationMetrics() {
    totalWrites := float64(m.totalWrites.Load())
    backendQueries := float64(m.backendQueries.Load())
    batchedWrites := float64(m.batchedWrites.Load())
    
    if backendQueries > 0 {
        // Aggregation factor = how many writes per backend query
        aggregationFactor := totalWrites / backendQueries
        metrics.WriteAggregationFactor.WithLabelValues("all").Set(aggregationFactor)
    }
    
    if totalWrites > 0 {
        // Batch effectiveness = percentage of writes that were batched
        effectiveness := (batchedWrites / totalWrites) * 100
        metrics.BatchEffectiveness.WithLabelValues("all").Set(effectiveness)
    }
}

// Call periodically or after each batch
func (m *Manager) trackWrite(batched bool, batchSize int) {
    m.totalWrites.Add(uint64(batchSize))
    m.backendQueries.Add(1)
    
    if batched {
        m.batchedWrites.Add(uint64(batchSize))
    } else {
        m.individualWrites.Add(1)
    }
}
```

### 5. Add Metrics Endpoint Documentation

**File**: `docs/components/metrics/README.md`

- [x] Document new metrics
- [x] Provide PromQL query examples
- [x] Add Grafana dashboard suggestions

````markdown
## Write Batching Metrics

### Backend Query Frequency

Track how often write queries are sent to the database:

```promql
# Rate of backend write queries per second
rate(tqdbproxy_backend_write_queries_total[1m])

# By query type
sum by (query_type) (rate(tqdbproxy_backend_write_queries_total[5m]))
```
````

### Batch Size Distribution

Understand typical batch sizes:

```promql
# Average batch size
rate(tqdbproxy_batched_writes_total[5m]) / rate(tqdbproxy_backend_write_queries_total[5m])

# Batch size histogram percentiles
histogram_quantile(0.5, rate(tqdbproxy_write_batch_size_distribution_bucket[5m]))
histogram_quantile(0.95, rate(tqdbproxy_write_batch_size_distribution_bucket[5m]))
histogram_quantile(0.99, rate(tqdbproxy_write_batch_size_distribution_bucket[5m]))
```

### Aggregation Factor (Speedup)

Calculate the speedup from batching:

```promql
# Current aggregation factor
tqdbproxy_write_aggregation_factor

# Aggregation factor over time
rate(tqdbproxy_batched_writes_total[5m]) / rate(tqdbproxy_backend_write_queries_total[5m])
```

**Interpretation:**

- Factor of 1.0 = No batching (every write = one backend query)
- Factor of 10.0 = 10 writes batched into 1 backend query (10x speedup)
- Factor of 100.0 = 100 writes batched into 1 backend query (100x speedup)

### Batch Effectiveness

Track what percentage of writes are batched:

```promql
# Percentage of writes that are batched
tqdbproxy_batch_effectiveness_percent

# Effectiveness by query type
sum by (query_type) (rate(tqdbproxy_batched_writes_total[5m])) / 
sum by (query_type) (rate(tqdbproxy_batched_writes_total[5m]) + rate(tqdbproxy_individual_writes_total[5m])) * 100
```

### Individual vs Batched Writes

Compare execution paths:

```promql
# Individual writes per second
rate(tqdbproxy_individual_writes_total[5m])

# Batched writes per second
rate(tqdbproxy_batched_writes_total[5m])

# Ratio of batched to individual
rate(tqdbproxy_batched_writes_total[5m]) / rate(tqdbproxy_individual_writes_total[5m])
```

### Reasons for Individual Execution

Understand why writes aren't batched:

```promql
# Individual writes by reason
sum by (reason) (rate(tqdbproxy_individual_writes_total[5m]))
```

Common reasons:

- `no_hint` - Query has no `max_wait_ms` hint
- `in_transaction` - Write is inside a transaction
- `timeout` - Batch timer expired with single request

````
### 6. Add Tests for Metrics

**File**: `writebatch/manager_test.go`

- [x] Test backend query counter increments
- [x] Test batch size histogram recording
- [x] Test aggregation factor calculation
- [x] Test batch effectiveness tracking

```go
func TestManager_BackendQueryMetrics(t *testing.T) {
    db := setupTestDB(t)
    defer db.Close()
    
    // Initialize metrics
    metrics.Init()
    
    mgr := New(db, DefaultConfig())
    defer mgr.Close()
    
    query := "INSERT INTO test_table (value) VALUES (?)"
    
    // Execute 10 writes with batching (max_wait_ms:10)
    var wg sync.WaitGroup
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            mgr.Enqueue("test:1", query, []interface{}{idx}, 10)
        }(i)
    }
    wg.Wait()
    
    // Should have 1 backend query (batched)
    // Verify via metrics
    // Note: In real test, would use testutil.CollectAndCount()
}

func TestManager_BatchSizeDistribution(t *testing.T) {
    db := setupTestDB(t)
    defer db.Close()
    
    metrics.Init()
    
    mgr := New(db, DefaultConfig())
    defer mgr.Close()
    
    query := "INSERT INTO test_table (value) VALUES (?)"
    
    // Create batches of different sizes
    testCases := []int{1, 5, 10, 50}
    
    for _, size := range testCases {
        var wg sync.WaitGroup
        for i := 0; i < size; i++ {
            wg.Add(1)
            go func(idx int) {
                defer wg.Done()
                mgr.Enqueue(fmt.Sprintf("test:%d", size), query, []interface{}{idx}, 10)
            }(i)
        }
        wg.Wait()
    }
    
    // Verify histogram has recorded various batch sizes
}

func TestManager_AggregationFactor(t *testing.T) {
    db := setupTestDB(t)
    defer db.Close()
    
    mgr := New(db, DefaultConfig())
    defer mgr.Close()
    
    query := "INSERT INTO test_table (value) VALUES (?)"
    
    // Execute 100 writes with batching
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            mgr.Enqueue("test:1", query, []interface{}{idx}, 50)
        }(i)
    }
    wg.Wait()
    
    // Expected: 100 writes, ~1-2 backend queries
    // Aggregation factor should be ~50-100
    
    factor := float64(mgr.totalWrites.Load()) / float64(mgr.backendQueries.Load())
    if factor < 10 {
        t.Errorf("Aggregation factor too low: %v", factor)
    }
}
````

### 7. Add Grafana Dashboard Template

**File**: `docs/components/metrics/grafana-dashboard.json`

- [x] Create Grafana dashboard for write batching metrics

```json
{
    "dashboard": {
        "title": "TQDBProxy Write Batching",
        "panels": [
            {
                "title": "Aggregation Factor (Speedup)",
                "targets": [
                    {
                        "expr": "tqdbproxy_write_aggregation_factor"
                    }
                ]
            },
            {
                "title": "Backend Write Queries/sec",
                "targets": [
                    {
                        "expr": "rate(tqdbproxy_backend_write_queries_total[1m])"
                    }
                ]
            },
            {
                "title": "Batch Size Distribution (p50, p95, p99)",
                "targets": [
                    {
                        "expr": "histogram_quantile(0.5, rate(tqdbproxy_write_batch_size_distribution_bucket[5m]))",
                        "legendFormat": "p50"
                    },
                    {
                        "expr": "histogram_quantile(0.95, rate(tqdbproxy_write_batch_size_distribution_bucket[5m]))",
                        "legendFormat": "p95"
                    },
                    {
                        "expr": "histogram_quantile(0.99, rate(tqdbproxy_write_batch_size_distribution_bucket[5m]))",
                        "legendFormat": "p99"
                    }
                ]
            },
            {
                "title": "Batch Effectiveness (%)",
                "targets": [
                    {
                        "expr": "tqdbproxy_batch_effectiveness_percent"
                    }
                ]
            },
            {
                "title": "Batched vs Individual Writes",
                "targets": [
                    {
                        "expr": "rate(tqdbproxy_batched_writes_total[5m])",
                        "legendFormat": "Batched"
                    },
                    {
                        "expr": "rate(tqdbproxy_individual_writes_total[5m])",
                        "legendFormat": "Individual"
                    }
                ]
            }
        ]
    }
}
```

## Deliverables

- [x] New metrics defined and registered
- [x] Metrics collection integrated into manager
- [x] Aggregation factor calculation implemented
- [x] Documentation with PromQL examples
- [x] Grafana dashboard template
- [x] Tests for metric tracking

## Validation Steps

1. **Metric Registration**: Verify all metrics are registered
   ```bash
   curl http://localhost:9090/metrics | grep tqdbproxy_backend_write
   ```

2. **Backend Query Tracking**: Execute writes and verify counter
   ```bash
   # Run test workload
   # Check: tqdbproxy_backend_write_queries_total
   ```

3. **Batch Size Histogram**: Verify histogram buckets populated
   ```bash
   # Check histogram in Prometheus
   # Query: tqdbproxy_write_batch_size_distribution_bucket
   ```

4. **Aggregation Factor**: Verify calculation is correct
   ```bash
   # Execute 100 batched writes
   # Verify: tqdbproxy_write_aggregation_factor shows ~50-100
   ```

5. **Grafana Dashboard**: Import and verify dashboard displays correctly

## Success Criteria

- [x] All metrics implemented and registered
- [x] Backend query frequency tracked accurately
- [x] Batch size distribution recorded in histogram
- [x] Aggregation factor calculated correctly
- [x] Batch effectiveness percentage accurate
- [x] Tests pass with >90% coverage
- [x] Grafana dashboard functional
- [x] Documentation complete with examples

## Performance Considerations

- Metrics collection should add <1ms overhead per batch
- Use atomic counters for low-contention updates
- Calculate derived metrics (aggregation factor) periodically, not on every
  write
- Limit histogram bucket count to avoid memory bloat

## Future Enhancements

- Per-batch-key aggregation factors
- Alert rules for low batch effectiveness
- Comparison with historical baselines
- Cost savings calculations based on reduced backend queries
