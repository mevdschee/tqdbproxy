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
	config.WriteThreshold = 50 // Low threshold for testing
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
			m.Enqueue(context.Background(), "test:adaptive",
				"INSERT INTO test_writes (data) VALUES (?)",
				[]interface{}{n})
		}(i)
	}
	wg.Wait()

	// Force throughput update for test
	m.forceThroughputUpdate()

	// Wait for adjustment
	time.Sleep(2 * time.Second)

	finalDelay := m.GetCurrentDelay()

	if finalDelay <= initialDelay {
		t.Errorf("Expected delay to increase under load: initial=%.2fms, final=%.2fms",
			initialDelay, finalDelay)
	}

	t.Logf("Delay increased from %.2fms to %.2fms under high load", initialDelay, finalDelay)
	t.Logf("Throughput: %d ops/sec", m.GetOpsPerSecond())
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
		m.Enqueue(context.Background(), "test:adaptive",
			"INSERT INTO test_writes (data) VALUES (?)",
			[]interface{}{i})
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for adjustment
	time.Sleep(2 * time.Second)

	finalDelay := m.GetCurrentDelay()

	if finalDelay >= initialDelay {
		t.Errorf("Expected delay to decrease under low load: initial=%.2fms, final=%.2fms",
			initialDelay, finalDelay)
	}

	t.Logf("Delay decreased from %.2fms to %.2fms under low load", initialDelay, finalDelay)
	t.Logf("Throughput: %d ops/sec", m.GetOpsPerSecond())
}

func TestAdaptiveDelay_RespectsBounds(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	config := DefaultConfig()
	config.MaxDelayMs = 10
	config.MinDelayMs = 1
	config.WriteThreshold = 100

	m := New(db, config)
	defer m.Close()

	// Simulate high ops to trigger increase
	m.opsPerSecond.Store(200)

	// Test max bound
	m.currentDelay.Store(9000) // 9ms, should increase but cap at 10ms
	m.adjustDelay()

	delay := m.GetCurrentDelay()
	if delay > float64(config.MaxDelayMs) {
		t.Errorf("Delay exceeded max: %.2fms > %dms", delay, config.MaxDelayMs)
	}
	t.Logf("Delay capped at max: %.2fms (max: %dms)", delay, config.MaxDelayMs)

	// Simulate low ops to trigger decrease
	m.opsPerSecond.Store(10)

	// Test min bound
	m.currentDelay.Store(1500) // 1.5ms, should decrease but cap at 1ms
	m.adjustDelay()

	delay = m.GetCurrentDelay()
	if delay < float64(config.MinDelayMs) {
		t.Errorf("Delay below min: %.2fms < %dms", delay, config.MinDelayMs)
	}
	t.Logf("Delay capped at min: %.2fms (min: %dms)", delay, config.MinDelayMs)
}

func TestAdaptiveDelay_StableInMiddleRange(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	config := DefaultConfig()
	config.WriteThreshold = 100
	config.MetricsInterval = 1

	m := New(db, config)
	defer m.Close()

	initialDelay := int64(5000) // 5ms
	m.currentDelay.Store(initialDelay)

	// Simulate ops in the middle range (between threshold/2 and threshold)
	m.opsPerSecond.Store(75) // Between 50 and 100

	m.adjustDelay()

	finalDelay := m.currentDelay.Load()

	if finalDelay != initialDelay {
		t.Errorf("Expected delay to remain stable in middle range: initial=%d, final=%d",
			initialDelay, finalDelay)
	}

	t.Logf("Delay remained stable: %.2fms (ops/sec: %d, threshold: %d)",
		m.GetCurrentDelay(), m.GetOpsPerSecond(), config.WriteThreshold)
}

func TestAdaptiveDelay_ThroughputTracking(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	config := DefaultConfig()
	config.InitialDelayMs = 1

	m := New(db, config)
	defer m.Close()

	// Execute some operations
	for i := 0; i < 50; i++ {
		m.Enqueue(context.Background(), "test:throughput",
			"INSERT INTO test_writes (data) VALUES (?)",
			[]interface{}{i})
	}

	// Force throughput update and give time for measurement
	m.forceThroughputUpdate()
	time.Sleep(100 * time.Millisecond)

	ops := m.GetOpsPerSecond()

	// We executed 50 ops in ~1 second, should be around 50
	if ops == 0 {
		t.Error("Expected non-zero throughput")
	}

	t.Logf("Measured throughput: %d ops/sec (executed 50 ops)", ops)
}

func TestAdaptiveDelay_ContextCancellation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	m := New(db, DefaultConfig())
	defer m.Close()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan bool)
	go func() {
		m.StartAdaptiveAdjustment(ctx)
		done <- true
	}()

	// Let it run briefly
	time.Sleep(500 * time.Millisecond)

	// Cancel and verify it stops
	cancel()

	select {
	case <-done:
		t.Log("Adaptive adjustment stopped successfully on context cancellation")
	case <-time.After(2 * time.Second):
		t.Error("Adaptive adjustment did not stop after context cancellation")
	}
}

func TestAdaptiveDelay_AdaptiveStepMultiplier(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	config := DefaultConfig()
	config.WriteThreshold = 100
	config.AdaptiveStep = 2.0 // Double/halve each adjustment

	m := New(db, config)
	defer m.Close()

	initialDelay := int64(4000) // 4ms
	m.currentDelay.Store(initialDelay)

	// Trigger increase
	m.opsPerSecond.Store(200)
	m.adjustDelay()

	expectedIncrease := int64(float64(initialDelay) * 2.0)
	if m.currentDelay.Load() != expectedIncrease {
		t.Errorf("Expected delay to double: got %d, want %d",
			m.currentDelay.Load(), expectedIncrease)
	}

	// Trigger decrease
	m.opsPerSecond.Store(10)
	m.adjustDelay()

	expectedDecrease := int64(float64(expectedIncrease) / 2.0)
	if m.currentDelay.Load() != expectedDecrease {
		t.Errorf("Expected delay to halve: got %d, want %d",
			m.currentDelay.Load(), expectedDecrease)
	}

	t.Logf("Delay adjustment with step=%.1f: %dμs → %dμs → %dμs",
		config.AdaptiveStep, initialDelay, expectedIncrease, expectedDecrease)
}
