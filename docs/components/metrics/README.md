# Metrics Component

The `metrics` component is responsible for collecting and exposing Prometheus-compatible metrics for monitoring the proxy's performance and behavior.

## Implementation Details

- **Library**: Uses the official [Prometheus Go client](https://github.com/prometheus/client_golang).
- **Endpoint**: Exposes metrics at a configurable HTTP endpoint (defaulting to `:9090/metrics`).

## Tracked Metrics

- `tqdbproxy_query_total`: Total number of queries processed.
  - Labels: `file`, `line`, `query_type`, `cached`.
- `tqdbproxy_query_latency_seconds`: Histogram of query execution time.
  - Labels: `file`, `line`, `query_type`.
- `tqdbproxy_cache_hits_total`: Total number of successful cache lookups.
  - Labels: `file`, `line`.
- `tqdbproxy_cache_misses_total`: Total number of failed cache lookups.
  - Labels: `file`, `line`.
- `tqdbproxy_database_queries_total`: Total queries sent to the backend database.
  - Labels: `replica`.

[Back to Components](../README.md)
