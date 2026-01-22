package replica

import (
	"context"
	"testing"
	"time"
)

func TestNewPool(t *testing.T) {
	primary := "localhost:3306"
	replicas := []string{"localhost:3307", "localhost:3308"}

	pool := NewPool(primary, replicas)

	if pool.GetPrimary() != primary {
		t.Errorf("Expected primary %s, got %s", primary, pool.GetPrimary())
	}

	if len(pool.replicas) != 2 {
		t.Errorf("Expected 2 replicas, got %d", len(pool.replicas))
	}

	// All replicas should be initially healthy
	if pool.GetHealthyCount() != 2 {
		t.Errorf("Expected 2 healthy replicas, got %d", pool.GetHealthyCount())
	}
}

func TestGetReplicaRoundRobin(t *testing.T) {
	primary := "localhost:3306"
	replicas := []string{"localhost:3307", "localhost:3308", "localhost:3309"}

	pool := NewPool(primary, replicas)

	// Should cycle through replicas in order
	first := pool.GetReplica()
	second := pool.GetReplica()
	third := pool.GetReplica()
	fourth := pool.GetReplica() // Should wrap back to first

	if first == second || second == third {
		t.Error("Round-robin not working: got duplicate replicas in sequence")
	}

	if first != fourth {
		t.Errorf("Round-robin wrap failed: first=%s, fourth=%s", first, fourth)
	}
}

func TestGetReplicaWithUnhealthy(t *testing.T) {
	primary := "localhost:3306"
	replicas := []string{"localhost:3307", "localhost:3308"}

	pool := NewPool(primary, replicas)

	// Mark first replica as unhealthy
	pool.MarkUnhealthy(replicas[0])

	// Should only return the healthy replica
	for i := 0; i < 5; i++ {
		replica := pool.GetReplica()
		if replica == replicas[0] {
			t.Errorf("Got unhealthy replica: %s", replica)
		}
	}
}

func TestGetReplicaAllUnhealthy(t *testing.T) {
	primary := "localhost:3306"
	replicas := []string{"localhost:3307", "localhost:3308"}

	pool := NewPool(primary, replicas)

	// Mark all replicas as unhealthy
	pool.MarkUnhealthy(replicas[0])
	pool.MarkUnhealthy(replicas[1])

	// Should fall back to primary
	replica := pool.GetReplica()
	if replica != primary {
		t.Errorf("Expected primary %s when all replicas unhealthy, got %s", primary, replica)
	}
}

func TestGetReplicaNoReplicas(t *testing.T) {
	primary := "localhost:3306"
	replicas := []string{}

	pool := NewPool(primary, replicas)

	// Should return primary when no replicas configured
	replica := pool.GetReplica()
	if replica != primary {
		t.Errorf("Expected primary %s when no replicas, got %s", primary, replica)
	}
}

func TestMarkHealthy(t *testing.T) {
	primary := "localhost:3306"
	replicas := []string{"localhost:3307"}

	pool := NewPool(primary, replicas)

	// Mark as unhealthy
	pool.MarkUnhealthy(replicas[0])
	if pool.IsHealthy(replicas[0]) {
		t.Error("Replica should be unhealthy")
	}

	// Mark as healthy again
	pool.MarkHealthy(replicas[0])
	if !pool.IsHealthy(replicas[0]) {
		t.Error("Replica should be healthy")
	}
}

func TestHealthCheckContext(t *testing.T) {
	primary := "localhost:3306"
	replicas := []string{"localhost:3307"}

	pool := NewPool(primary, replicas)

	ctx, cancel := context.WithCancel(context.Background())

	// Start health checks in background
	done := make(chan bool)
	go func() {
		pool.StartHealthChecks(ctx, 100*time.Millisecond)
		done <- true
	}()

	// Wait a bit
	time.Sleep(150 * time.Millisecond)

	// Cancel context
	cancel()

	// Should exit quickly
	select {
	case <-done:
		// Success
	case <-time.After(500 * time.Millisecond):
		t.Error("Health check goroutine did not exit after context cancellation")
	}
}

func TestGetHealthyCount(t *testing.T) {
	primary := "localhost:3306"
	replicas := []string{"localhost:3307", "localhost:3308", "localhost:3309"}

	pool := NewPool(primary, replicas)

	if pool.GetHealthyCount() != 3 {
		t.Errorf("Expected 3 healthy replicas, got %d", pool.GetHealthyCount())
	}

	pool.MarkUnhealthy(replicas[0])
	if pool.GetHealthyCount() != 2 {
		t.Errorf("Expected 2 healthy replicas, got %d", pool.GetHealthyCount())
	}

	pool.MarkUnhealthy(replicas[1])
	pool.MarkUnhealthy(replicas[2])
	if pool.GetHealthyCount() != 0 {
		t.Errorf("Expected 0 healthy replicas, got %d", pool.GetHealthyCount())
	}
}
