# @tqdbproxy/mariadb

TypeScript MariaDB client library for TQDBProxy with TTL-aware caching.

## Installation

```bash
npm install @tqdbproxy/mariadb mysql2
```

## Requirements

- Node.js 16 or higher
- mysql2 package

## Usage

### Basic Example

```typescript
import { Database } from '@tqdbproxy/mariadb';

async function main() {
  // Create a new connection through TQDBProxy
  const db = await Database.create({
    host: 'localhost',
    port: 3307,
    user: 'root',
    password: 'password',
    database: 'test'
  });

  // Standard query (no caching)
  const [rows] = await db.query("SELECT * FROM users");
  console.log(rows);

  // Query with 60-second cache TTL
  const [users] = await db.queryWithTTL(60, "SELECT * FROM users WHERE id = ?", [1]);
  console.log(users);

  await db.close();
}

main();
```

### Wrapping an Existing Connection

```typescript
import mysql from 'mysql2/promise';
import { Database } from '@tqdbproxy/mariadb';

async function main() {
  // Create a standard mysql2 connection
  const connection = await mysql.createConnection({
    host: 'localhost',
    port: 3307,
    database: 'test'
  });

  // Wrap it to add TTL support
  const db = Database.wrap(connection);

  // Now you can use queryWithTTL
  const [products] = await db.queryWithTTL(120, "SELECT * FROM products WHERE category = ?", ['electronics']);
  console.log(products);
}

main();
```

## API

### `static async create(config: ConnectionOptions): Promise<Database>`

Creates a new Database instance with a mysql2 connection.

**Parameters:**
- `config` - mysql2 connection configuration object

**Returns:** Promise<Database>

### `static wrap(connection: Connection): Database`

Wraps an existing mysql2 connection to add TTL-aware query methods.

**Parameters:**
- `connection` - Existing mysql2 connection

**Returns:** Database instance

### `async queryWithTTL<T>(ttl: number, sql: string, values?: any): Promise<[T, FieldPacket[]]>`

Executes a query with a cache TTL hint.

The method automatically:
- Captures the caller's file and line number using Error stack trace
- Constructs a SQL comment hint: `/* ttl:X file:Y line:Z */`
- Prepends the hint to your query
- Executes the query using mysql2

**Parameters:**
- `ttl` - Cache TTL in seconds
- `sql` - SQL query (can contain placeholders)
- `values` - Query parameters (optional)

**Returns:** Promise with [rows, fields]

### `async query<T>(sql: string, values?: any): Promise<[T, FieldPacket[]]>`

Executes a standard query without caching (pass-through to mysql2).

**Parameters:**
- `sql` - SQL query
- `values` - Query parameters (optional)

**Returns:** Promise with [rows, fields]

### `async execute<T>(sql: string, values?: any): Promise<[T, FieldPacket[]]>`

Executes a prepared statement (pass-through to mysql2).

**Parameters:**
- `sql` - SQL query
- `values` - Query parameters (optional)

**Returns:** Promise with [rows, fields]

### `getConnection(): Connection`

Returns the underlying mysql2 connection.

**Returns:** mysql2 Connection

### `async close(): Promise<void>`

Closes the database connection.

## How It Works

When you call `queryWithTTL()`, the library:

1. Captures your call location (file and line number) using Error stack trace
2. Constructs a hint comment: `/* ttl:60 file:index.ts line:42 */`
3. Prepends it to your query
4. Sends the modified query to TQDBProxy

For example, this code:

```typescript
const [rows] = await db.queryWithTTL(60, "SELECT * FROM users WHERE id = ?", [1]);
```

Becomes:

```sql
/* ttl:60 file:index.ts line:15 */ SELECT * FROM users WHERE id = ?
```

The TQDBProxy server parses this hint and caches the query result for 60 seconds.

## TypeScript Support

This library is written in TypeScript and includes full type definitions. You get autocomplete and type checking out of the box.

## Testing

```bash
cd clients/ts/mariadb
npm install
npm test
```

## License

MIT License - Same as TQDBProxy project.
