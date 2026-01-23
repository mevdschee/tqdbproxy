# PostgreSQL Component

The `postgres` component implements the PostgreSQL wire protocol, allowing TQDBProxy to act as a transparent or intelligent proxy for PostgreSQL databases.

## Features

- **Protocol Handshake**: Handles the initial connection, SSL negotiation denial, and authentication with the backend PostgreSQL server.
- **Command Interception**: Intercepts simple query messages ('Q') for caching and metrics.
- **Caching Integration**:
  - Checks the cache for `SELECT` queries with a TTL hint.
  - Automatically caches results returned from the backend if the query is cacheable.
- **Backend Connection**: Uses Go's `database/sql` with `lib/pq` driver for backend connections.

## Query Status

Use `SELECT * FROM pg_tqdb_status` to see which backend served the last query:

```sql
tqdbproxy=> SELECT * FROM pg_tqdb_status;
 variable_name |  value  
---------------+---------
 Backend       | primary
 Cache_hit     | 0
(2 rows)
```

Values: `Backend` = `primary`, `replicaN`, `cache`, or `none`; `Cache_hit` = `0` or `1`.

## Metrics Integration

The PostgreSQL component records detailed metrics for every query, including latency and cache status, labeled with source file and line number information extracted from SQL hints.

[Back to Index](../../README.md)
