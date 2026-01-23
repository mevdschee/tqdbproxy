# tqdbproxy-mysql

PHP MySQL client library for TQDBProxy with TTL-aware caching.

## Installation

```bash
composer require mevdschee/tqdbproxy-mysql
```

## Requirements

- PHP 8.0 or higher
- PDO extension

## Usage

### Basic Example

```php
<?php
require 'vendor/autoload.php';

use TQDBProxy\MariaDB\Database;
use PDO;

// Create a new connection through TQDBProxy
$db = new Database(
    'mysql:host=localhost;port=3307;dbname=mydb',
    'username',
    'password'
);

// Standard query (no caching)
$stmt = $db->query("SELECT * FROM users");
$users = $stmt->fetchAll(PDO::FETCH_ASSOC);

// Query with 60-second cache TTL
$stmt = $db->queryWithTTL(60, "SELECT * FROM users WHERE id = ?", [1]);
$user = $stmt->fetch(PDO::FETCH_ASSOC);
```

### Wrapping an Existing PDO Instance

```php
use TQDBProxy\MySQL\Database;
use PDO;

// Create a standard PDO connection
$pdo = new PDO('mysql:host=localhost;port=3307;dbname=mydb', 'user', 'pass');

// Wrap it to add TTL support
$db = Database::wrap($pdo);

// Now you can use queryWithTTL
$stmt = $db->queryWithTTL(120, "SELECT * FROM products WHERE category = ?", ['electronics']);
```

## API

### `__construct(string $dsn, ?string $username = null, ?string $password = null, ?array $options = null)`

Creates a new Database instance with a PDO connection.

**Parameters:**
- `$dsn` - Data Source Name
- `$username` - Database username (optional)
- `$password` - Database password (optional)
- `$options` - PDO options array (optional)

### `static wrap(PDO $pdo): self`

Wraps an existing PDO instance to add TTL-aware query methods.

**Parameters:**
- `$pdo` - Existing PDO instance

**Returns:** Database instance

### `queryWithTTL(int $ttl, string $query, array $params = []): PDOStatement`

Executes a query with a cache TTL hint.

The method automatically:
- Captures the caller's file and line number using `debug_backtrace()`
- Constructs a SQL comment hint: `/* ttl:X file:Y line:Z */`
- Prepends the hint to your query
- Executes the query using PDO

**Parameters:**
- `$ttl` - Cache TTL in seconds
- `$query` - SQL query (can contain placeholders)
- `$params` - Query parameters array (optional)

**Returns:** PDOStatement

### `query(string $query): PDOStatement`

Executes a standard query without caching (pass-through to PDO).

**Parameters:**
- `$query` - SQL query

**Returns:** PDOStatement

### `prepare(string $query): PDOStatement|false`

Prepares a statement (pass-through to PDO).

**Parameters:**
- `$query` - SQL query

**Returns:** PDOStatement or false on failure

### `getPDO(): PDO`

Returns the underlying PDO instance.

**Returns:** PDO instance

## How It Works

When you call `queryWithTTL()`, the library:

1. Captures your call location (file and line number) using `debug_backtrace()`
2. Constructs a hint comment: `/* ttl:60 file:index.php line:42 */`
3. Prepends it to your query
4. Sends the modified query to TQDBProxy

For example, this code:

```php
$stmt = $db->queryWithTTL(60, "SELECT * FROM users WHERE id = ?", [1]);
```

Becomes:

```sql
/* ttl:60 file:index.php line:15 */ SELECT * FROM users WHERE id = ?
```

The TQDBProxy server parses this hint and caches the query result for 60 seconds.

## Testing

```bash
cd clients/php/mysql
composer install
vendor/bin/phpunit tests/
```

## License

MIT License - Same as TQDBProxy project.
