# TQDBProxy

---

## What is TQDBProxy?

- A proxy for **MariaDB** and **PostgreSQL**
- Fast in-memory **query result caching**
- Provides **single-flight** for cold and warm cache
- Routes cacheable queries to **read replicas**
- Metrics with query **source file + line number**
- Database **sharding** based on database name

---

## Architecture
 
 ```
┌────────────────────────┐    ┌────────────────────────┐
│  Application Server 1  │    │  Application Server 2  │
│ ┌────────┐ ┌─────────┐ │    │ ┌────────┐ ┌─────────┐ │
│ │TQMemory|+|TQDBProxy| │    │ │TQMemory|+|TQDBProxy│ │
│ └────────┘ └────┬────┘ │    │ └────────┘ └────┬────┘ │
└─────────────────┼──────┘    └─────────────────┼──────┘
                  │                             │
                  └──────────────┬──────────────┘
                                 │
         ┌───────────────────────┴─────┐
┌────────▼────────┐           ┌────────▼─────────┐
│   1 Primary     │           │ N Read Replicas  │─┐
│ (Writes/Reads)  │           │ (Cachable Reads) │ │
└─────────────────┘           └─┬────────────────┘ │
                                └──────────────────┘
 ```

---

## Caching with TTL Hints

Add metadata comments to your SQL queries:

```sql
/* ttl:60 file:app.php line:42 */ 
SELECT * FROM `users` WHERE `active` = 1
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

## Query Flow

1. Check incoming query, analyze:
  a. Write, forward to primary
  b. Transaction, forward to primary
  c. Hint, lookup in local cache
    1. **Hit**, check if TTL expired (soft)
      a. No, return cached result
      b. Yes, check if in-flight
        1. Yes, return stale data
        2. No, forward to replica pool
    2. **Miss**, check if in-flight
      a. Yes, wait for result
      b. No, forward to replica pool
  d. Else, forward to primary (consistent read)

---

## Query Status Inspection

Custom commands
- **MariaDB**: SHOW TQDB STATUS;
- **PostgreSQL**: SELECT * FROM pg_tqdb_status;

Show your last query results
- **Shard**: main / shard1 / ...
- **Backend**: primary / replicas[n] / cache

---

## Prometheus Metrics

Exposed at `http://localhost:9090/metrics`:

- `tqdbproxy_query_total`: Queries by type, file, and line
- `tqdbproxy_query_latency_seconds`: Latency histograms
- `tqdbproxy_cache_hits_total`: Hit counters
- `tqdbproxy_database_queries_total`: Actual backend traffic

Labels are enriched with the `file` and `line` metadata hints.

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
mysql -u tqdbproxy tqdbproxy -ptqdbproxy -P 3307 --comments
```

```sql
/* ttl:2 */ select sleep(1); show tqdb status;
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

- **MariaDB**: Switches backends mid-connection when you issue `USE database` or FQN query.
- **PostgreSQL**: Routes to the correct shard at connection-time based on the startup message.
- **Unified Model**: Sharding is done at the **Database level** (not schemas).
- **Credentials**: User credentials must be synced across shards (Proxy forwards them as-is).

---

**TQDBProxy** — Sharding, Caching, Reliability

[https://github.com/mevdschee/tqdbproxy](https://github.com/mevdschee/tqdbproxy)
