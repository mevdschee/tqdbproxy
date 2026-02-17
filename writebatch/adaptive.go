package writebatch

import (
	"context"
	"time"

	"github.com/mevdschee/tqdbproxy/metrics"
)

// StartAdaptiveAdjustment runs the adaptive delay adjustment loop
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

// adjustDelay adjusts the batch delay based on current throughput
func (m *Manager) adjustDelay() {
	currentOps := m.opsPerSecond.Load()
	currentDelay := m.currentDelay.Load()

	// Update gauge metrics
	metrics.WriteOpsPerSecond.Set(float64(currentOps))
	metrics.WriteCurrentDelay.Set(float64(currentDelay) / 1000.0)

	threshold := uint64(m.config.WriteThreshold)

	if currentOps > threshold {
		// High write rate - increase delay to batch more
		newDelay := int64(float64(currentDelay) * m.config.AdaptiveStep)
		maxDelay := int64(m.config.MaxDelayMs * 1000) // to microseconds
		if newDelay > maxDelay {
			newDelay = maxDelay
		}
		if newDelay != currentDelay {
			m.currentDelay.Store(newDelay)
			metrics.WriteDelayAdjustments.WithLabelValues("increase").Inc()
		}
	} else if currentOps < threshold/2 && currentOps > 0 {
		// Low write rate - decrease delay for lower latency
		newDelay := int64(float64(currentDelay) / m.config.AdaptiveStep)
		minDelay := int64(m.config.MinDelayMs * 1000) // to microseconds
		if newDelay < minDelay {
			newDelay = minDelay
		}
		if newDelay != currentDelay {
			m.currentDelay.Store(newDelay)
			metrics.WriteDelayAdjustments.WithLabelValues("decrease").Inc()
		}
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
