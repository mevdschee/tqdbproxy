# TQDBProxy

---

**High-performance MariaDB and PostgreSQL proxy**

- **Caching**: Unified query caching with TQMemory
- **Observability**: Metrics with source file and line info
- **Scalability**: Read replica support and **Database Sharding**
- **Reliability**: Single-flight thundering herd protection

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
│   Shard A   │     │   Shard B   │
│ (Prim+Repl) │     │ (Prim+Repl) │
└─────────────┘     └─────────────┘
```

---

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

---

## Dynamic Shard Switching

- **MariaDB**: Transparently switches backends mid-connection when you issue `USE database` or a `COM_INIT_DB` packet.
- **PostgreSQL**: Routes to the correct shard at connection-time based on the startup message.
- **Unified Model**: Sharding is done at the **Database level** (not schemas).
- **Credentials**: User credentials must be synced across shards (Proxy forwards them as-is).

---

## Caching with TTL Hints

Add metadata comments to your SQL queries:

```sql
/* ttl:60 file:app.php line:42 */ 
SELECT * FROM users WHERE active = 1
```

- `ttl`: Cache duration in seconds
- `file`: Source file of the query (for metrics)
- `line`: Line number of the query (for metrics)

---

## Thundering Herd Protection

**Cold Cache**: Single-flight prevents concurrent DB queries for the same key while the first result is being fetched.

**Warm Cache**: Stale data is served to most clients while one background goroutine refreshes the value.

- Prevents backend saturation during heavy traffic spikes.
- Ensures consistent low latency for hits.

---

## Transaction Support

Full ACID transaction support for both protocols:

```sql
BEGIN;
UPDATE accounts SET balance = balance - 100 WHERE id = 1;
UPDATE accounts SET balance = balance + 100 WHERE id = 2;
COMMIT;
```

Transactions automatically bypass the cache and pin the session to the **primary** backend.

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

Inspect your last query results:
- `Shard`: (main / shard1 / ...)
- `Backend`: (primary / replicas[0] / cache / none)
- `Cache_hit`: (0 / 1)

---

## Prometheus Metrics

Exposed at `http://localhost:9090/metrics`:

- `tqdbproxy_query_total`: Queries by type, file, and line
- `tqdbproxy_query_latency_seconds`: Latency histograms
- `tqdbproxy_cache_hits_total`: Hit counters
- `tqdbproxy_database_queries_total`: Actual backend traffic

Labels are enriched with the `file` and `line` metadata hints.

---

## Performance

- **Zero-latency** cache hits (as fast as local query)
- **Minimal overhead** for proxied queries (>1ms)
- **High concurrency**: Handle thousands of client connections with small backend pools
- **Hot Reload**: Send `SIGHUP` to refresh backend addresses without restart

---

## Quick Start

```bash
# 1. Start the proxy
./tqdbproxy

# 2. Connect via MariaDB
mariadb -h 127.0.0.1 -P 3307 -u user -p --comments

# 3. Connect via PostgreSQL
psql -h 127.0.0.1 -p 5433 -U user -d database
```

---

## Demo

```bash
mysql -u tqdbproxy tqdbproxy -p -P 3307 --comments
```

```sql
/* ttl:60 */ select sleep(1);
show tqdb status;
```

**TQDBProxy** — Sharding, Caching, Reliability

[https://github.com/mevdschee/tqdbproxy](https://github.com/mevdschee/tqdbproxy)
