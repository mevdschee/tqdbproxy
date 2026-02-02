# TQDBProxy

A high-performance MariaDB and PostgreSQL proxy with caching, read replica, metrics, and transaction support.

Blog post: https://www.tqdev.com/2026-tqdbproxy-mariadb-postgresql-proxy/

## Features

- **Query Caching**: Cache SELECT queries with configurable TTL and thundering herd protection
- **Caller Metadata**: Track queries by source file and line number
- **Metrics**: Prometheus metrics for cache hits/misses, query latency, and more
- **Read Replica Support**: Automatic routing of SELECT queries to replicas
- **Database Sharding**: Automatic routing of queries to shards based on database name
- **Transaction Support**: Full BEGIN/COMMIT/ROLLBACK support
- **Interactive Mode**: Full interactive client support

## Performance

![TQDBProxy Benchmark](benchmarks/proxy/proxy_benchmark.png)

Cache hits are as fast as empty queries with 100 connections. Proxy overhead is minimal for queries ≥1ms.

## Quick Start

```bash
# Start the proxy
./tqdbproxy

# Connect via MariaDB client (interactive mode)
mariadb -u tqdbproxy -p -P 3307 tqdbproxy --comments
```

## Using Metadata Comments

Add caller metadata to your queries for better observability:

```sql
/* ttl:60 file:app.php line:42 */ SELECT * FROM users WHERE active = 1
```

NB: When using the MariaDB CLI, you **must** use the `--comments` flag to preserve metadata comments

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

Backend = primary, replicas[n], cache, cache (stale)

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

PostgreSQL routing is based on the **database name** provided during connection and (schema sharding is not supported) and this aligns 1:1 with the MariaDB implementation. Note that it is the responsibility of the user to keep the database usernames and passwords in sync across shards as the proxy forwards the credentials to the backend.

## Thundering Herd Protection

The proxy implements single-flight for warmup and resfresh to prevent concurrent DB queries for the same key.

## Metrics

Access Prometheus metrics at `http://localhost:9090/metrics`:

```bash
curl http://localhost:9090/metrics | grep tqdbproxy_query_total
```

Metrics include file and line labels when metadata comments are used.

## Cluster Setup

DNS round-robin load balancing can be used to distribute queries across multiple proxies.

## Documentation

See [docs/README.md](docs/README.md) for more information.
