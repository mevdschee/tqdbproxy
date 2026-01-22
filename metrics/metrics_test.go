package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetrics_Init(t *testing.T) {
	// Init should not panic when called multiple times
	Init()
	Init()
}

func TestMetrics_Handler(t *testing.T) {
	Init()

	// Create a test request to /metrics
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()

	// Check that our custom metrics are registered
	expectedMetrics := []string{
		"tqdbproxy_query_total",
		"tqdbproxy_query_latency_seconds",
		"tqdbproxy_cache_hits_total",
		"tqdbproxy_cache_misses_total",
		"tqdbproxy_database_queries_total",
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(body, metric) {
			t.Errorf("Expected metric %q not found in response", metric)
		}
	}
}

func TestMetrics_Increment(t *testing.T) {
	Init()

	// Test incrementing counters
	QueryTotal.WithLabelValues("test.go", "42", "select", "false").Inc()
	CacheHits.WithLabelValues("test.go", "42").Inc()
	CacheMisses.WithLabelValues("test.go", "42").Inc()
	DatabaseQueries.WithLabelValues("primary").Inc()

	// Test observing histogram
	QueryLatency.WithLabelValues("test.go", "42", "select").Observe(0.001)

	// Verify by checking /metrics output
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `file="test.go"`) {
		t.Error("Expected label file=test.go in output")
	}
}
