# Cache Component

The `cache` component provides an in-memory caching layer with **thundering herd protection**.

## Implementation Details

- **Library**: Uses [TQMemory](https://github.com/mevdschee/tqmemory), a high-performance sharded cache with built-in soft-expiry support.
- **Variable TTL**: Supports per-entry TTL with soft and hard expiry.
- **Thundering Herd Protection**: Returns staleness flags (0=fresh, 1=stale, 3=needs-refresh) to enable single-flight refresh.
- **Cold Cache Single-Flight**: Uses `sync.Map` with channels to prevent concurrent DB queries for the same key.

## Key Functions

- `New(cfg CacheConfig)`: Initializes a new cache with configuration.
- `Get(key string) ([]byte, int, bool)`: Returns (value, flags, ok). Flags indicate freshness.
- `GetOrWait(key string)`: For cold cache single-flight - waits if another goroutine is fetching.
- `SetAndNotify(key, value, ttl)`: Stores result and notifies waiting goroutines.
- `Set(key, value, ttl)`: Stores a fresh result in the cache.
- `Delete(key string)`: Removes an entry.

## Staleness Flags

| Flag | Constant      | Meaning                                    |
|------|---------------|--------------------------------------------|
| 0    | `FlagFresh`   | Value is fresh                             |
| 1    | `FlagStale`   | Value is stale, refresh already in progress|
| 3    | `FlagRefresh` | First stale access - caller should refresh |

[Back to Index](../../README.md)
