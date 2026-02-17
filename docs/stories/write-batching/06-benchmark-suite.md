# Sub-Plan 6: Benchmark Suite

**Parent Story**: [WRITE_BATCHING.md](../WRITE_BATCHING.md)

**Status**: Not Started

**Estimated Effort**: 3-4 days

## Overview

Create comprehensive benchmarks using pgx client to validate adaptive delay
scaling at different load levels (1k, 10k, 100k, 1M TPS).

## Prerequisites

- [x] All previous sub-plans completed (1-5)
- [x] Proxy running with write batching enabled
- [x] Metrics endpoint accessible

## Goals

- Benchmark at 4 load levels: 1k, 10k, 100k, 1M TPS
- Validate automatic delay scaling
- Measure actual throughput and latency
- Generate visualization of results
- Verify system stability under load

## Tasks

### 1. Create Benchmark Package Structure

```bash
mkdir -p benchmarks/writebatch
touch benchmarks/writebatch/adaptive_bench.go
touch benchmarks/writebatch/plot.go
touch benchmarks/writebatch/main.go
touch benchmarks/writebatch/go.mod
```

### 2. Implement Core Benchmark

**File**: `benchmarks/writebatch/adaptive_bench.go`

- [ ] Define benchmark result types
- [ ] Implement load generator for each TPS level
- [ ] Track latency percentiles
- [ ] Query proxy metrics during/after test
- [ ] Implement cooldown between tests

```go
package main

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
    Latencies      []time.Duration
}

func RunAdaptiveBenchmark(connStr string) []BenchmarkResult {
    results := make([]BenchmarkResult, 0, 4)
    
    targets := []int{1_000, 10_000, 100_000, 1_000_000}
    
    for _, target := range targets {
        fmt.Printf("\\n=== Running benchmark: %d TPS ===\\n", target)
        result := runSingleBenchmark(connStr, target)
        results = append(results, result)
        
        fmt.Printf("Results: Actual TPS=%.0f, Avg Delay=%.2fms, Avg Batch=%.1f\\n",
            result.ActualTPS, result.AvgDelayMs, result.AvgBatchSize)
        
        // Cooldown between tests
        fmt.Println("Cooling down for 5 seconds...")
        time.Sleep(5 * time.Second)
    }
    
    return results
}

func runSingleBenchmark(connStr string, targetTPS int) BenchmarkResult {
    // Create connection pool
    poolConfig, _ := pgxpool.ParseConfig(connStr)
    poolConfig.MaxConns = 100
    pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
    if err != nil {
        panic(err)
    }
    defer pool.Close()
    
    // Setup test table
    _, err = pool.Exec(context.Background(), `
        CREATE TABLE IF NOT EXISTS test_writes (
            id SERIAL PRIMARY KEY,
            data BIGINT,
            created_at TIMESTAMP DEFAULT NOW()
        )
    `)
    if err != nil {
        panic(err)
    }
    
    // Clear old data
    pool.Exec(context.Background(), "TRUNCATE test_writes")
    
    ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
    defer cancel()
    
    var (
        completed    atomic.Int64
        failed       atomic.Int64
        latencies    sync.Mutex
        latencyList  = make([]time.Duration, 0, targetTPS)
        wg           sync.WaitGroup
    )
    
    // Calculate distribution
    numWorkers := 100
    opsPerWorker := targetTPS / numWorkers
    interval := time.Second / time.Duration(opsPerWorker)
    
    if interval < time.Microsecond {
        interval = time.Microsecond
    }
    
    start := time.Now()
    
    // Launch workers
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
                        latencies.Lock()
                        latencyList = append(latencyList, latency)
                        latencies.Unlock()
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
    
    // Calculate latency percentiles
    sort.Slice(latencyList, func(i, j int) bool {
        return latencyList[i] < latencyList[j]
    })
    
    p50 := percentile(latencyList, 0.50)
    p95 := percentile(latencyList, 0.95)
    p99 := percentile(latencyList, 0.99)
    
    // Query proxy metrics
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
        Latencies:    latencyList,
    }
}

func percentile(data []time.Duration, p float64) float64 {
    if len(data) == 0 {
        return 0
    }
    idx := int(float64(len(data)) * p)
    if idx >= len(data) {
        idx = len(data) - 1
    }
    return float64(data[idx].Microseconds()) / 1000.0 // to milliseconds
}
```

### 3. Implement Metrics Querying

**File**: `benchmarks/writebatch/metrics.go`

- [ ] Query Prometheus metrics endpoint
- [ ] Parse batch size histogram
- [ ] Parse delay histogram
- [ ] Calculate averages and percentiles

