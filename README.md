# TQDBProxy

A high-performance MariaDB and PostgreSQL proxy with caching, read replica,
metrics, and transaction support.

Blog post: https://www.tqdev.com/2026-tqdbproxy-mariadb-postgresql-proxy/

## Features

- **Query Caching**: Cache SELECT queries with configurable TTL and thundering
  herd protection
- **Write Batching**: Automatically batch write operations with hint-based
  control
- **Caller Metadata**: Track queries by source file and line number
- **Metrics**: Prometheus metrics for cache hits/misses, query latency, and more
- **Read Replica Support**: Automatic routing of SELECT queries to replicas
- **Database Sharding**: Automatic routing of queries to shards based on
  database name
- **Transaction Support**: Full BEGIN/COMMIT/ROLLBACK support
- **Interactive Mode**: Full interactive client support

## Performance

![TQDBProxy Benchmark](benchmarks/proxy/proxy_benchmark.png)

Cache hits are as fast as empty queries with 100 connections. Proxy overhead is
minimal for queries ≥1ms.

## Quick Start

```bash
# Start the proxy
./tqdbproxy

# Connect via MariaDB client (interactive mode)
mariadb -u tqdbproxy -p -P 3307 tqdbproxy --comments
```

## Using Metadata Comments

Add caller metadata and hints to your queries for better observability and
performance:

```sql
-- Caching hint (SELECT queries)
/* ttl:60 file:app.php line:42 */ SELECT * FROM users WHERE active = 1

-- Batching hint (write operations)
/* batch:10 file:logger.php line:15 */ INSERT INTO logs (message) VALUES ('Event occurred')
```

**Available Hints:**

- `ttl:N` - Cache result for N seconds (SELECT queries only)
- `batch:N` - Wait up to N milliseconds to batch writes (INSERT/UPDATE/DELETE)
- `file:X` - Source file name (for metrics/debugging)
- `line:N` - Source line number (for metrics/debugging)

NB: When using the MariaDB CLI, you **must** use the `--comments` flag to
preserve metadata comments

## Write Batching

Improve write throughput by batching operations together using the `batch:N`
hint:

```sql
-- Low latency batching (1ms window)
/* batch:1 */ INSERT INTO logs (level, message) VALUES ('INFO', 'Request processed')

-- Moderate batching (10ms window) - good balance
/* batch:10 */ INSERT INTO events (type, data) VALUES ('click', '{"x":100,"y":200}')

-- High throughput batching (100ms window)
/* batch:100 */ INSERT INTO analytics (metric, value) VALUES ('page_view', 1)
```

**How it works:**

1. Operations with the same query structure are automatically grouped
2. Batch executes when timer expires OR 1000 operations collected
3. Each operation receives its individual result
4. Batching is disabled inside transactions

**Performance gains:**

- `batch:1` → 5-10x throughput improvement
- `batch:10` → 50-100x throughput improvement
- `batch:100` → 500-1000x throughput improvement

See [Write Batching Documentation](docs/components/writebatch/README.md) for
details.

## Transaction Support

Full transaction support with proper isolation:

```sql
BEGIN;
UPDATE accounts SET balance = balance - 100 WHERE id = 1;
UPDATE accounts SET balance = balance + 100 WHERE id = 2;
COMMIT;
```

All query types supported:

- SELECT queries (with optional caching)
- INSERT queries (returns last insert ID)
- UPDATE queries (returns affected rows)
- DELETE queries (returns affected rows)

## Query Status Inspection

Check which backend served the last query:

**MariaDB:**

```sql
mariadb> SHOW TQDB STATUS;
+---------------+---------+
| Variable_name | Value   |
+---------------+---------+
| Shard         | main    |
| Backend       | primary |
+---------------+---------+
```

**PostgreSQL:**

```sql
tqdbproxy=> SELECT * FROM pg_tqdb_status;
 variable_name |  value  
---------------+---------
 Shard         | main
 Backend       | primary
(2 rows)
```

Values: `Backend` = `primary`, `replicas[n]`, `cache`, `cache (stale)` or `none`
(no query yet).

This is useful for debugging cache behavior during development.

## Sharding & Replicas

Configure backends and database mappings in `config.ini`:

```ini
[mariadb]
listen = :3307
default = main

[mariadb.main]
primary = 127.0.0.1:3306
replicas = 127.0.0.1:3307, 127.0.0.1:3308

[mariadb.shard1]
primary = 10.0.0.1:3309
databases = logs
```

### Sharding Constraints

PostgreSQL routing is based on the **database name** provided during connection
and (schema sharding is not supported) and this aligns 1:1 with the MariaDB
implementation. Note that it is the responsibility of the user to keep the
database usernames and passwords in sync across shards as the proxy forwards the
credentials to the backend.

## Thundering Herd Protection

The proxy implements single-flight for cold cache misses and stale cache
refreshes to prevent backend saturation.

## Metrics

Access Prometheus metrics at `http://localhost:9090/metrics`:

```bash
curl http://localhost:9090/metrics | grep tqdbproxy_query_total
```

Metrics include file and line labels when metadata comments are used.

## Cluster Setup

DNS round-robin load balancing can be used to distribute queries across multiple
proxies.

## Documentation

See [docs/README.md](docs/README.md) for more information.
