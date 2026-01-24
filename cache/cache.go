package cache

import (
	"sync"
	"time"

	"github.com/mevdschee/tqmemory/pkg/tqmemory"
)

// Flag values returned by Get
const (
	FlagFresh   = 0 // Value is fresh
	FlagStale   = 1 // Value is stale (serve while refreshing)
	FlagRefresh = 3 // First stale access - caller should refresh
)

// Cache wraps TQMemory cache for query result caching with thundering herd protection
type Cache struct {
	store    *tqmemory.ShardedCache
	inflight sync.Map // key -> *flight for cold cache single-flight
}

// flight represents an in-flight cache population request
type flight struct {
	done  chan struct{}
	value []byte
}

// CacheConfig holds configuration for the cache
type CacheConfig struct {
	MaxMemory       int64   // Maximum memory in bytes
	Workers         int     // Number of worker goroutines
	StaleMultiplier float64 // Hard expiry = TTL * StaleMultiplier
}

// DefaultCacheConfig returns sensible defaults
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		MaxMemory:       64 * 1024 * 1024, // 64MB
		Workers:         4,
		StaleMultiplier: 2.0,
	}
}

// New creates a new cache with the specified configuration
func New(cfg CacheConfig) (*Cache, error) {
	tqcfg := tqmemory.DefaultConfig()
	tqcfg.MaxMemory = cfg.MaxMemory
	tqcfg.StaleMultiplier = cfg.StaleMultiplier

	store, err := tqmemory.NewSharded(tqcfg, cfg.Workers)
	if err != nil {
		return nil, err
	}
	return &Cache{store: store}, nil
}

// Get retrieves a cached result by key.
// Returns (value, flags, ok) where flags indicate freshness:
//   - FlagFresh (0): Value is fresh
//   - FlagStale (1): Value is stale, already being refreshed
//   - FlagRefresh (3): Value is stale, caller should refresh
func (c *Cache) Get(key string) ([]byte, int, bool) {
	value, _, flags, err := c.store.Get(key)
	if err != nil {
		return nil, 0, false
	}
	if value == nil {
		return nil, 0, false
	}
	return value, flags, true
}

// GetOrWait implements cold cache single-flight pattern.
// Returns (value, flags, ok, waited) where waited indicates if we waited for another goroutine.
// If not ok and not waited, the caller should fetch from DB and call SetAndNotify.
func (c *Cache) GetOrWait(key string) ([]byte, int, bool, bool) {
	// First try normal get
	if value, flags, ok := c.Get(key); ok {
		return value, flags, true, false
	}

	// Check if another goroutine is already fetching this key
	f := &flight{done: make(chan struct{})}
	if existing, loaded := c.inflight.LoadOrStore(key, f); loaded {
		// Wait for the other goroutine to finish
		existingFlight := existing.(*flight)
		<-existingFlight.done
		// Now try to get from cache
		if value, flags, ok := c.Get(key); ok {
			return value, flags, true, true
		}
		return nil, 0, false, true // Other request failed or TTL=0
	}

	// We are the first - return immediately, caller will fetch and call SetAndNotify
	return nil, 0, false, false
}

// SetAndNotify stores a value and notifies any waiting goroutines.
// Use this after GetOrWait returns (nil, _, false, false).
func (c *Cache) SetAndNotify(key string, value []byte, ttl time.Duration) {
	if ttl > 0 {
		c.store.Set(key, value, ttl)
	}

	// Notify waiters
	if f, ok := c.inflight.LoadAndDelete(key); ok {
		close(f.(*flight).done)
	}
}

// CancelInflight cancels an in-flight request (e.g., on error).
// Waiters will receive (nil, _, false, true) from GetOrWait.
func (c *Cache) CancelInflight(key string) {
	if f, ok := c.inflight.LoadAndDelete(key); ok {
		close(f.(*flight).done)
	}
}

// Set stores a result with the specified TTL (for backward compatibility)
func (c *Cache) Set(key string, value []byte, ttl time.Duration) {
	c.store.Set(key, value, ttl)
}

// Delete removes an entry from the cache
func (c *Cache) Delete(key string) {
	c.store.Delete(key)
}

// Close closes the cache
func (c *Cache) Close() error {
	return c.store.Close()
}
