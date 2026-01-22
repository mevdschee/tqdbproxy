# TQDBProxy

A high-performance MySQL and PostgreSQL proxy with query caching, metrics, and full transaction support.

## Features

- **Query Caching**: Cache SELECT queries with configurable TTL
- **Caller Metadata**: Track queries by source file and line number
- **Metrics**: Prometheus metrics for cache hits/misses, query latency, and more
- **Read Replica Support**: Automatic routing of SELECT queries to replicas
- **Transaction Support**: Full BEGIN/COMMIT/ROLLBACK support
- **Interactive Mode**: Full MySQL client support without restrictions

## Quick Start

```bash
# Start the proxy
./tqdbproxy

# Connect via MySQL client (interactive mode)
mysql -u php-crud-api -p -P 3307 php-crud-api --comments
```

## Using Metadata Comments

Add caller metadata to your queries for better observability:

```sql
/* ttl:60 file:app.php line:42 */ SELECT * FROM users WHERE active = 1
```

### Important: MySQL CLI Requires `--comments` Flag

When testing with the MySQL CLI, you **must** use the `--comments` flag to preserve metadata comments:

```bash
# Interactive mode with metadata support
mysql -u php-crud-api -p -P 3307 php-crud-api --comments

# Batch mode with metadata
mysql -u user -P 3307 database --comments -e "/* ttl:60 file:test.php line:1 */ SELECT * FROM table"
```

**Why?** The MySQL CLI strips comments by default before sending queries to the server. The `--comments` flag preserves them.

**Note:** Application code (PHP, Go, Python, etc.) sends comments by default - the `--comments` flag is only needed for the MySQL CLI.

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

## Metrics

Access Prometheus metrics at `http://localhost:9090/metrics`:

```bash
curl http://localhost:9090/metrics | grep tqdbproxy_query_total
```

Metrics include file and line labels when metadata comments are used.

## Documentation

See [docs/README.md](docs/README.md) for more information.