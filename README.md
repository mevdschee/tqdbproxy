# tqdbproxy

TQDBProxy acts as a dedicated proxy that handles caching, metrics, and query instrumentation centrally. It understands both MySQL and PostgreSQL wire protocols, intercepts queries, extracts cache-TTL hints, normalizes SQL, and serves results from a fast Otter cache or forwards to the underlying primary database or read replicas.

### Key Features
- **Protocol Support**: Native understanding of MySQL and PostgreSQL (currently transparent) protocols.
- **Intelligent Caching**: Serves results from Otter cache based on SQL hints (e.g., `/* ttl:60 */`).
- **Replica Support**: Routes cacheable SELECT queries to read replicas for horizontal scaling.
- **Observability**: Rich Prometheus metrics collected per file and line number.

## Benefits

- **Zero-friction adoption**: Drop-in replacement for existing drivers
- **Centralized caching**: Reduces database load across all services
- **Unified observability**: Consistent instrumentation across the entire stack
- **Language-agnostic**: Same behavior and metrics regardless of client language
- **Measurable & optimizable**: Makes database access transparent and tunable

## Documentation

See [docs/README.md](docs/README.md) for more information.