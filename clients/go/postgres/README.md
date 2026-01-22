# tqdbproxy-go-postgres

PostgreSQL client library for tqdbproxy that adds cache TTL hints and caller metadata to SQL queries.

## Installation

```bash
go get github.com/mevdschee/tqdbproxy/clients/go/postgres
```

You'll also need a PostgreSQL driver:

```bash
# Using lib/pq
go get github.com/lib/pq

# Or using pgx
go get github.com/jackc/pgx/v5/stdlib
```

## Usage

### Basic Example

```go
package main

import (
    "context"
    "log"
    
    postgres "github.com/mevdschee/tqdbproxy/clients/go/postgres"
    _ "github.com/lib/pq" // PostgreSQL driver
)

func main() {
    // Open a connection through tqdbproxy
    db, err := postgres.Open("postgres", "postgres://user:pass@localhost:5432/mydb")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()
    
    ctx := context.Background()
    
    // Standard query (no caching)
    rows, err := db.Query("SELECT * FROM users WHERE id = $1", 1)
    if err != nil {
        log.Fatal(err)
    }
    defer rows.Close()
    
    // Query with 60-second cache TTL
    rows, err = db.QueryWithTTL(ctx, 60, "SELECT * FROM users WHERE id = $1", 1)
    if err != nil {
        log.Fatal(err)
    }
    defer rows.Close()
    
    // Single row query with cache
    row := db.QueryRowWithTTL(ctx, 120, "SELECT name, email FROM users WHERE id = $1", 1)
    var name, email string
    if err := row.Scan(&name, &email); err != nil {
        log.Fatal(err)
    }
}
```

### Wrapping an Existing Connection

```go
import (
    "database/sql"
    postgres "github.com/mevdschee/tqdbproxy/clients/go/postgres"
    _ "github.com/lib/pq"
)

// Open a standard connection
sqlDB, err := sql.Open("postgres", "postgres://user:pass@localhost:5432/mydb")
if err != nil {
    log.Fatal(err)
}

// Wrap it to add TTL support
db := postgres.Wrap(sqlDB)

// Now you can use QueryWithTTL
rows, err := db.QueryWithTTL(ctx, 60, "SELECT * FROM products")
```

## API

### `Open(driverName, dataSourceName string) (*DB, error)`

Opens a new database connection with the specified driver and data source name.

### `Wrap(db *sql.DB) *DB`

Wraps an existing `*sql.DB` to add TTL-aware query methods.

### `QueryWithTTL(ctx context.Context, ttl int, query string, args ...any) (*sql.Rows, error)`

Executes a query with a cache TTL hint. The TTL is specified in seconds.

The method automatically:
- Captures the caller's file and line number using `runtime.Caller()`
- Constructs a SQL comment hint: `/* ttl:X file:Y line:Z */`
- Prepends the hint to your query
- Executes the query using the underlying `*sql.DB`

### `QueryRowWithTTL(ctx context.Context, ttl int, query string, args ...any) *sql.Row`

Similar to `QueryWithTTL`, but for queries that return at most one row.

## How It Works

When you call `QueryWithTTL`, the library:

1. Captures your call location (file and line number)
2. Constructs a hint comment: `/* ttl:60 file:main.go line:42 */`
3. Prepends it to your query
4. Sends the modified query to tqdbproxy

For example, this code:

```go
rows, err := db.QueryWithTTL(ctx, 60, "SELECT * FROM users WHERE id = $1", 1)
```

Becomes:

```sql
/* ttl:60 file:main.go line:42 */ SELECT * FROM users WHERE id = $1
```

The tqdbproxy server parses this hint and caches the query result for 60 seconds.

## Testing

```bash
cd clients/go/postgres
go test -v
```

## License

Same as tqdbproxy project.
