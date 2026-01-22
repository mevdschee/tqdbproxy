package cache

import (
	"time"

	"github.com/maypok86/otter"
)

// Cache wraps Otter cache for query result caching
type Cache struct {
	store otter.CacheWithVariableTTL[string, []byte]
}

// New creates a new cache with the specified max size
func New(maxSize int) (*Cache, error) {
	store, err := otter.MustBuilder[string, []byte](maxSize).
		WithVariableTTL().
		Build()
	if err != nil {
		return nil, err
	}
	return &Cache{store: store}, nil
}

// Get retrieves a cached result by key
func (c *Cache) Get(key string) ([]byte, bool) {
	return c.store.Get(key)
}

// Set stores a result with the specified TTL
func (c *Cache) Set(key string, value []byte, ttl time.Duration) {
	c.store.Set(key, value, ttl)
}

// Delete removes an entry from the cache
func (c *Cache) Delete(key string) {
	c.store.Delete(key)
}
