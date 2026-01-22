# Cache Component

The `cache` component provides an in-memory caching layer for query results.

## Implementation Details

- **Library**: It uses the [Otter](https://github.com/maypok86/otter) library, which is a high-performance, contention-free Java cache inspired Go cache.
- **Variable TTL**: Supports per-entry TTL (Time-To-Live), allowing different queries to be cached for different durations.
- **Storage**: Results are stored as raw byte arrays (`[]byte`), making it protocol-agnostic for storing database responses.

## Key Functions

- `New(maxSize int)`: Initializes a new cache with a maximum number of entries.
- `Get(key string)`: Retrieves a cached result.
- `Set(key string, value []byte, ttl time.Duration)`: Stores a result with a specific TTL.
- `Delete(key string)`: Manually removes an entry from the cache.

[Back to Components](../README.md)
