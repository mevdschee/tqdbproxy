package replica

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// Pool manages a primary database and multiple read replicas
type Pool struct {
	primary  string
	replicas []string
	healthy  map[string]bool
	current  int // round-robin index
	mu       sync.RWMutex
}

// NewPool creates a new replica pool
func NewPool(primary string, replicas []string) *Pool {
	p := &Pool{
		primary:  primary,
		replicas: replicas,
		healthy:  make(map[string]bool),
		current:  0,
	}

	// Initially mark all replicas as healthy
	for _, replica := range replicas {
		p.healthy[replica] = true
	}

	return p
}

// UpdateReplicas updates the replica list for hot config reload.
// Existing healthy replicas keep their health status.
func (p *Pool) UpdateReplicas(primary string, replicas []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.primary = primary

	// Build new healthy map, preserving status of existing replicas
	newHealthy := make(map[string]bool)
	for _, r := range replicas {
		if status, exists := p.healthy[r]; exists {
			newHealthy[r] = status
		} else {
			newHealthy[r] = true // New replicas start as healthy
		}
	}

	p.replicas = replicas
	p.healthy = newHealthy

	// Reset round-robin index if it's now out of bounds
	if len(replicas) > 0 {
		p.current = p.current % len(replicas)
	} else {
		p.current = 0
	}
}

// GetPrimary returns the primary database address
func (p *Pool) GetPrimary() string {
	return p.primary
}

// GetReplica returns the next healthy replica using round-robin,
// or the primary if no replicas are healthy. It returns (address, name).
func (p *Pool) GetReplica() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.replicas) == 0 {
		return p.primary, "primary"
	}

	// Try to find a healthy replica
	attempts := 0
	for attempts < len(p.replicas) {
		idx := p.current
		replica := p.replicas[idx]
		p.current = (p.current + 1) % len(p.replicas)
		attempts++

		if p.healthy[replica] {
			return replica, fmt.Sprintf("replica%d", idx+1)
		}
	}

	// No healthy replicas, fall back to primary
	log.Printf("[Replica] No healthy replicas available, using primary")
	return p.primary, "primary"
}

// MarkUnhealthy marks a replica as unhealthy
func (p *Pool) MarkUnhealthy(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.healthy[addr]; exists {
		p.healthy[addr] = false
		log.Printf("[Replica] Marked %s as unhealthy", addr)
	}
}

// MarkHealthy marks a replica as healthy
func (p *Pool) MarkHealthy(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.healthy[addr]; exists {
		wasUnhealthy := !p.healthy[addr]
		p.healthy[addr] = true
		if wasUnhealthy {
			log.Printf("[Replica] Marked %s as healthy", addr)
		}
	}
}

// IsHealthy returns whether a replica is healthy
func (p *Pool) IsHealthy(addr string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.healthy[addr]
}

// GetHealthyCount returns the number of healthy replicas
func (p *Pool) GetHealthyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, healthy := range p.healthy {
		if healthy {
			count++
		}
	}
	return count
}

// StartHealthChecks begins periodic health checks for all replicas
func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run initial health check immediately
	p.checkAllReplicas()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.checkAllReplicas()
		}
	}
}

func (p *Pool) checkAllReplicas() {
	for _, replica := range p.replicas {
		go p.checkReplica(replica)
	}
}

func (p *Pool) checkReplica(addr string) {
	network := "tcp"
	dialAddr := addr
	if len(addr) > 5 && addr[:5] == "unix:" {
		network = "unix"
		dialAddr = addr[5:]
	}

	// Simple connection check
	conn, err := net.DialTimeout(network, dialAddr, 2*time.Second)
	if err != nil {
		p.MarkUnhealthy(addr)
		return
	}
	conn.Close()
	p.MarkHealthy(addr)
}
