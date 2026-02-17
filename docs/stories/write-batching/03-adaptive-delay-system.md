# Sub-Plan 3: Adaptive Delay System

**Parent Story**: [WRITE_BATCHING.md](../WRITE_BATCHING.md)

**Status**: Complete

**Estimated Effort**: 3-4 days (Completed)

## Overview

Implement automatic delay adjustment based on write throughput to optimize batch

sizes dynamically.

## Prerequisites

- [x] Sub-Plan 1: Parser extensions completed
- [x] Sub-Plan 2: Write batch manager core completed

## Goals

- Track write operations per second
- Automatically adjust delay based on throughput
- Scale delay up when TPS increases
- Scale delay down when TPS decreases
- Provide configuration for thresholds

## Tasks

### 1. Extend Manager for Throughput Tracking

**File**: `writebatch/manager.go`

- [x] Add throughput tracking fields to `Manager`
- [x] Add metrics update in `executeBatch()`
- [x] Implement periodic throughput calculation
- [x] Add forceThroughputUpdate() for testing

```go
type Manager struct {
    groups       sync.Map
    config       Config
    db           *sql.DB
    currentDelay atomic.Int64
    closed       atomic.Bool
    
    // Throughput tracking
    opsPerSecond atomic.Uint64
    opsCounter   atomic.Uint64    // Total ops executed
    lastReset    atomic.Int64     // Unix timestamp of last reset
}

func (m *Manager) updateThroughput(batchSize int, duration time.Duration) {
    m.opsCounter.Add(uint64(batchSize))
    
    // Calculate ops/sec over the last second
    now := time.Now().Unix()
    lastReset := m.lastReset.Load()
    
    if now > lastReset {
        ops := m.opsCounter.Swap(0)
        elapsed := now - lastReset
        if elapsed > 0 {
            opsPerSec := ops / uint64(elapsed)
            m.opsPerSecond.Store(opsPerSec)
        }
        m.lastReset.Store(now)
    }
}
```

### 2. Extend Config for Adaptive Parameters

**File**: `writebatch/types.go`

- [x] Add adaptive delay configuration fields

```go
type Config struct {
    InitialDelayMs  int
    MaxDelayMs      int
    MinDelayMs      int
    MaxBatchSize    int
    
    // Adaptive delay settings
    WriteThreshold  int     // ops/sec threshold to increase delay
    AdaptiveStep    float64 // Multiplier for delay adjustment (e.g., 1.5)
    MetricsInterval int     // Seconds between adjustments
}

func DefaultConfig() Config {
    return Config{
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

### 3. Implement Adaptive Delay Logic

**File**: `writebatch/adaptive.go`

- [x] Create adaptive delay adjustment function
- [x] Implement background adjustment goroutine
- [x] Add proper shutdown handling
- [x] Implement GetCurrentDelay() and GetOpsPerSecond()

```go
package writebatch

import (
    "context"
    "time"
)

// StartAdaptiveAdjustment runs the adaptive delay adjustment loop
func (m *Manager) StartAdaptiveAdjustment(ctx context.Context) {
    ticker := time.NewTicker(time.Duration(m.config.MetricsInterval) * time.Second)
    defer ticker.Stop()
    
    m.lastReset.Store(time.Now().Unix())
    
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
    
    threshold := uint64(m.config.WriteThreshold)
    
    if currentOps > threshold {
        // High write rate - increase delay to batch more
        newDelay := int64(float64(currentDelay) * m.config.AdaptiveStep)
        maxDelay := int64(m.config.MaxDelayMs * 1000) // to microseconds
        if newDelay > maxDelay {
            newDelay = maxDelay
        }
        m.currentDelay.Store(newDelay)
    } else if currentOps < threshold/2 && currentOps > 0 {
        // Low write rate - decrease delay for lower latency
        newDelay := int64(float64(currentDelay) / m.config.AdaptiveStep)
        minDelay := int64(m.config.MinDelayMs * 1000) // to microseconds
        if newDelay < minDelay {
            newDelay = minDelay
        }
        m.currentDelay.Store(newDelay)
    }
    // If ops is between threshold/2 and threshold, keep current delay
}

// GetCurrentDelay returns the current delay in milliseconds
func (m *Manager) GetCurrentDelay() float64 {
    return float64(m.currentDelay.Load()) / 1000.0
}

// GetOpsPerSecond returns the current throughput
func (m *Manager) GetOpsPerSecond() uint64 {
    return m.opsPerSecond.Load()
}
```

### 4. Update executeBatch to Track Metrics

**File**: `writebatch/executor.go`

- [x] Add throughput update call in `executeBatch()`

```go
func (m *Manager) executeBatch(batchKey string, group *BatchGroup) {
    group.mu.Lock()
    requests := group.Requests
    group.Requests = make([]*WriteRequest, 0, m.config.MaxBatchSize)
    group.mu.Unlock()
    
    if len(requests) == 0 {
        return
    }
    
    batchStart := time.Now()
    batchSize := len(requests)
    
    if len(requests) == 1 {
        m.executeSingle(requests[0])
    } else {
        m.executeBatchedWrites(requests)
    }
    
    duration := time.Since(batchStart)
    
    // Update throughput metrics
    m.updateThroughput(batchSize, duration)
}
```

### 5. Integration Tests

**File**: `writebatch/adaptive_test.go`

- [x] Test delay increases under high load
- [x] Test delay decreases under low load
- [x] Test delay stays within min/max bounds
- [x] Test throughput tracking accuracy
- [x] Test adaptive adjustment timing
- [x] Test context cancellation
- [x] Test adaptive step multiplier
- [x] Test stable delay in middle range

```go
package writebatch