```go
package main

import (
    "io"
    "net/http"
    "regexp"
    "strconv"
    "strings"
)

type BatchMetrics struct {
    AvgDelay     float64
    P50Delay     float64
    P95Delay     float64
    P99Delay     float64
    AvgBatchSize float64
    MaxBatchSize int
}

func queryProxyMetrics() BatchMetrics {
    resp, err := http.Get("http://localhost:9090/metrics")
    if err != nil {
        return BatchMetrics{}
    }
    defer resp.Body.Close()
    
    body, _ := io.ReadAll(resp.Body)
    content := string(body)
    
    return BatchMetrics{
        AvgDelay:     extractAvgDelay(content),
        P50Delay:     extractPercentileDelay(content, 0.5),
        P95Delay:     extractPercentileDelay(content, 0.95),
        P99Delay:     extractPercentileDelay(content, 0.99),
        AvgBatchSize: extractAvgBatchSize(content),
        MaxBatchSize: extractMaxBatchSize(content),
    }
}

func extractAvgDelay(content string) float64 {
    // Parse: tqdbproxy_write_batch_delay_seconds_sum / _count
    sumRe := regexp.MustCompile(`tqdbproxy_write_batch_delay_seconds_sum\\{.*?\\} ([\\d.]+)`)
    countRe := regexp.MustCompile(`tqdbproxy_write_batch_delay_seconds_count\\{.*?\\} ([\\d.]+)`)
    
    sumMatches := sumRe.FindStringSubmatch(content)
    countMatches := countRe.FindStringSubmatch(content)
    
    if len(sumMatches) > 1 && len(countMatches) > 1 {
        sum, _ := strconv.ParseFloat(sumMatches[1], 64)
        count, _ := strconv.ParseFloat(countMatches[1], 64)
        if count > 0 {
            return (sum / count) * 1000 // to milliseconds
        }
    }
    return 0
}

func extractAvgBatchSize(content string) float64 {
    sumRe := regexp.MustCompile(`tqdbproxy_write_batch_size_sum\\{.*?\\} ([\\d.]+)`)
    countRe := regexp.MustCompile(`tqdbproxy_write_batch_size_count\\{.*?\\} ([\\d.]+)`)
    
    sumMatches := sumRe.FindStringSubmatch(content)
    countMatches := countRe.FindStringSubmatch(content)
    
    if len(sumMatches) > 1 && len(countMatches) > 1 {
        sum, _ := strconv.ParseFloat(sumMatches[1], 64)
        count, _ := strconv.ParseFloat(countMatches[1], 64)
        if count > 0 {
            return sum / count
        }
    }
    return 0
}

func extractMaxBatchSize(content string) int {
    // Find maximum value in histogram buckets
    re := regexp.MustCompile(`tqdbproxy_write_batch_size_bucket\\{.*?le="([\\d.]+)".*?\\} ([\\d.]+)`)
    matches := re.FindAllStringSubmatch(content, -1)
    
    maxSize := 0
    for _, match := range matches {
        if len(match) > 2 {
            le, _ := strconv.ParseFloat(match[1], 64)
            count, _ := strconv.ParseFloat(match[2], 64)
            if count > 0 && int(le) > maxSize {
                maxSize = int(le)
            }
        }
    }
    return maxSize
}

func extractPercentileDelay(content string, p float64) float64 {
    // Simplified: extract from histogram buckets
    // In practice, use Prometheus histogram_quantile
    return 0 // Implement based on histogram data
}
```

### 4. Implement Visualization

**File**: `benchmarks/writebatch/plot.go`

- [ ] Create combined line chart
- [ ] Plot throughput, delay, and batch size
- [ ] Generate HTML output
- [ ] Add styling and annotations

```go
package main

import (
    "fmt"
    "os"
    
    "github.com/go-echarts/go-echarts/v2/charts"
    "github.com/go-echarts/go-echarts/v2/opts"
)

func PlotResults(results []BenchmarkResult) {
    line := charts.NewLine()
    line.SetGlobalOptions(
        charts.WithTitleOpts(opts.Title{
            Title:    "Write Batch Adaptive Delay Scaling",
            Subtitle: "Throughput vs Delay Adjustment (1 second per test)",
        }),
        charts.WithYAxisOpts(opts.YAxis{
            Name: "Value",
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
        charts.WithTooltipOpts(opts.Tooltip{
            Show:    true,
            Trigger: "axis",
        }),
    )
    
    // Prepare data
    xAxis := make([]string, len(results))
    avgDelays := make([]opts.LineData, len(results))
    p95Delays := make([]opts.LineData, len(results))
    batchSizes := make([]opts.LineData, len(results))
    actualTPS := make([]opts.LineData, len(results))
    
    for i, r := range results {
        xAxis[i] = formatTPS(r.TargetTPS)
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
            charts.WithMarkPointNameTypeItemOpts(
                opts.MarkPointNameTypeItem{Name: "Maximum", Type: "max"},
                opts.MarkPointNameTypeItem{Name: "Minimum", Type: "min"},
            ),
        )
    
    f, _ := os.Create("adaptive_delay_benchmark.html")
    defer f.Close()
    line.Render(f)
    
    fmt.Println("\\nVisualization saved to: adaptive_delay_benchmark.html")
}

func formatTPS(tps int) string {
    if tps >= 1_000_000 {
        return fmt.Sprintf("%dM", tps/1_000_000)
    } else if tps >= 1_000 {
        return fmt.Sprintf("%dk", tps/1_000)
    }
    return fmt.Sprintf("%d", tps)
}
```

