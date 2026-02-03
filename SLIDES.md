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

### Cache Expiry

- Fresh: Age between 0 and TTL = serve cache
- Stale: Age between TTL and 2 x TTL = refresh cache
- Miss: Age above 2 x TTL = cache evicted

NB: Ensure: TTL > refresh duration!

---

## Thundering Herd Protection / Single Flight

**Cold Cache**: The first request that gets a cache miss fetches the result from the database. Subsequent requests have to wait for the result.

**Warm Cache**: The first request that detects soft expiration fetches a new result from the database. Subsequent requests are served stale data.

- Prevents backend saturation during heavy traffic spikes.
- Ensures consistent low latency for hits.

---

## Caching Logic

1. Fresh **hit** => serve cache
2. Stale **hit** + in-flight => serve stale
3. Stale **hit** + first access => refresh synchronously (single-flight)
4. **Miss** + in-flight => wait for result
5. **Miss** + first access => query backend (single-flight)

---

## Query Status

mariadb> SHOW TQDB STATUS;       => SELECT * FROM pg_tqdb_status;
+---------------+---------+       variable_name |  value  
| Variable_name | Value   |      ---------------+---------
+---------------+---------+       Shard         | main
| Shard         | main    |       Backend       | primary
| Backend       | primary |      (2 rows)
+---------------+---------+

Backend = primary, replicas[n], cache, cache (stale)

---

## Prometheus Metrics

/* ttl:4 file:app.php line:42 */ SELECT SLEEP(2)

tqdbproxy_cache_hits_total{file="app.php",line="42"} 16
tqdbproxy_cache_misses_total{file="app.php",line="42"} 2
tqdbproxy_query_latency_seconds_sum{file="app.php",line="42",query_type="select"} 4.0013
tqdbproxy_query_latency_seconds_count{file="app.php",line="42",query_type="select"} 18
tqdbproxy_query_total{cached="false",file="app.php",line="42",query_type="select"} 2
tqdbproxy_query_total{cached="true",file="app.php",line="42",query_type="select"} 16

Use: curl localhost:9090/metrics -s | grep app.php

---

## Configuration

Replication & Sharding

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

## Demo

# Start proxy on port 3307 (default)
./tqdbproxy
# Connect via MariaDB client (interactive mode)
mariadb -u tqdbproxy -ptqdbproxy -P 3307 tqdbproxy --comments
# Cache sleep 2 seconds query for 4 seconds (store 8 seconds)
/* ttl:4 */ SELECT SLEEP(2); SHOW TQDB STATUS

NB: In MariaDB CLI you **must** use the "--comments" flag to preserve comments

---

## MariaDB Authentication

- Most proxies use credentials in config file
- TQDBProxy forwards credentials as-is to backend
- MariaDB uses double SHA1 hashing with nonce (salt)
- Patched Go MySQL driver, to forward handshake
- See: https://github.com/mevdschee/go-sql-driver

---

## MariaDB Shard Switching

- TQDBProxy shards at the database level (by database name).
- Some PostgreSQL proxies support schema level sharding.
- MariaDB may switch backends on `USE database` or FQN query.
- PostgreSQL routes to the correct shard at connection time.

---

## Summary

- Databases are fast, **NVMe drives** have ridiculous performance
- Trade durability for performance first, if possible (**periodic fsync**)
- 1M QPS recipe: (smart) **caches** + (read) **replicas** + (database) **shards**

---

## Disclaimer

- Built in a few weeks (**AI assisted**)
- Work in progress (**optimizing further**)
- No production use (**yet**)
- No warranties (**use at your own risk**)

---

## Questions?

Repository:
[github.com/mevdschee/tqdbproxy](https://github.com/mevdschee/tqdbproxy)

Blog post:
[tqdev.com/2026-tqdbproxy-mariadb-postgresql-proxy](https://tqdev.com/2026-tqdbproxy-mariadb-postgresql-proxy)