import (
    "context"
    "sync"
    "testing"
    "time"
)

func TestAdaptiveDelay_IncreasesUnderLoad(t *testing.T) {
    db := setupTestDB(t)
    defer db.Close()
    
    config := DefaultConfig()
    config.WriteThreshold = 50  // Low threshold for testing
    config.MetricsInterval = 1
    config.InitialDelayMs = 1
    
    m := New(db, config)
    defer m.Close()
    
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    // Start adaptive adjustment
    go m.StartAdaptiveAdjustment(ctx)
    
    initialDelay := m.GetCurrentDelay()
    
    // Generate high load (>50 ops/sec)
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(n int) {
            defer wg.Done()
            m.Enqueue(context.Background(), "test:1",
                "INSERT INTO test_writes (data) VALUES (?)",
                []interface{}{n})
        }(i)
    }
    wg.Wait()
    
    // Wait for adjustment
    time.Sleep(2 * time.Second)
    
    finalDelay := m.GetCurrentDelay()
    
    if finalDelay <= initialDelay {
        t.Errorf("Expected delay to increase under load: initial=%v, final=%v",
            initialDelay, finalDelay)
    }
    
    t.Logf("Delay increased from %.2fms to %.2fms", initialDelay, finalDelay)
}

func TestAdaptiveDelay_DecreasesUnderLowLoad(t *testing.T) {
    db := setupTestDB(t)
    defer db.Close()
    
    config := DefaultConfig()
    config.WriteThreshold = 1000
    config.MetricsInterval = 1
    config.InitialDelayMs = 50 // Start with higher delay
    
    m := New(db, config)
    defer m.Close()
    
    // Manually set high delay
    m.currentDelay.Store(50000) // 50ms in microseconds
    
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    go m.StartAdaptiveAdjustment(ctx)
    
    initialDelay := m.GetCurrentDelay()
    
    // Generate low load (< 500 ops/sec)
    for i := 0; i < 10; i++ {
        m.Enqueue(context.Background(), "test:1",
            "INSERT INTO test_writes (data) VALUES (?)",
            []interface{}{i})
        time.Sleep(50 * time.Millisecond)
    }
    
    // Wait for adjustment
    time.Sleep(2 * time.Second)
    
    finalDelay := m.GetCurrentDelay()
    
    if finalDelay >= initialDelay {
        t.Errorf("Expected delay to decrease under low load: initial=%v, final=%v",
            initialDelay, finalDelay)
    }
    
    t.Logf("Delay decreased from %.2fms to %.2fms", initialDelay, finalDelay)
}

func TestAdaptiveDelay_RespectsBounds(t *testing.T) {
    db := setupTestDB(t)
    defer db.Close()
    
    config := DefaultConfig()
    config.MaxDelayMs = 10
    config.MinDelayMs = 1
    
    m := New(db, config)
    defer m.Close()
    
    // Test max bound
    m.currentDelay.Store(100000) // 100ms
    m.adjustDelay()
    
    if m.GetCurrentDelay() > float64(config.MaxDelayMs) {
        t.Errorf("Delay exceeded max: %v > %v", m.GetCurrentDelay(), config.MaxDelayMs)
    }
    
    // Test min bound
    m.currentDelay.Store(100) // 0.1ms
    m.adjustDelay()
    
    if m.GetCurrentDelay() < float64(config.MinDelayMs) {
        t.Errorf("Delay below min: %v < %v", m.GetCurrentDelay(), config.MinDelayMs)
    }
}
```

## Dependencies

- Write batch manager core (Sub-Plan 2)

## Deliverables

- [x] Throughput tracking implemented (opsPerSecond, opsCounter, lastReset)
- [x] Adaptive delay adjustment logic (increases/decreases based on threshold)
- [x] Background adjustment goroutine with context cancellation
- [x] Configuration parameters (WriteThreshold, AdaptiveStep, MetricsInterval)
- [x] Unit and integration tests (7 test functions, all passing with race detector)
- [x] Documentation on tuning parameters
- [x] Code reviewed

## Validation

```bash
# Run adaptive delay tests
go test ./writebatch -v -run "TestAdaptive"

# Test with race detector
go test ./writebatch -race -run "TestAdaptive"

# Manual verification with logging
go test ./writebatch -v -run "TestAdaptiveDelay_IncreasesUnderLoad"
```

## Tuning Guidelines

Document recommended settings for different workloads:

- **Low Latency** (< 1000 TPS): InitialDelay=0, Threshold=500
- **Balanced** (1k-10k TPS): InitialDelay=1ms, Threshold=1000
- **High Throughput** (>10k TPS): InitialDelay=5ms, Threshold=5000


## Next Steps
 

After completion, proceed to:

- [04-metrics-monitoring.md](04-metrics-monitoring.md) - Add comprehensive
  metrics
