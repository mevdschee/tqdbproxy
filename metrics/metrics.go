package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// QueryTotal counts total queries by file, line, query_type, cached
	QueryTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tqdbproxy_query_total",
			Help: "Total number of queries processed",
		},
		[]string{"file", "line", "query_type", "cached"},
	)

	// QueryLatency tracks query latency by file, line, query_type
	QueryLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tqdbproxy_query_latency_seconds",
			Help:    "Query latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"file", "line", "query_type"},
	)

	// CacheHits counts cache hits by file, line
	CacheHits = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tqdbproxy_cache_hits_total",
			Help: "Total number of cache hits",
		},
		[]string{"file", "line"},
	)

	// CacheMisses counts cache misses by file, line
	CacheMisses = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tqdbproxy_cache_misses_total",
			Help: "Total number of cache misses",
		},
		[]string{"file", "line"},
	)

	// DatabaseQueries counts queries sent to database by replica
	DatabaseQueries = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tqdbproxy_database_queries_total",
			Help: "Total queries sent to database",
		},
		[]string{"replica"},
	)

	// Write Batch Metrics

	// WriteBatchSize tracks the number of operations in each write batch
	WriteBatchSize = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tqdbproxy_write_batch_size",
			Help:    "Number of operations in each write batch",
			Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000},
		},
		[]string{"query"},
	)

	// WriteBatchDelay tracks time between first enqueue and execution
	WriteBatchDelay = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tqdbproxy_write_batch_delay_seconds",
			Help:    "Time between first operation enqueue and batch execution",
			Buckets: []float64{0.00001, 0.0001, 0.001, 0.01, 0.05, 0.1, 0.5, 1.0},
		},
		[]string{"query"},
	)

	// WriteBatchLatency tracks time to execute a batch
	WriteBatchLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tqdbproxy_write_batch_latency_seconds",
			Help:    "Time to execute a write batch",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"query"},
	)

	// WriteOpsPerSecond is the current write operations per second
	WriteOpsPerSecond = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "tqdbproxy_write_ops_per_second",
			Help: "Current write operations per second (for adaptive delay)",
		},
	)

	// WriteCurrentDelay is the current adaptive batching delay
	WriteCurrentDelay = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "tqdbproxy_write_current_delay_ms",
			Help: "Current adaptive batching delay in milliseconds",
		},
	)

	// WriteDelayAdjustments counts delay adjustments
	WriteDelayAdjustments = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tqdbproxy_write_delay_adjustments_total",
			Help: "Number of delay adjustments (increase/decrease)",
		},
		[]string{"direction"},
	)

	// WriteBatchedTotal counts write operations processed through batching
	WriteBatchedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tqdbproxy_write_batched_total",
			Help: "Total write operations processed through batching",
		},
		[]string{"query_type"},
	)

	once sync.Once
)

// Init registers all metrics with Prometheus
func Init() {
	once.Do(func() {
		prometheus.MustRegister(QueryTotal)
		prometheus.MustRegister(QueryLatency)
		prometheus.MustRegister(CacheHits)
		prometheus.MustRegister(CacheMisses)
		prometheus.MustRegister(DatabaseQueries)

		// Write batch metrics
		prometheus.MustRegister(WriteBatchSize)
		prometheus.MustRegister(WriteBatchDelay)
		prometheus.MustRegister(WriteBatchLatency)
		prometheus.MustRegister(WriteOpsPerSecond)
		prometheus.MustRegister(WriteCurrentDelay)
		prometheus.MustRegister(WriteDelayAdjustments)
		prometheus.MustRegister(WriteBatchedTotal)
	})
}

// Handler returns the Prometheus HTTP handler
func Handler() http.Handler {
	return promhttp.Handler()
}
