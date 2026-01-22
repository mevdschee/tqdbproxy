# @tqdbproxy/postgres

TypeScript PostgreSQL client library for TQDBProxy with TTL-aware caching.

## Installation

```bash
npm install @tqdbproxy/postgres pg
```

## Requirements

- Node.js 16 or higher
- pg (node-postgres) package

## Usage

### Basic Example

```typescript
import { Database } from '@tqdbproxy/postgres';

async function main() {
  // Create a new connection through TQDBProxy
  const db = new Database({
    host: 'localhost',
    port: 5433,
    user: 'postgres',
    password: 'password',
    database: 'test'
  });

  await db.connect();

  // Standard query (no caching)
  const result = await db.query("SELECT * FROM users");
  console.log(result.rows);

  // Query with 60-second cache TTL (note: PostgreSQL uses $1, $2 for placeholders)
  const userResult = await db.queryWithTTL(60, "SELECT * FROM users WHERE id = $1", [1]);
  console.log(userResult.rows);

  await db.end();
}

main();
```

### Wrapping an Existing Client

```typescript
import { Client } from 'pg';
import { Database } from '@tqdbproxy/postgres';

async function main() {
  // Create a standard pg client
  const client = new Client({
    host: 'localhost',
    port: 5433,
    database: 'test'
  });

  await client.connect();

  // Wrap it to add TTL support
  const db = Database.wrap(client);

  // Now you can use queryWithTTL
  const result = await db.queryWithTTL(120, "SELECT * FROM products WHERE category = $1", ['electronics']);
  console.log(result.rows);
}

main();
```

## API

### `constructor(config: ClientConfig)`

Creates a new Database instance with a pg client.

**Parameters:**
- `config` - pg client configuration object

### `static wrap(client: Client): Database`

Wraps an existing pg client to add TTL-aware query methods.

**Parameters:**
- `client` - Existing pg client

**Returns:** Database instance

### `async connect(): Promise<void>`

Connects to the database.

### `async queryWithTTL<R>(ttl: number, text: string, values?: any[]): Promise<QueryResult<R>>`

Executes a query with a cache TTL hint.

The method automatically:
- Captures the caller's file and line number using Error stack trace
- Constructs a SQL comment hint: `/* ttl:X file:Y line:Z */`
- Prepends the hint to your query
- Executes the query using pg

**Parameters:**
- `ttl` - Cache TTL in seconds
- `text` - SQL query (can contain placeholders like $1, $2, etc.)
- `values` - Query parameters array (optional)

**Returns:** Promise<QueryResult<R>>

### `async query<R>(text: string, values?: any[]): Promise<QueryResult<R>>`

Executes a standard query without caching (pass-through to pg).

**Parameters:**
- `text` - SQL query
- `values` - Query parameters array (optional)

**Returns:** Promise<QueryResult<R>>

### `getClient(): Client`

Returns the underlying pg client.

**Returns:** pg Client

### `async end(): Promise<void>`

Closes the database connection.

## How It Works

When you call `queryWithTTL()`, the library:

1. Captures your call location (file and line number) using Error stack trace
2. Constructs a hint comment: `/* ttl:60 file:index.ts line:42 */`
3. Prepends it to your query
4. Sends the modified query to TQDBProxy

For example, this code:

```typescript
const result = await db.queryWithTTL(60, "SELECT * FROM users WHERE id = $1", [1]);
```

Becomes:

```sql
/* ttl:60 file:index.ts line:15 */ SELECT * FROM users WHERE id = $1
```

The TQDBProxy server parses this hint and caches the query result for 60 seconds.

## PostgreSQL-Specific Notes

- PostgreSQL uses `$1`, `$2`, etc. for parameter placeholders (not `?` like MySQL)
- Make sure to connect through TQDBProxy port (default: 5433) not directly to PostgreSQL
- Call `connect()` before executing queries

## TypeScript Support

This library is written in TypeScript and includes full type definitions. You get autocomplete and type checking out of the box.

## Testing

```bash
cd clients/ts/postgres
npm install
npm test
```

## License

MIT License - Same as TQDBProxy project.
