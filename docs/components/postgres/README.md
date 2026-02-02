# PostgreSQL Component

The `postgres` component implements the PostgreSQL wire protocol, allowing TQDBProxy to act as a transparent or intelligent proxy for PostgreSQL databases.

## Features

- **Protocol Handshake**: Handles the initial connection, SSL negotiation denial, and authentication with the backend PostgreSQL server.
- **Command Interception**: Intercepts simple query messages ('Q') for caching and metrics.
- **Caching Integration**:
  - Checks the cache for `SELECT` queries with a TTL hint.
  - Automatically caches results returned from the backend if the query is cacheable.
  - **Thundering Herd Protection**: Serves stale data while refreshing in background.
  - **Cold Cache Single-Flight**: Prevents concurrent DB queries for the same uncached key.
- **Database Sharding**: Routes client connections to the correct shard based on the `database` parameter in the startup message.
- **Backend Connection**: Uses Go's `database/sql` with `lib/pq` driver for backend connections.

## Query Status

Use `SELECT * FROM pg_tqdb_status` to see which backend served the last query:

```sql
tqdbproxy=> SELECT * FROM pg_tqdb_status;
 variable_name |  value  
---------------+---------
 Shard         | main
 Backend       | primary
(3 rows)
```

Values: `Backend` = `primary`, `replicaN`, `cache`, `cache (stale)` or `none`;

## Unix Socket Support

The PostgreSQL proxy can listen on both TCP and a Unix socket simultaneously. Use the `socket` option to specify a Unix socket path:

```ini
[postgres]
listen = :5433
socket = /var/run/tqdbproxy/.s.PGSQL.5433
default = main

[postgres.main]
primary = 127.0.0.1:5432
```

Connect via TCP or Unix socket:

```bash
# TCP
psql -h 127.0.0.1 -p 5433 -U tqdbproxy -d tqdbproxy

# Unix socket
psql -h /var/run/tqdbproxy -p 5433 -U tqdbproxy -d tqdbproxy
```

## Metrics Integration

The PostgreSQL component records detailed metrics for every query, including latency and cache status, labeled with source file and line number information extracted from SQL hints.

[Back to Index](../../README.md)
