package mariadb

import (
	"strings"
	"testing"

	"github.com/mevdschee/tqdbproxy/replica"
)

func TestHealthRoundRobin(t *testing.T) {
	// Setup Pool with 3 replicas, one unhealthy
	replicas := []string{"127.0.0.1:3342", "127.0.0.1:3343", "127.0.0.1:3344"}
	pool := replica.NewPool("127.0.0.1:3341", replicas)

	// Mark replica3 as unhealthy
	pool.MarkUnhealthy("127.0.0.1:3344")

	// Track which replicas are selected
	results := make(map[string]int)

	// Execute 6 queries worth of replica selection
	for i := 0; i < 6; i++ {
		_, name := pool.GetReplica()
		results[name]++
	}

	// Verify unhealthy replica is never used
	for name := range results {
		if strings.Contains(name, "replicas[2]") {
			t.Errorf("Unhealthy replicas[2] was used")
		}
	}

	// Verify round-robin between healthy replicas
	if results["replicas[0]"] == 0 {
		t.Errorf("replicas[0] was never used")
	}
	if results["replicas[1]"] == 0 {
		t.Errorf("replicas[1] was never used")
	}

	// Should be roughly equal distribution (3 each for 6 queries between 2 replicas)
	if results["replicas[0]"] < 2 || results["replicas[1]"] < 2 {
		t.Errorf("Round-robin distribution uneven: replicas[0]=%d, replicas[1]=%d", results["replicas[0]"], results["replicas[1]"])
	}

	t.Logf("Round-robin results: %v", results)
}
