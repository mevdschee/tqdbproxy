# PostgreSQL Component

The `postgres` component implements the PostgreSQL wire protocol, allowing TQDBProxy to act as a transparent or intelligent proxy for PostgreSQL databases.

## Features

- **Protocol Handshake**: Handles the initial connection and authentication between the client and the backend PostgreSQL server.
- **Command Interception**: Intercepts simple query messages ('Q') for caching and metrics.
- **Caching Integration**:
  - Checks the cache for `SELECT` queries with a TTL hint.
  - Automatically caches results returned from the backend if the query is cacheable.
- **Extended Query Protocol**: Parse/Bind/Execute messages are passed through without caching (future enhancement).

## Metrics Integration

The PostgreSQL component records detailed metrics for every query, including latency and cache status, labeled with source file and line number information extracted from SQL hints.

[Back to Index](../../README.md)
