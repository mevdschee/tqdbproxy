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
	})
}

// Handler returns the Prometheus HTTP handler
func Handler() http.Handler {
	return promhttp.Handler()
}
