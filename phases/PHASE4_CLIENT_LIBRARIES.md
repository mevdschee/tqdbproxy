# Phase 4: Client Libraries

## Goal

Create six client libraries (Go, PHP, TypeScript × MySQL, PostgreSQL) that wrap native drivers and inject cache-TTL and caller metadata hints.

## Deliverables

### 4.1 Go Client Libraries

#### tqdbproxy-go-mysql
- Wrap `database/sql` with MySQL driver
- Add `QueryWithTTL(ctx, ttl, query, args...)` method
- Automatically inject `/* file:X line:Y ttl:Z */` comment
- Use `runtime.Caller()` to capture call site

#### tqdbproxy-go-postgres
- Wrap `database/sql` with PostgreSQL driver
- Same API as MySQL variant
- Support for pgx driver as alternative

### 4.2 PHP Client Libraries

#### tqdbproxy-php-mysql
- Wrap PDO MySQL
- Add `queryWithTTL($ttl, $query, $params)` method
- Use `debug_backtrace()` to capture call site
- Composer package

#### tqdbproxy-php-postgres
- Wrap PDO PostgreSQL
- Same API as MySQL variant
- Composer package

### 4.3 TypeScript Client Libraries

#### tqdbproxy-ts-mysql
- Wrap mysql2 package
- Add `queryWithTTL(ttl, query, params)` method
- Use Error stack trace to capture call site
- npm package

#### tqdbproxy-ts-postgres
- Wrap pg package
- Same API as MySQL variant
- npm package

## API Design

All libraries follow the same pattern:

```
// Standard query (no caching)
db.query("SELECT * FROM users WHERE id = ?", [1])

// Query with 60-second cache TTL
db.queryWithTTL(60, "SELECT * FROM users WHERE id = ?", [1])
```

## Success Criteria

- [ ] All six libraries published to respective package managers
- [ ] Existing code can migrate by changing import and adding TTL parameter
- [ ] Caller metadata correctly captured in all languages
- [ ] Integration tests pass with proxy

## Estimated Effort

2-3 weeks
