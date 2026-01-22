# Phase 3: Observability & Metrics

## Goal

Implement comprehensive metrics collection with Prometheus-compatible endpoint for monitoring database access patterns.

## Deliverables

### 3.1 Caller Metadata Extraction
- Extract file and line number hints from SQL comments: `/* file:user.go line:42 */`
- Associate each query with its call site
- Track unknown/missing metadata gracefully

### 3.2 Metrics Collection
Collect metrics per dimensions:
- **File + Line Number**: identify exact call site
- **Replica**: primary, replica1, replica2, etc.
- **Query Type**: insert, update, delete, select

Metrics to collect:
- `tqdbproxy_query_total` - Total query count
- `tqdbproxy_query_latency_seconds` - Query latency histogram
- `tqdbproxy_cache_hits_total` - Cache hit count
- `tqdbproxy_cache_misses_total` - Cache miss count
- `tqdbproxy_database_queries_total` - Queries sent to database (by replica)

### 3.3 Prometheus Endpoint
- HTTP endpoint at `/metrics`
- Standard Prometheus text format
- Include Go runtime metrics

### 3.4 Query Logging (Optional)
- Log slow queries above configurable threshold
- Structured JSON logging
- Configurable log level

## Success Criteria

- [ ] All queries produce metrics with correct labels
- [ ] `/metrics` endpoint returns valid Prometheus format
- [ ] Grafana dashboard can visualize query patterns
- [ ] Cache hit ratio is accurately tracked

## Estimated Effort

1 week
