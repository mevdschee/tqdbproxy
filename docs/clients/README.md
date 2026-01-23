# Client Libraries

To make adoption effortless, TQDBProxy includes client libraries that wrap existing native drivers. These libraries automatically inject the necessary SQL hints for caching and observability.

## Supported Languages

- **[Go](../../clients/go/)**: Supports both MariaDB and PostgreSQL.
- **[PHP](../../clients/php/)**: Supports both MariaDB and PostgreSQL.
- **[TypeScript](../../clients/ts/)**: Supports both MariaDB and PostgreSQL.

## How it Works

The client libraries wrap the standard database connection objects and provide methods that allow passing an optional TTL. When a query is executed, the library:
1. Captures the caller's file and line number.
2. Formats a SQL comment hint (e.g., `/* ttl:60 file:user.go line:42 */`).
3. Prepends or appends the hint to the query before sending it to the proxy.

## Usage Example (Go)

```go
// Standard query through the proxy
rows, err := db.Query("SELECT * FROM users WHERE id = ?", 1)

// Query with 60-second caching
rows, err := db.QueryWithTTL(60, "SELECT * FROM users WHERE id = ?", 1)
```

[Back to Documentation Home](../README.md)