### 5. Create Main Entry Point

**File**: `benchmarks/writebatch/main.go`

- [ ] Parse command line arguments
- [ ] Run benchmark suite
- [ ] Print summary table
- [ ] Generate visualization

```go
package main

import (
    "flag"
    "fmt"
    "os"
    "text/tabwriter"
)

func main() {
    connStr := flag.String("conn", "postgres://tqdbproxy:tqdbproxy@localhost:5432/testdb", "PostgreSQL connection string")
    flag.Parse()
    
    fmt.Println("===========================================")
    fmt.Println("Write Batch Adaptive Delay Benchmark Suite")
    fmt.Println("===========================================")
    
    results := RunAdaptiveBenchmark(*connStr)
    
    // Print summary table
    printSummaryTable(results)
    
    // Generate visualization
    PlotResults(results)
    
    // Validate acceptance criteria
    validateResults(results)
}

func printSummaryTable(results []BenchmarkResult) {
    fmt.Println("\\n=== Benchmark Results ===\\n")
    
    w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight|tabwriter.Debug)
    fmt.Fprintln(w, "Target TPS\\tActual TPS\\tAvg Delay\\tP95 Delay\\tAvg Batch\\tSuccess Rate\\t")
    fmt.Fprintln(w, "----------\\t----------\\t---------\\t---------\\t---------\\t------------\\t")
    
    for _, r := range results {
        fmt.Fprintf(w, "%d\\t%.0f\\t%.2f ms\\t%.2f ms\\t%.1f\\t%.1f%%\\t\\n",
            r.TargetTPS,
            r.ActualTPS,
            r.AvgDelayMs,
            r.P95DelayMs,
            r.AvgBatchSize,
            r.SuccessRate,
        )
    }
    
    w.Flush()
}

func validateResults(results []BenchmarkResult) {
    fmt.Println("\\n=== Validation ===\\n")
    
    allPassed := true
    
    for _, r := range results {
        achievedPercent := (r.ActualTPS / float64(r.TargetTPS)) * 100
        passed := achievedPercent >= 95.0
        
        status := "✓ PASS"
        if !passed {
            status = "✗ FAIL"
            allPassed = false
        }
        
        fmt.Printf("%s - %d TPS: %.1f%% of target\\n", status, r.TargetTPS, achievedPercent)
    }
    
    if allPassed {
        fmt.Println("\\n✓ All benchmarks passed!")
    } else {
        fmt.Println("\\n✗ Some benchmarks failed")
        os.Exit(1)
    }
}
```

### 6. Dependencies

**File**: `benchmarks/writebatch/go.mod`

```go
module github.com/mevdschee/tqdbproxy/benchmarks/writebatch

go 1.21

require (
    github.com/go-echarts/go-echarts/v2 v2.3.3
    github.com/jackc/pgx/v5 v5.5.0
)
```

## Deliverables

- [ ] Complete benchmark suite implementation
- [ ] Metrics querying and parsing
- [ ] Visualization generation
- [ ] Validation logic
- [ ] Documentation

## Running the Benchmark

```bash
# Build
cd benchmarks/writebatch
go build -o writebatch-bench

# Run benchmark
./writebatch-bench --conn "postgres://tqdbproxy:tqdbproxy@localhost:5432/testdb"

# View results
open adaptive_delay_benchmark.html
```

## Expected Output

```
===========================================
Write Batch Adaptive Delay Benchmark Suite
===========================================

=== Running benchmark: 1000 TPS ===
Results: Actual TPS=980, Avg Delay=0.52ms, Avg Batch=1.8

=== Running benchmark: 10000 TPS ===
Results: Actual TPS=9500, Avg Delay=2.3ms, Avg Batch=22.5

=== Running benchmark: 100000 TPS ===
Results: Actual TPS=92000, Avg Delay=14.8ms, Avg Batch=168.2

=== Running benchmark: 1000000 TPS ===
Results: Actual TPS=850000, Avg Delay=87.3ms, Avg Batch=892.5

=== Benchmark Results ===
...
```

## Next Steps

After completion, proceed to:

- [07-testing-validation.md](07-testing-validation.md) - Comprehensive testing
  and validation
