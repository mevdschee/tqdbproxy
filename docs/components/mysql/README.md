# MySQL Component

The `mysql` component implements the MySQL wire protocol, allowing TQDBProxy to act as a transparent or intelligent proxy for MySQL databases.

## Features

- **Protocol Handshake**: Handles the initial connection and authentication between the client and the backend MySQL server.
- **Command Interception**: Intercepts `COM_QUERY`, `COM_STMT_PREPARE`, and `COM_STMT_EXECUTE` commands.
- **Caching Integration**:
  - Checks the cache for `SELECT` queries with a TTL hint.
  - Automatically caches results returned from the backend if the query is cacheable.
- **Prepared Statements**: Tracks statement IDs and handles caching for executed prepared statements by combining the query template and parameters into a cache key.
- **Transaction Support**: Full `BEGIN`, `COMMIT`, `ROLLBACK` support with cache bypass during transactions.

## Query Status

Use `SHOW TQDB STATUS` to see which backend served the last query:

```sql
mysql> SHOW TQDB STATUS;
+---------------+---------+
| Variable_name | Value   |
+---------------+---------+
| Backend       | primary |
| Cache_hit     | 0       |
+---------------+---------+
```

Values: `Backend` = `primary`, `replicaN`, `cache`, or `none`; `Cache_hit` = `0` or `1`.

## Metrics Integration

The MySQL component records detailed metrics for every query, including latency and cache status, labeled with source file and line number information extracted from SQL hints.

[Back to Index](../../README.md)
