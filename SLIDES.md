# TQDBProxy

### High-Performance Proxy for MariaDB and PostgreSQL 

---

## What is TQDBProxy?

- Transparent proxy for **MariaDB** and **PostgreSQL**
- Caches results for **Time To Live** seconds
- Provides **single-flight** for cold and warm cache
- Routes cacheable queries to **read replicas**
- Metrics with query **source file + line number**

---

## Architecture

```
┌─────────────┐     ┌─────────────┐
│  MariaDB    │     │ PostgreSQL  │
│   Client    │     │   Client    │
└──────┬──────┘     └──────┬──────┘
       │                   │
       └─────────┬─────────┘
                 │
          ┌──────▼──────┐
          │  TQDBProxy  │
          │  ┌───────┐  │
          │  │ Cache │  │
          │  └───────┘  │
          └──────┬──────┘
                 │
       ┌─────────┴─────────┐
       │                   │
┌──────▼──────┐     ┌──────▼──────┐
│   Primary   │     │  Replicas   │
│   Database  │     │  1, 2, ...  │
└─────────────┘     └─────────────┘
```

---

## System Components

- **Cache** — In-memory caching with TQMemory
- **Metrics** — Prometheus-compatible metrics
- **MariaDB** — Wire protocol implementation
- **PostgreSQL** — Wire protocol implementation  
- **Parser** — SQL hint extraction
- **Replica** — Connection pool management

---

## Caching with TTL Hints

Add metadata comments to your SQL queries:

```sql
/* ttl:60 file:app.php line:42 */ 
SELECT * FROM users WHERE active = 1
```

- `ttl`: Cache duration in seconds
- `file`: Source file of the query
- `line`: Line number of the query

---

## Thundering Herd Protection

| Flag | Meaning |
|------|---------|
| 0 | Value is **fresh** |
| 1 | Value is **stale**, refresh in progress |
| 3 | First stale access — **caller should refresh** |

**Cold Cache**: Single-flight prevents concurrent DB queries for the same key

**Warm Cache**: Stale data served while one goroutine refreshes in background

---

## Read Replica Routing

Configure replicas in `config.ini`:

```ini
[mariadb]
listen = :3307
primary = 127.0.0.1:3306
replica1 = 127.0.0.2:3306
replica2 = 127.0.0.3:3306
```

- SELECT with `ttl > 0` → Round-robin across replicas
- Writes, DDL, Transactions → Always to primary
- Automatic failover if replicas unavailable

---

## Transaction Support

Full ACID transaction support:

```sql
BEGIN;
UPDATE accounts SET balance = balance - 100 WHERE id = 1;
UPDATE accounts SET balance = balance + 100 WHERE id = 2;
COMMIT;
```

- **SELECT** — With optional caching
- **INSERT** — Returns last insert ID
- **UPDATE** — Returns affected rows
- **DELETE** — Returns affected rows

---

## Query Status Inspection

**MariaDB:**
```sql
SHOW TQDB STATUS;
```

**PostgreSQL:**
```sql
SELECT * FROM pg_tqdb_status;
```

Returns `Backend` (primary/replicaN/cache/none) and `Cache_hit` (0/1)

---

## Client Libraries

Wrapper libraries for automatic hint injection:

| Language | MariaDB | PostgreSQL |
|----------|---------|------------|
| Go | ✓ | ✓ |
| PHP | ✓ | ✓ |
| TypeScript | ✓ | ✓ |

```go
// Standard query
rows, err := db.Query("SELECT * FROM users")

// Query with 60-second caching
rows, err := db.QueryWithTTL(60, "SELECT * FROM users")
```

---

## Prometheus Metrics

Exposed at `http://localhost:9090/metrics`:

- `tqdbproxy_query_total` — Queries by type, file, line
- `tqdbproxy_query_latency_seconds` — Execution time histogram
- `tqdbproxy_cache_hits_total` — Cache hits
- `tqdbproxy_cache_misses_total` — Cache misses
- `tqdbproxy_database_queries_total` — Backend queries

---

## Configuration

```ini
[mariadb]
listen = :3307
socket = /var/run/tqdbproxy/mysql.sock
primary = 127.0.0.1:3306

[postgres]
listen = :5433
socket = /var/run/tqdbproxy/.s.PGSQL.5433
primary = 127.0.0.1:5432
```

**Hot reload** via `SIGHUP`:
```bash
kill -SIGHUP $(pidof tqdbproxy)
```

---

## Performance

![Benchmark](benchmarks/proxy/proxy_benchmark.png)

- Cache hits as fast as empty queries
- Minimal proxy overhead for queries ≥1ms
- Run multiple proxies, one per application server

---

## Future Ideas

### ACID-Compliant Dual Writes

- Write to two databases simultaneously using XA transactions
- Two-phase commit ensures atomicity across both databases
- Enables zero-downtime migrations and geographic redundancy
- Latency = max(primary, secondary), not sum

### Database Sharding

- You may support multiple primaries
- Each primary can handle a subset of databases
- The proxy will route queries to the right primary
- You can use the same client libraries as before

---

## Quick Start

```bash
# Start the proxy
./tqdbproxy

# Connect via MariaDB
mariadb -u tqdbproxy -p -P 3307 --comments

# Connect via PostgreSQL  
psql -h 127.0.0.1 -p 5433 -U tqdbproxy
```

---

## Links

- Blog: https://www.tqdev.com/2026-tqdbproxy-mariadb-postgresql-proxy/
- Documentation: [docs/README.md](docs/README.md)
- Cache library: [TQMemory](https://github.com/mevdschee/tqmemory)

---

# Thank You

**TQDBProxy** — Fast, Observable, Reliable
